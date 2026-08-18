// Package weather 提供天气感知的系统插件：按设定的城市定时取现实天气，把此刻的
// 天气每轮注入上下文，作为角色所处环境的一部分。
//
// 它注入的是环境状态而不是一个可调用的工具——这是它依赖 roleplay 的原因。通用对话
// 里每轮塞一行天气只是噪声，真要问天气，模型自己抓网页就够了。
//
// **天气首先得是角色自己的**。只给「对方所在地的天气」时，那是一条关于第三方的信息：
// 角色对它既没有体感也没有可做的反应，于是它在对话里几乎不起作用。所以这里分成角色
// 与用户两处地点，同城时合成一处——角色自己那边的天气才会渗进环境描写与状态。
//
// 天气在后台按固定间隔刷新，注入路径只读内存里的缓存：TurnPrompt 在每轮对话的同步
// 路径上，在那里发网络请求就是让每一轮都替一次超时买单。
package weather

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"wen/internal/plugin"
)

const (
	defaultRefreshMinutes = 30
	defaultStaleMinutes   = 60
	defaultSameCity       = true
	// minRefreshMinutes 是刷新间隔的下限。数据源是免费的公共接口，取得再勤也没有
	// 更新的数据，只是白白打对方。
	minRefreshMinutes = 10

	// requestTimeout 是单次取数（含地址解析）的总时限。
	requestTimeout = 20 * time.Second
)

// observation 是一处地点的观测缓存。
type observation struct {
	place   Place
	placeOK bool
	cur     Report
	curOK   bool
	lastErr string
}

// Plugin 是 weather 系统插件。
type Plugin struct {
	mu         sync.RWMutex
	personaLoc string // 角色所在的城市
	userLoc    string // 用户所在的城市；同城时归一化为空
	sameCity   bool
	refresh    time.Duration
	stale      time.Duration
	client     *http.Client

	// 后台刷新循环。Init 可重入，每次先停旧的再起新的。
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 观测缓存，按城市名原文分条。单独一把锁：它由后台循环写、由注入路径读，
	// 与配置的生命周期无关。两处地点填了同一个城市时天然共用一条，不会重复取。
	dataMu sync.RWMutex
	obs    map[string]*observation

	// 设置页操作的状态。StartAction 与 ActionState 会被 HTTP 并发调用，自带一把锁。
	actMu    sync.Mutex
	actSeq   uint64
	actState plugin.ActionState
}

func New() *Plugin {
	return &Plugin{
		sameCity: defaultSameCity,
		refresh:  defaultRefreshMinutes * time.Minute,
		stale:    defaultStaleMinutes * time.Minute,
	}
}

func (p *Plugin) Name() string { return "weather" }

func (p *Plugin) Category() string { return plugin.CategoryPersona }

func (p *Plugin) Description() string {
	return "天气感知：按设定的城市定时获取现实天气，作为角色所处环境的一部分每轮注入"
}

