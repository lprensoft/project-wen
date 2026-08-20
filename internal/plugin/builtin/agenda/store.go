// Package agenda 提供日程的系统插件：角色在对话之外有自己的一天——早上醒来排一张
// 两到四件事的表，到点去做、做完回来带着一句经历，遇到变故会改、会挪、会跟对方商量；
// 表里的「和谁」来自人物库里有名有姓的人。
//
// 它与相邻插件的分工：scheduler 管对对方的提醒与承诺，这里管角色自己要去做的事；
// presence 记此刻在哪、在做什么的定格，这里给出的是一天的粗粒度安排，活动开始与结束
// 的轮次里由模型顺手更新现场；memory 按时间线记过去，这里只记今天与将来——结束轮次
// 写下的经历以助手消息落进会话，时间线自然收进去，本插件不另写记忆。
//
// 全部只用现有契约（StateDir / Sessions / RunTurn / TurnPrompt / TurnObserver /
// Stoppable / Notice / StatusReporter），核心不知道「日程」这回事；与心跳、消息通道的
// 衔接走 internal/cue（开口理由）与 internal/availability（忙碌状态），两者同级。
package agenda

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 字段上限。表与约定随每轮注入，单条必须有确定上界；超长报错让模型压缩后重试，
// 不悄悄截断——被截掉的部分模型不知道丢了。
const (
	maxTitleRunes   = 30
	maxPlaceRunes   = 30
	maxOutcomeRunes = 80
	maxNoteRunes    = 60
	maxWith         = 4
	hardMaxItems    = 6 // 项数的硬上限，配置项 max_items 不能超过它
	maxCommitments  = 50
)

// 项的状态。
const (
	statusPlanned   = "planned"
	statusOngoing   = "ongoing"
	statusDone      = "done"
	statusSkipped   = "skipped"
	statusDeferred  = "deferred"
	statusCancelled = "cancelled"
)

// flex 三档：能不能挪。
var flexLevels = []string{"可挪", "尽量守", "不能动"}

const (
	flexMovable = "可挪"
	flexTry     = "尽量守"
	flexFixed   = "不能动"
)

// busy 三档：做这件事时能不能回消息，与 availability 的档位同一套词。
var busyLevels = []string{"轻忙", "重忙", "不回"}

const defaultBusy = "轻忙"

// Item 是当天表里的一项。
type Item struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Start          string   `json:"start"` // HH:MM 本地时间
	End            string   `json:"end"`   // HH:MM，早于 Start 视为次日
	Place          string   `json:"place,omitempty"`
	With           []string `json:"with,omitempty"`
	WithUser       bool     `json:"with_user,omitempty"`
	FromCommitment string   `json:"from_commitment,omitempty"`
	Flex           string   `json:"flex"`
	Busy           string   `json:"busy"`
	Status         string   `json:"status"`
	Outcome        string   `json:"outcome,omitempty"`
	// StartFired / EndFired 是开始 / 结束轮次**已派发**的时刻：记「上次发生」不记倒计时，
	// 派发前落盘，重启不重发。SoonFired 是「快到了」的开口理由投出的时刻，同理。
	StartFired time.Time `json:"start_fired,omitempty"`
	EndFired   time.Time `json:"end_fired,omitempty"`
	SoonFired  time.Time `json:"soon_fired,omitempty"`
}

// terminal 报告该项是否已到终态（做完、跳过、延期、取消）。
func (it *Item) terminal() bool {
	switch it.Status {
	case statusDone, statusSkipped, statusDeferred, statusCancelled:
		return true
	}
	return false
}

// Attempts 记录某一天规划轮次已尝试的次数：失败两次就到明天再说。
type Attempts struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// Plan 是一天的表。文件里始终只有最近的一张：过去的日子由时间线记经历，不在这里归档。
type Plan struct {
	Date      string    `json:"date"` // 逻辑日期 YYYY-MM-DD
	Weekday   string    `json:"weekday"`
	PlannedAt time.Time `json:"planned_at"`
	Note      string    `json:"note,omitempty"`
	Items     []Item    `json:"items"`
	// Attempts 与 Date 无关：它记的是「今天」的规划尝试次数，而 Date 可能还是昨天。
	Attempts Attempts `json:"plan_attempts"`
}

