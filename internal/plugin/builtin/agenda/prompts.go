package agenda

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wen/internal/plugin"
)

// guidePrompt 是静态判据。表与约定的本体走 TurnPrompt（取决于可见域、要读盘）。
const guidePrompt = `[日程]
你在对话之外有自己的一天。本轮状态里的 [今日安排] 是今天的表，[未来约定] 是已经定下
的事；你此刻在哪、在做什么以它们为准，不另编行程，表尚未排定时也不要宣布今天的安排。
- 定在某天某时的事（和对方约好的、答应别人的、自己要去办的）用 add_commitment 登记，
  到那天会排进表里。对方要你到点提醒他的事不在此列，那归定时任务。
- 表里的项开始了、做完了、挪了时间、不去了，用 update_day_plan 更新；跳过、取消、
  延期都要写原因，做完写一句经历。
- 与对方的约定不能由你单方面改动，要改先跟对方商量，对方明确同意后才传 agreed_by_user。
- 表里「和谁」只能是人物清单里的人；要约清单外的人，先用 upsert_person 登记他。
- 注入的约定不全时用 list_commitments 查看。`

// stateHeader / commitHeader 领起两块注入。
const (
	stateHeader  = "[今日安排]"
	commitHeader = "[未来约定]"
)

// planPrompt 是规划轮次的一次性输入。花括号由代码填：星期、日期、时刻、今天的约定。
// 昨天的事不由这里传入：规划是一轮完整对话，[最近经历]、天气、心情、人物清单都随
// 各自的 TurnPrompt 自然到场，本插件不读任何别家的文件。
const planPrompt = `【规划今天】现在是 %s %s %s，你刚醒来，给自己排一张今天的表。
昨天与前几天的事见 [最近经历]，今天别排成一样的。
今天已经定下的约定（必须排进表里，与对方的约定时间不能改）：
%s
要求：
- 只排 2-4 件事，留大段空白。一天不必塞满，空着的时间就是你自己的。
- 每项写清开始结束时间、和谁、在哪，并标出能不能挪：可挪 / 尽量守 / 不能动。
  与对方的约定一律不能动。
- 「和谁」只能是人物清单里的人；想约清单外的人，先用 upsert_person 登记他。
- 别与昨天一样。看今天是星期几、天气如何、心情与身体状态怎样、答应过谁什么，
  按角色的处境与性格安排——上班的人有上班的一天，周末有周末的样子。
- 每项标出做这件事时能不能回消息（轻忙 / 重忙 / 不回）。
排好后用 set_day_plan 提交。这一轮不必对对方说话，输出空回复即可。`

// startPrompt 是活动开始轮次的一次性输入。
const startPrompt = `【日程·开始】现在 %s，到了「%s」（%s-%s，%s）的时间。%s
看看此刻的天气、心情、身体状态，以及刚才的对话，决定怎么办：
- 照做：用 update_day_plan 把它标为 ongoing（做法有变可一并改标题与 busy），
  用现场工具更新你的所在与在做的事，然后出门。
- 改方式、挪时间：可挪与尽量守的项你自己定，改了要有缘由。
- 延期或不去：status 填 deferred / skipped，原因写进 outcome。
- 不能动的项照做；与对方的约定无论如何不要单方面改，要改先跟对方说。
这一轮不必对对方说话，除非有事要告诉他（比如要改和他的约定）；否则输出空回复。`

// endPrompt 是活动结束轮次的一次性输入；第一句在补记时换成 catchUpLead。
const endPrompt = `【日程·结束】%s
写下这段时间的经历：做了什么、有什么小事发生、感觉如何，一两句即可，用 update_day_plan
标为 done 并把经历写进 outcome。顺手把该更新的更新掉：和谁见了面就用 upsert_person
记一笔；现场回到哪、在做什么；心情与身体若有变化；买了或用掉了什么。
值得长期记住的事按 [长期记忆] 的判据保存，只是经历本身不必存。
这一轮不必对对方说话，输出空回复即可——回来后想说的，之后自然会说。`