// Requires 硬依赖 roleplay：没有角色，一行天气就只是上下文里的噪声。
func (p *Plugin) Requires() []string { return []string{"roleplay"} }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "persona_location", Label: "角色所在的城市", Type: plugin.FieldString,
			Description: "角色身处的地方，如「杭州」「上海」「Hangzhou」。这是主要的那一个——" +
				"角色自己那边的天气才会进入它的环境与状态。留空则不注入角色这边的天气。",
			Default: "",
		},
		{
			Key: "same_city", Label: "与我在同一个城市", Type: plugin.FieldBool,
			Description: "两人同城。开启时只按上面那个城市查一次，下面的「我所在的城市」不生效。",
			Default:     defaultSameCity,
		},
		{
			// 标签用「我」：填的人就是你自己。注入给模型的那一段写「对方所在的」——
			// 提示词是模型以第一人称读的，那里的「我」指它自己。与 roleplay 的
			// 「我的信息」是同一处人称约定。
			Key: "user_location", Label: "我所在的城市", Type: plugin.FieldString,
			Description: "两人不在一处时你所在的地方，作为角色知道的一件事（用于关心的方向）。" +
				"仅在上一项关闭时生效；留空则不注入你这边的天气。",
			Default: "",
		},
		{
			Key: "refresh_minutes", Label: "刷新间隔（分钟）", Type: plugin.FieldInt,
			Description: "后台每隔多久取一次天气。取得再勤也没有更新的数据，只是白打接口。",
			Default:     defaultRefreshMinutes,
			Min:         plugin.IntPtr(minRefreshMinutes),
			Max:         plugin.IntPtr(1440),
		},
		{
			Key: "stale_minutes", Label: "过期不注入的时限（分钟）", Type: plugin.FieldInt,
			Description: "距上次成功取得超过这个时间就不再注入天气——取不到新的时，" +
				"宁可让角色不知道天气，也不要让它把几个小时前的天气当作此刻。" +
				"不得小于刷新间隔。",
			Default: defaultStaleMinutes,
			Min:     plugin.IntPtr(minRefreshMinutes),
			Max:     plugin.IntPtr(10080),
		},
	}
}

// Init 应用配置并（重）启动后台刷新循环。可重入：先停掉上一轮循环。
//
// 这里不发网络请求：启动时断网不该让插件启用失败，取不到就是没有天气可注入而已。
func (p *Plugin) Init(_ plugin.InitContext, cfg map[string]any) error {
	personaLoc, userLoc, sameCity := normalizeLocations(cfg)

	refreshMin := plugin.CfgInt(cfg, "refresh_minutes", defaultRefreshMinutes)
	staleMin := plugin.CfgInt(cfg, "stale_minutes", defaultStaleMinutes)
	if staleMin < refreshMin {
		return fmt.Errorf("「过期不注入的时限」(%d 分钟) 不能小于「刷新间隔」(%d 分钟)："+
			"那样刚取到的天气也会立刻被判为过期，等于永远不注入", staleMin, refreshMin)
	}

	p.Stop() // 重入：先停上一轮循环，避免两套定时器并行

	// 只丢掉不再需要的城市的缓存。改个刷新间隔就把手上这份好数据扔掉，
	// 万一此刻正好取不到，就白白空窗一段。
	// 这一段刻意不与下面的 p.mu 嵌套：两把锁全程各用各的，就不存在取锁顺序问题。
	wanted := wantedLocations(personaLoc, userLoc)
	p.dataMu.Lock()
	if p.obs == nil {
		p.obs = map[string]*observation{}
	}
	for loc := range p.obs {
		if !slices.Contains(wanted, loc) {
			delete(p.obs, loc)
		}
	}
	p.dataMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.personaLoc, p.userLoc, p.sameCity = personaLoc, userLoc, sameCity
	p.refresh = time.Duration(refreshMin) * time.Minute
	p.stale = time.Duration(staleMin) * time.Minute
	p.client = &http.Client{Timeout: requestTimeout}
	if len(wanted) == 0 {
		return nil // 一个城市都没填就不起循环，也不注入任何东西
	}

	// 循环起在锁内，与 cancel 的记录是同一步：分成两步的话，中间来一次 Stop
	// 会看到 cancel 还是 nil，于是这个刚起来的循环就没人停得掉了。
	var ctx context.Context
	ctx, p.cancel = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.loop(ctx, p.client, wanted, p.refresh)
	return nil
}

// normalizeLocations 把配置里的两处地点规整成「角色所在 / 我所在 / 是否同城」。
func normalizeLocations(cfg map[string]any) (personaLoc, userLoc string, sameCity bool) {
	personaLoc = strings.TrimSpace(plugin.CfgString(cfg, "persona_location", ""))
	sameCity = plugin.CfgBool(cfg, "same_city", defaultSameCity)
	if !sameCity {
		userLoc = strings.TrimSpace(plugin.CfgString(cfg, "user_location", ""))
	}
	// 两处填了同一个地方就当同城处理：否则会注入两行一模一样的天气，
	// 读起来像两个地方碰巧天气相同，而事实是它们本来就是一个地方。
	if userLoc != "" && userLoc == personaLoc {
		sameCity, userLoc = true, ""
	}
	return personaLoc, userLoc, sameCity
}