// item 按 id 查找，找不到返回 nil。
func (pl *Plan) item(id string) *Item {
	for i := range pl.Items {
		if pl.Items[i].ID == id {
			return &pl.Items[i]
		}
	}
	return nil
}

// Commitment 是一条未来约定：定在某天某时的事。
type Commitment struct {
	ID       string    `json:"id"`
	Date     string    `json:"date"`  // YYYY-MM-DD
	Start    string    `json:"start"` // HH:MM
	End      string    `json:"end,omitempty"`
	Title    string    `json:"title"`
	With     []string  `json:"with,omitempty"`
	WithUser bool      `json:"with_user,omitempty"`
	Place    string    `json:"place,omitempty"`
	Note     string    `json:"note,omitempty"`
	Flex     string    `json:"flex"`
	Created  time.Time `json:"created"`
	Planned  bool      `json:"planned,omitempty"` // 已排进当天的表
}

// commitmentsFile 是约定文件的形状。带一个自增序号：约定会被取消、被清理，
// 按「现存最大号 + 1」取号会让新约定顶替旧号，模型在同一轮对话里还记着旧的那条。
type commitmentsFile struct {
	NextID int          `json:"next_id"`
	Items  []Commitment `json:"items"`
}

// Store 管理一个可见域的表与约定：两个 JSON 文件，每次操作重新读取不做缓存
// （文件可能被用户在进程外修改，体量又小）。写入在锁内同步完成——不起 goroutine 写，
// 就不存在晚到的旧快照盖掉新进展的问题。
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore 建立指向 dir 的日程库。
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) planPath() string        { return filepath.Join(s.dir, "plan.json") }
func (s *Store) commitmentsPath() string { return filepath.Join(s.dir, "commitments.json") }

// LoadPlan 返回当前的表；文件不存在时返回零值（Date 为空）。
func (s *Store) LoadPlan() (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadPlan()
}

func (s *Store) loadPlan() (Plan, error) {
	var pl Plan
	raw, err := os.ReadFile(s.planPath())
	if err != nil {
		if os.IsNotExist(err) {
			return pl, nil
		}
		return pl, fmt.Errorf("读取日程表失败: %w", err)
	}
	if err := json.Unmarshal(raw, &pl); err != nil {
		return pl, fmt.Errorf("日程表文件损坏: %w", err)
	}
	return pl, nil
}

// SavePlan 整张覆盖。
func (s *Store) SavePlan(pl Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.savePlan(pl)
}

func (s *Store) savePlan(pl Plan) error {
	if pl.Items == nil {
		pl.Items = []Item{}
	}
	return s.writeJSON(s.planPath(), pl)
}

// UpdatePlan 在锁内读-改-写：mutate 返回 false 表示没有改动、不落盘。
func (s *Store) UpdatePlan(mutate func(pl *Plan) (bool, error)) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl, err := s.loadPlan()
	if err != nil {
		return pl, err
	}
	changed, err := mutate(&pl)
	if err != nil {
		return pl, err
	}
	if !changed {
		return pl, nil
	}
	return pl, s.savePlan(pl)
}

// LoadCommitments 返回全部约定，按记录顺序。
func (s *Store) LoadCommitments() ([]Commitment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadCommitments()
	return f.Items, err
}

func (s *Store) loadCommitments() (commitmentsFile, error) {
	var f commitmentsFile
	raw, err := os.ReadFile(s.commitmentsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, fmt.Errorf("读取约定失败: %w", err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("约定文件损坏: %w", err)
	}
	return f, nil
}

// UpdateCommitments 在锁内读-改-写约定列表。mutate 可经 next 取一个新 id。
func (s *Store) UpdateCommitments(mutate func(cs *[]Commitment, next func() string) (bool, error)) ([]Commitment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadCommitments()
	if err != nil {
		return nil, err
	}
	if f.NextID <= 0 {
		f.NextID = 1
		for _, c := range f.Items { // 兼容手写的文件：从现存最大号往后取
			var n int
			if _, err := fmt.Sscanf(c.ID, "c%d", &n); err == nil && n >= f.NextID {
				f.NextID = n + 1
			}
		}
	}
	next := func() string {
		id := fmt.Sprintf("c%d", f.NextID)
		f.NextID++
		return id
	}
	changed, err := mutate(&f.Items, next)
	if err != nil {
		return nil, err
	}
	if !changed {
		return f.Items, nil
	}
	if f.Items == nil {
		f.Items = []Commitment{}
	}
	return f.Items, s.writeJSON(s.commitmentsPath(), f)
}

// writeJSON 原子写回（tmp + rename），权限 0600：日程属于对话内容的一部分。
func (s *Store) writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("保存日程失败: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("创建日程目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("保存日程失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存日程失败: %w", err)
	}
	return nil
}