// buildPlanPrompt 填好规划输入。
func buildPlanPrompt(now time.Time, today []Commitment) string {
	lines := "（无）"
	if len(today) > 0 {
		var b strings.Builder
		for _, c := range today {
			fmt.Fprintf(&b, "- [%s] %s%s %s（%s）\n", c.ID, c.Start, dashEnd(c.End), c.Title, c.Flex)
		}
		lines = strings.TrimRight(b.String(), "\n")
	}
	return fmt.Sprintf(planPrompt, weekdayCN(now), now.Format("01-02"), now.Format("15:04"), lines)
}

// buildStartPrompt 填好开始输入。late 超过宽限时注明已经晚了多久。
func buildStartPrompt(now time.Time, it Item, late time.Duration, grace time.Duration) string {
	note := ""
	if late > grace {
		note = fmt.Sprintf("\n已经晚了 %s，先决定还去不去。", humanMinutes(late))
	}
	return fmt.Sprintf(startPrompt, now.Format("15:04"), it.Title, it.Start, it.End, it.Flex, note)
}

// buildEndPrompt 填好结束输入。catchUp 表示这项在离线期间已经过去，只补记一句经历。
func buildEndPrompt(now time.Time, it Item, catchUp bool) string {
	lead := fmt.Sprintf("现在 %s，「%s」（%s-%s）该结束了。", now.Format("15:04"), it.Title, it.Start, it.End)
	if catchUp {
		lead = fmt.Sprintf("现在 %s，「%s」（%s-%s）在你离线期间已经过去，补记一句经历即可。",
			now.Format("15:04"), it.Title, it.Start, it.End)
	}
	return fmt.Sprintf(endPrompt, lead)
}

func dashEnd(end string) string {
	if end == "" {
		return ""
	}
	return "-" + end
}

// ---------- TurnPrompt ----------

// TurnPrompt 注入本轮写入域的今日安排与未来约定。表尚未排定时也注入一句「尚未排定」：
// 模型得知道这件事，才不会凭空宣布今天的行程。
func (p *Plugin) TurnPrompt(ctx context.Context, _ plugin.TurnEvent) (string, error) {
	s := p.snapshot()
	st := p.writeStore(ctx)
	if st == nil {
		return "", nil
	}
	pl, err := st.LoadPlan()
	if err != nil {
		return "", err
	}
	cs, err := st.LoadCommitments()
	if err != nil {
		return "", err
	}
	return renderPrompt(pl, cs, p.now(), s), nil
}

// renderPrompt 按预算渲染两块注入，分级降级：去掉已做项的经历 → 已做项压成条数 →
// 约定只剩条数。进行中与下一项永远保留——它们是行为判据。
func renderPrompt(pl Plan, cs []Commitment, now time.Time, s settings) string {
	day := s.today(now)
	today := day.Format(dateLayout)
	var todayCs, future []Commitment
	for _, c := range cs {
		switch {
		case c.Date == today && !c.Planned:
			todayCs = append(todayCs, c)
		case c.Date > today:
			future = append(future, c)
		}
	}
	sortCommitments(todayCs)
	sortCommitments(future)
	planned := pl.Date == today

	for level := 0; level <= 3; level++ {
		var b strings.Builder
		b.WriteString(stateHeader + "（" + weekdayCN(day) + "）\n")
		if planned {
			b.WriteString(renderPlan(pl, day, now, s.dayStartHour, level))
		} else {
			b.WriteString("今天的安排尚未排定。")
			if len(todayCs) > 0 {
				b.WriteString("\n今天已定的约定：" + joinCommitLines(todayCs, false) + "。")
			}
		}
		if len(future) > 0 {
			b.WriteString("\n" + commitHeader + "\n")
			if level >= 3 {
				fmt.Fprintf(&b, "共 %d 条，可用 list_commitments 查看。", len(future))
			} else {
				shown := future
				if len(shown) > s.maxCommitInject {
					shown = shown[:s.maxCommitInject]
				}
				for _, c := range shown {
					fmt.Fprintf(&b, "%s %s %s（%s）\n", fmtDateShort(c.Date, now.Location()), c.Start, c.Title, c.Flex)
				}
				if rest := len(future) - len(shown); rest > 0 {
					fmt.Fprintf(&b, "另有 %d 条，可用 list_commitments 查看。", rest)
				}
			}
		}
		out := strings.TrimRight(b.String(), "\n")
		if s.maxInjectBytes <= 0 || len(out) <= s.maxInjectBytes || level == 3 {
			return out
		}
	}
	return "" // 不可达
}