// wantedLocations 返回需要取数的城市（去重、去空）。
func wantedLocations(locs ...string) []string {
	var out []string
	for _, loc := range locs {
		if loc != "" && !slices.Contains(out, loc) {
			out = append(out, loc)
		}
	}
	return out
}

// Stop 停止刷新循环。只做取消与有界等待，符合 Stoppable 契约。
func (p *Plugin) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.wg.Wait() // 循环里的阻塞点都挂在 ctx 上，取消后即刻返回
	}
}

func (p *Plugin) Tools() []plugin.Tool { return nil }

// settings 是一次调用期间使用的配置快照。
type settings struct {
	personaLoc string
	userLoc    string
	sameCity   bool
	refresh    time.Duration
	stale      time.Duration
	client     *http.Client
}

// configured 表示至少有一处地点可查。
func (s settings) configured() bool { return s.personaLoc != "" || s.userLoc != "" }

// snapshot 取一份配置快照：SetConfig 会在运行时重新 Init，而注入可能正在进行。
func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{
		personaLoc: p.personaLoc, userLoc: p.userLoc, sameCity: p.sameCity,
		refresh: p.refresh, stale: p.stale, client: p.client,
	}
}

// SystemPrompt 注入天气的使用规则，是静态内容。一个城市都没填时整段不注入：
// 那时候永远不会有 [当前天气]，一段解释怎么用它的规则只是白占上下文。
func (p *Plugin) SystemPrompt() string {
	if !p.snapshot().configured() {
		return ""
	}
	return guidePrompt
}

// TurnPrompt 注入此刻的天气。取不到或已过期时不注入：提示词里已经写了
// 「没有给出的一方就当作不知道」，让它不知道，好过让它把旧天气当成现在的。
//
// 一边取到、一边取不到时注入取到的那一边——半边可用不该让整块消失。
func (p *Plugin) TurnPrompt(_ context.Context, _ plugin.TurnEvent) (string, error) {
	s := p.snapshot()
	if !s.configured() {
		return "", nil
	}
	now := time.Now()

	var lines []string
	if s.sameCity {
		if r, ok := p.fresh(s.personaLoc, s.stale, now); ok {
			lines = append(lines, fmt.Sprintf("%s（你与对方同在）：%s", r.Place, renderConditions(r)))
		}
	} else {
		if r, ok := p.fresh(s.personaLoc, s.stale, now); ok {
			lines = append(lines, "你所在的"+r.Place+"："+renderConditions(r))
		}
		if r, ok := p.fresh(s.userLoc, s.stale, now); ok {
			lines = append(lines, "对方所在的"+r.Place+"："+renderConditions(r))
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return stateHeader + "\n" + strings.Join(lines, "\n"), nil
}

// ---------- 后台刷新 ----------

func (p *Plugin) loop(ctx context.Context, client *http.Client, locs []string, refresh time.Duration) {
	defer p.wg.Done()

	p.refreshAll(ctx, client, locs)

	t := time.NewTicker(refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.refreshAll(ctx, client, locs)
		}
	}
}

// refreshAll 逐个刷新。串行即可：最多两处，且失败的一处不该拖住另一处。
func (p *Plugin) refreshAll(ctx context.Context, client *http.Client, locs []string) {
	for _, loc := range locs {
		if ctx.Err() != nil {
			return
		}
		p.refreshOne(ctx, client, loc)
	}
}

// refreshOne 取一处地点的天气。失败时保留上一份观测，由过期时限决定它还能不能用。
func (p *Plugin) refreshOne(ctx context.Context, client *http.Client, loc string) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	rep, err := p.observe(ctx, client, loc)
	if err != nil {
		if ctx.Err() == nil {
			p.noteError(loc, err)
		}
		return
	}
	p.dataMu.Lock()
	o := p.entryLocked(loc)
	o.cur, o.curOK, o.lastErr = rep, true, ""
	p.dataMu.Unlock()
}