// ---------- 日期与时刻 ----------

const dateLayout = "2006-01-02"

var weekdayNames = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

func weekdayCN(t time.Time) string { return weekdayNames[t.Weekday()] }

// logicalDay 返回 now 所属的逻辑日期（零点时刻）：day_start_hour 之前算前一天——
// 凌晨一点还在聊天不算新的一天。
func logicalDay(now time.Time, dayStartHour int) time.Time {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if now.Hour() < dayStartHour {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

// parseHHMM 解析 "HH:MM"，返回当天的分钟数。
func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	var hh, mm int
	if _, err := fmt.Sscanf(s, "%d:%d", &hh, &mm); err != nil || len(s) < 4 || len(s) > 5 {
		return 0, fmt.Errorf("时间 %q 格式应为 HH:MM，例如 14:30", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("时间 %q 不合法", s)
	}
	return hh*60 + mm, nil
}

// normHHMM 把时间规整成两位数的 "HH:MM"。
func normHHMM(s string) (string, error) {
	m, err := parseHHMM(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d:%02d", m/60, m%60), nil
}

// parseDate 解析 "YYYY-MM-DD" 为本地零点。
func parseDate(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation(dateLayout, strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("日期 %q 格式应为 YYYY-MM-DD", s)
	}
	return t, nil
}

// absTime 把一张表上的 HH:MM 换算成绝对时刻。一天从 day_start_hour 起算，在此之前
// 的时刻属于次日凌晨（表上写 01:00 的项是这一天将尽的凌晨一点，不是排表之前的那个
// 凌晨）；end 不晚于 start 时视为跨到次日。
func absTime(day time.Time, hhmm string, dayStartHour int) time.Time {
	m, err := parseHHMM(hhmm)
	if err != nil {
		return time.Time{}
	}
	t := day.Add(time.Duration(m) * time.Minute)
	if m < dayStartHour*60 {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// span 返回一项的起止绝对时刻。
func (it *Item) span(day time.Time, dayStartHour int) (start, end time.Time) {
	start = absTime(day, it.Start, dayStartHour)
	end = absTime(day, it.End, dayStartHour)
	if !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}
	return start, end
}

// ---------- 校验 ----------

func checkRunes(v, what string, max int) error {
	if n := len([]rune(v)); n > max {
		return fmt.Errorf("%s过长（%d 字，上限 %d 字），请压缩后重试", what, n, max)
	}
	return nil
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func validFlex(f string) bool {
	for _, l := range flexLevels {
		if l == f {
			return true
		}
	}
	return false
}

func validBusy(b string) bool {
	for _, l := range busyLevels {
		if l == b {
			return true
		}
	}
	return false
}

// sortCommitments 按日期与开始时刻排序（稳定）。
func sortCommitments(cs []Commitment) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Date != cs[j].Date {
			return cs[i].Date < cs[j].Date
		}
		return cs[i].Start < cs[j].Start
	})
}

// fmtDateCN 把 YYYY-MM-DD 写成「8 月 23 日（周日）」。
func fmtDateCN(date string, loc *time.Location) string {
	t, err := parseDate(date, loc)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%d 月 %d 日（%s）", int(t.Month()), t.Day(), weekdayCN(t))
}

// fmtDateShort 把 YYYY-MM-DD 写成「08-23（周日）」。
func fmtDateShort(date string, loc *time.Location) string {
	t, err := parseDate(date, loc)
	if err != nil {
		return date
	}
	return fmt.Sprintf("%s（%s）", t.Format("01-02"), weekdayCN(t))
}

// humanMinutes 把时长写成「N 分钟 / N 小时 / N 小时 N 分钟」。
func humanMinutes(d time.Duration) string {
	mins := int(d.Round(time.Minute) / time.Minute)
	if mins < 0 {
		mins = 0
	}
	if mins < 60 {
		return fmt.Sprintf("%d 分钟", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%d 小时", h)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", h, m)
}