// renderPlan 写今天的表：此刻 / 之后 / 今天已做 / 今天没去。level 是降级档位。
func renderPlan(pl Plan, day, now time.Time, dayStartHour int, level int) string {
	var (
		cur, pending *Item
		upcoming     []*Item
		done, missed []*Item
	)
	active := 0
	for i := range pl.Items {
		it := &pl.Items[i]
		if it.Status == statusCancelled {
			missed = append(missed, it)
			continue
		}
		active++
		start, end := it.span(day, dayStartHour)
		switch it.Status {
		case statusOngoing:
			cur = it
		case statusPlanned:
			switch {
			case start.After(now):
				upcoming = append(upcoming, it)
			case end.After(now):
				pending = it
			default:
				missed = append(missed, it)
			}
		case statusDone:
			done = append(done, it)
		default: // skipped / deferred
			missed = append(missed, it)
		}
	}
	if active == 0 {
		return "今天没有安排，整天空着。"
	}

	var b strings.Builder
	switch {
	case cur != nil:
		start, end := cur.span(day, dayStartHour)
		if end.After(now) {
			fmt.Fprintf(&b, "此刻：%s（%s-%s，%s），已进行 %s。", cur.Title, cur.Start, cur.End, cur.Flex, humanMinutes(now.Sub(start)))
		} else {
			fmt.Fprintf(&b, "此刻：%s（%s-%s）已过了结束时间 %s，还没收尾。", cur.Title, cur.Start, cur.End, humanMinutes(now.Sub(end)))
		}
	case pending != nil:
		fmt.Fprintf(&b, "此刻：到了「%s」的时间（%s-%s，%s），还没动身。", pending.Title, pending.Start, pending.End, pending.Flex)
	case len(upcoming) > 0:
		next := upcoming[0]
		start, _ := next.span(day, dayStartHour)
		fmt.Fprintf(&b, "此刻：空闲。下一项 %s %s（%s），还有 %s。", next.Start, next.Title, next.Flex, humanMinutes(start.Sub(now)))
		upcoming = upcoming[1:]
	default:
		b.WriteString("此刻：空闲，今天剩下的时间都空着。")
	}
	if len(upcoming) > 0 {
		parts := make([]string, 0, len(upcoming))
		for _, it := range upcoming {
			parts = append(parts, fmt.Sprintf("%s %s（%s）", it.Start, it.Title, it.Flex))
		}
		b.WriteString("\n之后：" + strings.Join(parts, "；") + "。")
	}
	if len(done) > 0 {
		switch {
		case level >= 2:
			fmt.Fprintf(&b, "\n今天已做 %d 项。", len(done))
		default:
			b.WriteString("\n今天已做：" + joinItems(done, level == 0) + "。")
		}
	}
	if len(missed) > 0 {
		switch {
		case level >= 2:
			fmt.Fprintf(&b, "\n今天没去 %d 项。", len(missed))
		default:
			b.WriteString("\n今天没去：" + joinItems(missed, level == 0) + "。")
		}
	}
	return b.String()
}

// joinItems 把若干项写成「标题——经历」，withOutcome 为假时只留标题。
func joinItems(items []*Item, withOutcome bool) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		s := it.Title
		if withOutcome {
			switch {
			case it.Outcome != "" && it.Status == statusDeferred:
				s += "——已延期：" + it.Outcome
			case it.Outcome != "":
				s += "——" + it.Outcome
			case it.Status == statusPlanned:
				s += "——到点没去，没有记录"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "；")
}

// joinCommitLines 把几条约定写成「18:30 和对方吃晚饭（不能动）」，用分号连接。
func joinCommitLines(cs []Commitment, withID bool) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		s := fmt.Sprintf("%s %s（%s）", c.Start, c.Title, c.Flex)
		if withID {
			s = "[" + c.ID + "] " + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "；")
}