// observe 取一次观测，地名解析的结果按城市缓存——它几乎不变，不必每次重解析。
func (p *Plugin) observe(ctx context.Context, client *http.Client, loc string) (Report, error) {
	p.dataMu.RLock()
	var (
		place Place
		ok    bool
	)
	if o := p.obs[loc]; o != nil {
		place, ok = o.place, o.placeOK
	}
	p.dataMu.RUnlock()

	if !ok {
		resolved, err := geocode(ctx, client, loc)
		if err != nil {
			return Report{}, err
		}
		p.dataMu.Lock()
		o := p.entryLocked(loc)
		o.place, o.placeOK = resolved, true
		p.dataMu.Unlock()
		place = resolved
	}
	return fetchCurrent(ctx, client, place)
}

// entryLocked 取（或建）一处地点的缓存条目。调用方须持 dataMu 写锁。
func (p *Plugin) entryLocked(loc string) *observation {
	if p.obs == nil {
		p.obs = map[string]*observation{}
	}
	o := p.obs[loc]
	if o == nil {
		o = &observation{}
		p.obs[loc] = o
	}
	return o
}

// noteError 记下失败原因供状态行展示。同一处地点的同一个原因只写一次日志：
// 断网期间每半小时刷屏一条同样的错误没有意义。
func (p *Plugin) noteError(loc string, err error) {
	msg := err.Error()
	p.dataMu.Lock()
	o := p.entryLocked(loc)
	repeated := o.lastErr == msg
	o.lastErr = msg
	p.dataMu.Unlock()
	if !repeated {
		log.Printf("weather: %s: %s", loc, msg)
	}
}

// fresh 返回某处地点仍在有效期内的观测。
func (p *Plugin) fresh(loc string, stale time.Duration, now time.Time) (Report, bool) {
	if loc == "" {
		return Report{}, false
	}
	p.dataMu.RLock()
	defer p.dataMu.RUnlock()
	o := p.obs[loc]
	if o == nil || !o.curOK {
		return Report{}, false
	}
	if stale > 0 && now.Sub(o.cur.Fetched) > stale {
		return Report{}, false
	}
	return o.cur, true
}

// lastReport 返回某处地点最近一次成功的观测（不判过期）与最近一次失败的原因。
func (p *Plugin) lastReport(loc string) (Report, bool, string) {
	p.dataMu.RLock()
	defer p.dataMu.RUnlock()
	o := p.obs[loc]
	if o == nil {
		return Report{}, false, ""
	}
	return o.cur, o.curOK, o.lastErr
}

// ---------- 注入渲染 ----------

// renderConditions 渲染天气本身（不含地名）。地名与「谁所在」由调用方拼，
// 因为同城与分处两地的措辞不一样，而措辞正是这条注入起不起作用的关键。
//
// 不注入「几分钟前采集」：当前时间由核心的本轮状态块每轮给出，再加一个逐轮变化的
// 采集间隔只是噪声；数据够不够新已经由过期时限判过了。
func renderConditions(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s，%.0f℃", r.Condition, r.TempC)
	if diff := r.FeelsC - r.TempC; diff >= 1 || diff <= -1 {
		fmt.Fprintf(&b, "（体感 %.0f℃）", r.FeelsC)
	}
	if r.Humidity > 0 {
		fmt.Fprintf(&b, "，湿度 %d%%", r.Humidity)
	}
	fmt.Fprintf(&b, "，风速 %.0f km/h。", r.WindKmh)
	return b.String()
}
