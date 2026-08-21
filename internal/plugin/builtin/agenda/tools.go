package agenda

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"wen/internal/cue"
	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("日程尚未就绪")

// userRule 是「与对方的约定不能单方面改」的拒绝措辞。拒绝的同时把规则与出口都告诉模型，
// 否则它只会换个说法再试一次。
const userRule = "「%s」是和对方的约定，改动要先跟对方商量；对方在本轮对话里明确同意后，再传 agreed_by_user: true 重试"

// checkWith 校验「和谁」：去重、限量，每个名字都得是人物库里的人，并换成库里的规范写法。
func (p *Plugin) checkWith(ctx context.Context, with []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, w := range with {
		w = squash(w)
		if w == "" {
			continue
		}
		if p.lookup != nil {
			canonical, ok := p.lookup.Known(ctx, w)
			if !ok {
				known := p.lookup.Names(ctx)
				if len(known) == 0 {
					return nil, fmt.Errorf("没有叫「%s」的人物，人物清单还是空的。先用 upsert_person 登记他", w)
				}
				return nil, fmt.Errorf("没有叫「%s」的人物，已登记的有：%s。先用 upsert_person 登记他", w, strings.Join(known, "、"))
			}
			w = canonical
		}
		if seen[strings.ToLower(w)] {
			continue
		}
		seen[strings.ToLower(w)] = true
		out = append(out, w)
	}
	if len(out) > maxWith {
		return nil, fmt.Errorf("一项最多写 %d 个同行的人", maxWith)
	}
	return out, nil
}

// ---------- set_day_plan ----------

type setPlanTool struct{ p *Plugin }

func (t *setPlanTool) Name() string { return "set_day_plan" }

func (t *setPlanTool) Description() string {
	return "为今天排一张日程表：2-4 件事，中间留大段空白，每项给开始结束时间、和谁、能不能挪。" +
		"今天的未来约定必须全部排进去（填 from_commitment）。整表覆盖，只在【规划今天】的轮次里使用，" +
		"平时改动用 update_day_plan，临时加一件今天的事用 add_commitment。"
}

func (t *setPlanTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {"type": "array", "items": {"type": "object", "properties": {
				"title": {"type": "string", "description": "做什么，短句（30 字内）"},
				"start": {"type": "string", "description": "开始时间 HH:MM"},
				"end": {"type": "string", "description": "结束时间 HH:MM，早于开始时间表示跨到次日"},
				"place": {"type": "string", "description": "在哪（30 字内，可省）"},
				"with": {"type": "array", "items": {"type": "string"}, "description": "同行的人，必须是人物清单里已有的名字"},
				"with_user": {"type": "boolean", "description": "是否与对方一起；为真时这项一律不能动"},
				"flex": {"type": "string", "enum": ["可挪", "尽量守", "不能动"], "description": "能不能挪"},
				"busy": {"type": "string", "enum": ["轻忙", "重忙", "不回"], "description": "做这件事时能不能回消息：轻忙可随时回，重忙只能简短回，不回是完全顾不上。不填为轻忙"},
				"from_commitment": {"type": "string", "description": "若这项来自某条今天的约定，填它的 id"}
			}, "required": ["title", "start", "end", "flex"]}},
			"note": {"type": "string", "description": "这一天的基调，一句话（可省，不注入）"}
		},
		"required": ["items"]
	}`)
}

type planItemArg struct {
	Title          string   `json:"title"`
	Start          string   `json:"start"`
	End            string   `json:"end"`
	Place          string   `json:"place"`
	With           []string `json:"with"`
	WithUser       bool     `json:"with_user"`
	Flex           string   `json:"flex"`
	Busy           string   `json:"busy"`
	FromCommitment string   `json:"from_commitment"`
}

// buildItem 把一条参数校验并规整成 Item（不含 id 与状态）。
func (p *Plugin) buildItem(ctx context.Context, a planItemArg) (Item, error) {
	it := Item{Title: squash(a.Title), Place: squash(a.Place), WithUser: a.WithUser,
		Flex: strings.TrimSpace(a.Flex), Busy: strings.TrimSpace(a.Busy), FromCommitment: strings.TrimSpace(a.FromCommitment)}
	if it.Title == "" {
		// 提一句最常见的成因：模型偶尔会把一件事拆成两个对象（前一半带 title 与时间，
		// 后一半只剩 place / flex / busy），报「title 不能为空」的话它多半去改别的地方
		return it, fmt.Errorf("有一项没有 title：每件事是一个完整的对象，" +
			"title、start、end、flex 要写在同一个对象里，不要拆成两段")
	}
	if err := checkRunes(it.Title, "title", maxTitleRunes); err != nil {
		return it, err
	}
	if err := checkRunes(it.Place, "place", maxPlaceRunes); err != nil {
		return it, err
	}
	var err error
	if it.Start, err = normHHMM(a.Start); err != nil {
		return it, fmt.Errorf("「%s」的开始%w", it.Title, err)
	}
	if it.End, err = normHHMM(a.End); err != nil {
		return it, fmt.Errorf("「%s」的结束%w", it.Title, err)
	}
	if it.With, err = p.checkWith(ctx, a.With); err != nil {
		return it, err
	}
	if it.WithUser {
		it.Flex = flexFixed // 与对方的约定一律不能动
	}
	// 「缺了」与「填错了」分开说：都报「只能是……」的话，缺字段的那次会被读成
	// 「填的值不对」，模型于是去改一个本来就没写的字段，白绕两圈
	if !validFlex(it.Flex) {
		return it, fmt.Errorf("「%s」%s：%s", it.Title, missingOrBad(it.Flex, "flex"), strings.Join(flexLevels, " / "))
	}
	if it.Busy == "" {
		it.Busy = defaultBusy
	}
	if !validBusy(it.Busy) {
		return it, fmt.Errorf("「%s」%s：%s", it.Title, missingOrBad(it.Busy, "busy"), strings.Join(busyLevels, " / "))
	}
	return it, nil
}

// missingOrBad 按字段是空还是填错，给出前半句。
func missingOrBad(value, field string) string {
	if strings.TrimSpace(value) == "" {
		return "缺少 " + field + "，只能是"
	}
	return "的 " + field + " 只能是"
}

// overlapNotes 找出时间重叠的项。有重叠不拒绝，只告知——到时候模型自己取舍。
func overlapNotes(items []Item, day time.Time, dayStartHour int) []string {
	var notes []string
	for i := 0; i < len(items); i++ {
		si, ei := items[i].span(day, dayStartHour)
		for j := i + 1; j < len(items); j++ {
			sj, ej := items[j].span(day, dayStartHour)
			if si.Before(ej) && sj.Before(ei) {
				notes = append(notes, fmt.Sprintf("%s-%s 与 %s-%s 重叠，留着也行，到时你自己取舍",
					items[i].Start, items[i].End, items[j].Start, items[j].End))
			}
		}
	}
	return notes
}

func (t *setPlanTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Items []planItemArg `json:"items"`
		Note  string        `json:"note"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	p := t.p
	s := p.snapshot()
	st := p.writeStore(ctx)
	if st == nil {
		return "", errNotReady
	}
	// 规划轮次里已经提交过就不再受理：那句「排好后用 set_day_plan 提交」是一次性输入，
	// 却在工具循环的每次迭代里被重新读到，于是模型一版一版地微调着重提交
	//（实测连提十三次，见 planSubmitted 的说明）。
	tag := plugin.ScopeFrom(ctx).Write
	p.mu.RLock()
	resubmit := p.planning[tag] && p.planSubmitted[tag]
	p.mu.RUnlock()
	if resubmit {
		return "", fmt.Errorf("今天的表刚刚已经排好了（见上一条结果），本轮不要再提交。" +
			"要改其中某一项用 update_day_plan；没有要改的就直接结束这一轮")
	}
	if len(a.Items) > s.maxItems {
		return "", fmt.Errorf("一天最多排 %d 项（现在 %d 项），精简后重试；提示词要求的是 2-4 件事，留大段空白", s.maxItems, len(a.Items))
	}
	now := p.now()
	day := s.today(now)
	today := day.Format(dateLayout)

	cs, err := st.LoadCommitments()
	if err != nil {
		return "", err
	}
	todayCs := map[string]Commitment{}
	for _, c := range cs {
		if c.Date == today {
			todayCs[c.ID] = c
		}
	}

	// 先整体扫一眼有没有缺 title 的对象。逐项校验会先在前一项上报出「缺少 flex」，
	// 而那正是拆项的后遗症——真正的毛病在下一个对象上，先说这一条模型才改得对。
	for _, arg := range a.Items {
		if strings.TrimSpace(arg.Title) == "" {
			return "", fmt.Errorf("有一项没有 title：每件事是一个完整的对象，" +
				"title、start、end、flex 要写在同一个对象里，不要拆成两段")
		}
	}

	items := make([]Item, 0, len(a.Items))
	covered := map[string]bool{}
	for _, arg := range a.Items {
		it, err := p.buildItem(ctx, arg)
		if err != nil {
			return "", err
		}
		if it.FromCommitment != "" {
			c, ok := todayCs[it.FromCommitment]
			if !ok {
				return "", fmt.Errorf("今天没有 id 为 %s 的约定，可用 list_commitments 查看", it.FromCommitment)
			}
			if covered[c.ID] {
				return "", fmt.Errorf("约定 %s 被排了两次", c.ID)
			}
			covered[c.ID] = true
			if c.WithUser {
				it.WithUser, it.Flex = true, flexFixed
				if it.Start != c.Start || (c.End != "" && it.End != c.End) {
					return "", fmt.Errorf("约定 %s 是和对方的，时间 %s%s 不能改；要改先跟对方商量", c.ID, c.Start, dashEnd(c.End))
				}
			}
			if len(it.With) == 0 {
				it.With = c.With
			}
			if it.Place == "" {
				it.Place = c.Place
			}
		}
		items = append(items, it)
	}
	var missing []string
	for _, c := range cs {
		if c.Date == today && !covered[c.ID] {
			missing = append(missing, fmt.Sprintf("[%s] %s%s %s", c.ID, c.Start, dashEnd(c.End), c.Title))
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("今天有约定 %s 还没排进表，补上它（填 from_commitment）或先用 cancel_commitment 取消",
			strings.Join(missing, "、"))
	}

	pl, err := st.UpdatePlan(func(pl *Plan) (bool, error) {
		replan := pl.Date == today
		old := pl.Items
		for i := range items {
			items[i].ID = fmt.Sprintf("a%d", i+1)
			items[i].Status = statusPlanned
			if replan {
				// 重排只清状态，不清派发记录：同一件事到点已经派过开始轮次，不该再派一次
				for _, o := range old {
					if (o.FromCommitment != "" && o.FromCommitment == items[i].FromCommitment) ||
						(o.Title == items[i].Title && o.Start == items[i].Start) {
						items[i].StartFired, items[i].EndFired, items[i].SoonFired = o.StartFired, o.EndFired, o.SoonFired
						break
					}
				}
			}
		}
		pl.Date, pl.Weekday, pl.PlannedAt = today, weekdayCN(day), now
		pl.Note = squash(a.Note)
		pl.Items = items
		return true, nil
	})
	if err != nil {
		return "", err
	}
	clearAvailability() // 重排后没有「进行中」的项
	if len(covered) > 0 {
		_, _ = st.UpdateCommitments(func(list *[]Commitment, _ func() string) (bool, error) {
			for i := range *list {
				if covered[(*list)[i].ID] {
					(*list)[i].Planned = true
				}
			}
			return true, nil
		})
	}
	p.wakeup()
	p.mu.Lock()
	p.planSubmitted[tag] = true
	p.mu.Unlock()
	log.Printf("agenda: 今天（%s）的表已排定，%d 项", today, len(pl.Items))

	if len(pl.Items) == 0 {
		return "今天没有安排，整天空着。表已排定，这一轮到此为止，不要再次提交。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "今天（%s）的安排已定，共 %d 项：\n", pl.Weekday, len(pl.Items))
	for _, it := range pl.Items {
		b.WriteString(itemLine(it) + "\n")
	}
	for _, n := range overlapNotes(pl.Items, day, s.dayStartHour) {
		b.WriteString(n + "\n")
	}
	// 终结语。只回一份状态清单的话，模型会把它读成进展汇报，接着微调重排——
	// 成功的回执要自己说清「做完了」。
	b.WriteString("表已排定，这一轮到此为止：不要再次提交，也不必再调用别的工具。\n")
	return strings.TrimRight(b.String(), "\n"), nil
}

// itemLine 写一项：`a2 14:00-16:30 和林舟在图书馆查资料（尽量守，来自约定 c7）`。
func itemLine(it Item) string {
	tags := it.Flex
	if it.FromCommitment != "" {
		tags += "，来自约定 " + it.FromCommitment
	}
	return fmt.Sprintf("%s %s-%s %s（%s）", it.ID, it.Start, it.End, it.Title, tags)
}

// ---------- update_day_plan ----------

type updatePlanTool struct{ p *Plugin }

func (t *updatePlanTool) Name() string { return "update_day_plan" }

func (t *updatePlanTool) Description() string {
	return "更新今天日程里的一项：开始了、做完了、挪时间、改做法、延期到别天、跳过或取消。" +
		"做完写一句经历，跳过、取消、延期写原因。与对方的约定不能由你单方面改，先商量；" +
		"agreed_by_user 只在对方在本轮对话里明确同意后才传。"
}

func (t *updatePlanTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "项的 id，如 a2"},
			"status": {"type": "string", "enum": ["ongoing", "done", "skipped", "deferred", "cancelled"], "description": "新状态：开始了 / 做完了 / 跳过 / 延期到别天 / 取消"},
			"start": {"type": "string", "description": "新的开始时间 HH:MM"},
			"end": {"type": "string", "description": "新的结束时间 HH:MM"},
			"title": {"type": "string", "description": "改了做法时更新标题（30 字内）"},
			"busy": {"type": "string", "enum": ["轻忙", "重忙", "不回"], "description": "做这件事时能不能回消息"},
			"outcome": {"type": "string", "description": "做完时写一句经历；跳过、取消、延期时写原因（80 字内）"},
			"defer_to": {"type": "string", "description": "status 为 deferred 时必填：新日期 YYYY-MM-DD，可带 HH:MM"},
			"agreed_by_user": {"type": "boolean", "description": "与对方的约定要改时间、取消、延期或不去时，只在对方在本轮对话里明确同意后才传 true"}
		},
		"required": ["id"]
	}`)
}

func (t *updatePlanTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID      string  `json:"id"`
		Status  string  `json:"status"`
		Start   *string `json:"start"`
		End     *string `json:"end"`
		Title   *string `json:"title"`
		Busy    *string `json:"busy"`
		Outcome *string `json:"outcome"`
		DeferTo string  `json:"defer_to"`
		Agreed  bool    `json:"agreed_by_user"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	p := t.p
	s := p.snapshot()
	if s.base == "" {
		return "", errNotReady
	}
	a.ID = strings.TrimSpace(a.ID)
	loc, ok := p.findItem(ctx, a.ID)
	if !ok {
		return "", fmt.Errorf("没有 id 为 %q 的项，今天的表里有：%s", a.ID, p.idList(ctx))
	}
	st := p.storeFor(loc.tag)
	now := p.now()
	day := s.today(now)
	today := day.Format(dateLayout)

	var (
		changes    []string
		deferred   *Commitment
		wasOngoing bool
		result     Item
	)
	_, err := st.UpdatePlan(func(pl *Plan) (bool, error) {
		it := pl.item(a.ID)
		if it == nil {
			return false, fmt.Errorf("没有 id 为 %q 的项", a.ID)
		}
		wasOngoing = it.Status == statusOngoing
		timeChange := a.Start != nil || a.End != nil
		unilateral := timeChange || a.Status == statusSkipped || a.Status == statusDeferred || a.Status == statusCancelled
		if it.WithUser && unilateral && !a.Agreed {
			return false, fmt.Errorf(userRule, it.Title)
		}
		if it.terminal() && (timeChange || a.Status != "" || a.Title != nil || a.Busy != nil) {
			return false, fmt.Errorf("「%s」已经%s，不能再改；只有 outcome 还可以补", it.Title, statusCN(it.Status))
		}

		if a.Title != nil {
			title := squash(*a.Title)
			if title == "" {
				return false, fmt.Errorf("title 不能为空")
			}
			if err := checkRunes(title, "title", maxTitleRunes); err != nil {
				return false, err
			}
			if title != it.Title {
				changes = append(changes, fmt.Sprintf("改为「%s」", title))
				it.Title = title
			}
		}
		if timeChange {
			start, end := it.Start, it.End
			var err error
			if a.Start != nil {
				if start, err = normHHMM(*a.Start); err != nil {
					return false, fmt.Errorf("开始%w", err)
				}
			}
			if a.End != nil {
				if end, err = normHHMM(*a.End); err != nil {
					return false, fmt.Errorf("结束%w", err)
				}
			}
			if start != it.Start || end != it.End {
				it.Start, it.End = start, end
				changes = append(changes, fmt.Sprintf("时间改为 %s-%s", start, end))
				// 时间变了，到点的判断从头来：已派发的记录只对旧时间成立
				if it.Status == statusPlanned {
					it.StartFired, it.SoonFired = time.Time{}, time.Time{}
				}
				it.EndFired = time.Time{}
			}
		}
		if a.Busy != nil {
			b := strings.TrimSpace(*a.Busy)
			if !validBusy(b) {
				return false, fmt.Errorf("busy 只能是：%s", strings.Join(busyLevels, " / "))
			}
			if b != it.Busy {
				it.Busy = b
				changes = append(changes, "期间"+b)
			}
		}
		outcome := ""
		if a.Outcome != nil {
			outcome = squash(*a.Outcome)
			if err := checkRunes(outcome, "outcome", maxOutcomeRunes); err != nil {
				return false, err
			}
		}
		switch a.Status {
		case "":
			if a.Outcome != nil && outcome != it.Outcome {
				it.Outcome = outcome
				changes = append(changes, "经历已更新")
			}
		case statusOngoing:
			it.Status = statusOngoing
			if it.StartFired.IsZero() {
				it.StartFired = now
			}
			changes = append(changes, fmt.Sprintf("已标为进行中（到 %s）", it.End))
		case statusDone:
			it.Status, it.Outcome = statusDone, outcome
			if outcome == "" {
				changes = append(changes, "已做完（没写经历，下次记得写一句）")
			} else {
				changes = append(changes, "已做完："+outcome)
			}
		case statusSkipped, statusCancelled:
			if outcome == "" {
				return false, fmt.Errorf("%s要写原因（outcome）", statusCN(a.Status))
			}
			it.Status, it.Outcome = a.Status, outcome
			changes = append(changes, statusCN(a.Status)+"："+outcome)
		case statusDeferred:
			if outcome == "" {
				return false, fmt.Errorf("延期要写原因（outcome）")
			}
			date, hhmm, err := parseDeferTo(a.DeferTo, now.Location())
			if err != nil {
				return false, err
			}
			if date <= today {
				return false, fmt.Errorf("延期的日期要在今天之后；今天之内挪时间直接改 start / end")
			}
			it.Status, it.Outcome = statusDeferred, outcome
			changes = append(changes, "已延期："+outcome)
			c := Commitment{Date: date, Start: it.Start, End: it.End, Title: it.Title, With: it.With,
				WithUser: it.WithUser, Place: it.Place, Flex: it.Flex, Created: now,
				Note: fmt.Sprintf("从 %d/%d 延期：%s", int(day.Month()), day.Day(), outcome)}
			if hhmm != "" {
				c.Start, c.End = hhmm, ""
			}
			deferred = &c
		default:
			return false, fmt.Errorf("status 只能是：ongoing / done / skipped / deferred / cancelled")
		}
		result = *it
		return len(changes) > 0, nil
	})
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return fmt.Sprintf("%s 没有变化。", a.ID), nil
	}

	// 忙碌状态与开口理由跟着状态走
	switch {
	case result.Status == statusOngoing:
		clearAvailability()
		setAvailability(&result, day, s.dayStartHour, now)
		cue.Drop(availabilitySource, "soon|"+result.ID)
	case result.terminal():
		if wasOngoing {
			clearAvailability()
		}
		cue.Drop(availabilitySource, "soon|"+result.ID)
	}
	if deferred != nil {
		_, err := st.UpdateCommitments(func(list *[]Commitment, next func() string) (bool, error) {
			if len(*list) >= maxCommitments {
				return false, fmt.Errorf("约定已达上限（%d 条），先清理过期的", maxCommitments)
			}
			deferred.ID = next()
			*list = append(*list, *deferred)
			return true, nil
		})
		if err != nil {
			return "", fmt.Errorf("项已标为延期，但登记约定失败: %w", err)
		}
		changes = append(changes, fmt.Sprintf("已延期到 %s %s，登记为约定 %s",
			fmtDateCN(deferred.Date, now.Location()), deferred.Start, deferred.ID))
	}
	p.wakeup()
	log.Printf("agenda: %s %s", a.ID, strings.Join(changes, "；"))
	return fmt.Sprintf("%s %s。", a.ID, strings.Join(changes, "；")), nil
}

// idList 列出本轮写入域今天表里的项，供报错时参考。
func (p *Plugin) idList(ctx context.Context) string {
	st := p.writeStore(ctx)
	if st == nil {
		return "（空）"
	}
	pl, err := st.LoadPlan()
	if err != nil || len(pl.Items) == 0 {
		return "（空）"
	}
	parts := make([]string, 0, len(pl.Items))
	for _, it := range pl.Items {
		parts = append(parts, it.ID+" "+it.Title)
	}
	return strings.Join(parts, "、")
}

func statusCN(st string) string {
	switch st {
	case statusOngoing:
		return "进行中"
	case statusDone:
		return "做完"
	case statusSkipped:
		return "跳过"
	case statusDeferred:
		return "延期"
	case statusCancelled:
		return "取消"
	}
	return st
}

// parseDeferTo 解析 "YYYY-MM-DD" 或 "YYYY-MM-DD HH:MM"。
func parseDeferTo(s string, loc *time.Location) (date, hhmm string, err error) {
	fields := strings.Fields(s)
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", fmt.Errorf("defer_to 应为 YYYY-MM-DD，可带 HH:MM")
	}
	d, err := parseDate(fields[0], loc)
	if err != nil {
		return "", "", err
	}
	date = d.Format(dateLayout)
	if len(fields) == 2 {
		if hhmm, err = normHHMM(fields[1]); err != nil {
			return "", "", err
		}
	}
	return date, hhmm, nil
}

// ---------- add_commitment ----------

type addCommitmentTool struct{ p *Plugin }

func (t *addCommitmentTool) Name() string { return "add_commitment" }

func (t *addCommitmentTool) Description() string {
	return "登记一件定在某天某时的事：和对方约好的、答应别人的、自己要去办的。有具体日期时刻才用它；" +
		"传今天的日期时直接加进今天的表。对方要你到点提醒他的事用定时任务，不在这里登记。"
}

func (t *addCommitmentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"date": {"type": "string", "description": "日期 YYYY-MM-DD，不能是今天以前"},
			"start": {"type": "string", "description": "开始时间 HH:MM"},
			"end": {"type": "string", "description": "结束时间 HH:MM（可省）"},
			"title": {"type": "string", "description": "做什么，短句（30 字内）"},
			"with": {"type": "array", "items": {"type": "string"}, "description": "同行的人，必须是人物清单里已有的名字"},
			"with_user": {"type": "boolean", "description": "是否与对方一起；为真时一律不能动"},
			"place": {"type": "string", "description": "在哪（30 字内，可省）"},
			"note": {"type": "string", "description": "备注一句（60 字内，可省）"},
			"flex": {"type": "string", "enum": ["可挪", "尽量守", "不能动"], "description": "能不能挪，不填为尽量守"}
		},
		"required": ["date", "start", "title"]
	}`)
}

func (t *addCommitmentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Date     string   `json:"date"`
		Start    string   `json:"start"`
		End      string   `json:"end"`
		Title    string   `json:"title"`
		With     []string `json:"with"`
		WithUser bool     `json:"with_user"`
		Place    string   `json:"place"`
		Note     string   `json:"note"`
		Flex     string   `json:"flex"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	p := t.p
	s := p.snapshot()
	st := p.writeStore(ctx)
	if st == nil {
		return "", errNotReady
	}
	now := p.now()
	day := s.today(now)
	today := day.Format(dateLayout)

	d, err := parseDate(a.Date, now.Location())
	if err != nil {
		return "", err
	}
	c := Commitment{Date: d.Format(dateLayout), Title: squash(a.Title), Place: squash(a.Place),
		Note: squash(a.Note), WithUser: a.WithUser, Flex: strings.TrimSpace(a.Flex), Created: now}
	if c.Date < today {
		return "", fmt.Errorf("%s 已经过去了，约定只能登记今天或之后的日子", fmtDateCN(c.Date, now.Location()))
	}
	if c.Title == "" {
		return "", fmt.Errorf("title 不能为空")
	}
	if err := checkRunes(c.Title, "title", maxTitleRunes); err != nil {
		return "", err
	}
	if err := checkRunes(c.Place, "place", maxPlaceRunes); err != nil {
		return "", err
	}
	if err := checkRunes(c.Note, "note", maxNoteRunes); err != nil {
		return "", err
	}
	if c.Start, err = normHHMM(a.Start); err != nil {
		return "", fmt.Errorf("开始%w", err)
	}
	if strings.TrimSpace(a.End) != "" {
		if c.End, err = normHHMM(a.End); err != nil {
			return "", fmt.Errorf("结束%w", err)
		}
	}
	if c.With, err = p.checkWith(ctx, a.With); err != nil {
		return "", err
	}
	switch {
	case c.WithUser:
		c.Flex = flexFixed
	case c.Flex == "":
		c.Flex = flexTry
	case !validFlex(c.Flex):
		return "", fmt.Errorf("flex 只能是：%s", strings.Join(flexLevels, " / "))
	}

	// 今天的约定：表已排定就直接追加进表，否则留在约定里等规划时排入
	var appended *Item
	if c.Date == today {
		pl, err := st.LoadPlan()
		if err != nil {
			return "", err
		}
		if pl.Date == today {
			if len(pl.Items) >= hardMaxItems {
				return "", fmt.Errorf("今天的表已有 %d 项，不能再加", len(pl.Items))
			}
			c.Planned = true
			it := Item{Title: c.Title, Start: c.Start, End: c.End, Place: c.Place, With: c.With,
				WithUser: c.WithUser, Flex: c.Flex, Busy: defaultBusy, Status: statusPlanned}
			if it.End == "" {
				it.End = it.Start // 没说到几点：按「到点就算」处理，模型可再改
			}
			appended = &it
		}
	}
	_, err = st.UpdateCommitments(func(list *[]Commitment, next func() string) (bool, error) {
		if len(*list) >= maxCommitments {
			return false, fmt.Errorf("约定已达上限（%d 条），先用 cancel_commitment 清掉不再成立的", maxCommitments)
		}
		c.ID = next()
		*list = append(*list, c)
		return true, nil
	})
	if err != nil {
		return "", err
	}
	var overlap []string
	if appended != nil {
		appended.FromCommitment = c.ID
		pl, err := st.UpdatePlan(func(pl *Plan) (bool, error) {
			appended.ID = fmt.Sprintf("a%d", len(pl.Items)+1)
			pl.Items = append(pl.Items, *appended)
			return true, nil
		})
		if err != nil {
			return "", fmt.Errorf("约定已登记，但加进今天的表失败: %w", err)
		}
		overlap = overlapNotes(pl.Items, day, s.dayStartHour)
	}
	p.wakeup()
	log.Printf("agenda: 登记约定 %s：%s %s %s", c.ID, c.Date, c.Start, c.Title)

	desc := fmt.Sprintf("%s %s%s %s（%s）", fmtDateCN(c.Date, now.Location()), c.Start, dashEnd(c.End), c.Title, c.Flex)
	if appended != nil {
		out := fmt.Sprintf("已登记约定 %s 并加进今天的表（%s）：%s。", c.ID, appended.ID, desc)
		if len(overlap) > 0 {
			out += "\n" + strings.Join(overlap, "\n")
		}
		return out, nil
	}
	if c.Date == today {
		return fmt.Sprintf("已登记约定 %s：%s。今天的表还没排，排表时会要求把它排进去。", c.ID, desc), nil
	}
	return fmt.Sprintf("已登记约定 %s：%s。", c.ID, desc), nil
}

// ---------- cancel_commitment ----------

type cancelCommitmentTool struct{ p *Plugin }

func (t *cancelCommitmentTool) Name() string { return "cancel_commitment" }

func (t *cancelCommitmentTool) Description() string {
	return "取消一条约定，要写原因。与对方的约定不能由你单方面取消，先商量；" +
		"agreed_by_user 只在对方在本轮对话里明确同意后才传。"
}

func (t *cancelCommitmentTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "约定的 id，如 c8"},
			"reason": {"type": "string", "description": "取消的原因，一句话（80 字内）"},
			"agreed_by_user": {"type": "boolean", "description": "与对方的约定，只在对方在本轮对话里明确同意后才传 true"}
		},
		"required": ["id", "reason"]
	}`)
}

func (t *cancelCommitmentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
		Agreed bool   `json:"agreed_by_user"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	p := t.p
	if p.snapshot().base == "" {
		return "", errNotReady
	}
	a.ID = strings.TrimSpace(a.ID)
	reason := squash(a.Reason)
	if reason == "" {
		return "", fmt.Errorf("reason 不能为空：一句话说明为什么取消")
	}
	if err := checkRunes(reason, "reason", maxOutcomeRunes); err != nil {
		return "", err
	}

	// 约定在可读域里找（写入域优先），写回它所在的域
	var (
		st      *Store
		removed Commitment
		found   bool
	)
	for _, tag := range p.readDomains(ctx) {
		cand := p.storeFor(tag)
		if cand == nil {
			continue
		}
		_, err := cand.UpdateCommitments(func(list *[]Commitment, _ func() string) (bool, error) {
			for i, c := range *list {
				if c.ID != a.ID {
					continue
				}
				if c.WithUser && !a.Agreed {
					return false, fmt.Errorf(userRule, c.Title)
				}
				removed, found = c, true
				*list = append((*list)[:i], (*list)[i+1:]...)
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return "", err
		}
		if found {
			st = cand
			break
		}
	}
	if !found {
		return "", fmt.Errorf("没有 id 为 %q 的约定，可用 list_commitments 查看", a.ID)
	}
	// 已排进今天表里的，表上那一项一并取消
	var itemID string
	wasOngoing := false
	_, _ = st.UpdatePlan(func(pl *Plan) (bool, error) {
		for i := range pl.Items {
			it := &pl.Items[i]
			if it.FromCommitment == removed.ID && !it.terminal() {
				wasOngoing = it.Status == statusOngoing
				it.Status, it.Outcome = statusCancelled, reason
				itemID = it.ID
				return true, nil
			}
		}
		return false, nil
	})
	if itemID != "" {
		cue.Drop(availabilitySource, "soon|"+itemID)
		if wasOngoing {
			clearAvailability()
		}
	}
	p.wakeup()
	log.Printf("agenda: 取消约定 %s（%s）：%s", removed.ID, removed.Title, reason)
	out := fmt.Sprintf("已取消约定 %s（%s %s %s）：%s。", removed.ID, fmtDateCN(removed.Date, p.now().Location()), removed.Start, removed.Title, reason)
	if itemID != "" {
		out += fmt.Sprintf("今天表里的 %s 一并标为取消。", itemID)
	}
	return out, nil
}

// ---------- list_commitments ----------

type listCommitmentsTool struct{ p *Plugin }

func (t *listCommitmentsTool) Name() string { return "list_commitments" }

func (t *listCommitmentsTool) Description() string {
	return "列出全部已登记的约定（含备注与同行的人），可只看某一天。用于注入的约定不全、或要核对某天的安排时。"
}

func (t *listCommitmentsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"date": {"type": "string", "description": "只看这一天 YYYY-MM-DD；留空表示全部"}
		}
	}`)
}

func (t *listCommitmentsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Date string `json:"date"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	p := t.p
	st := p.writeStore(ctx)
	if st == nil {
		return "", errNotReady
	}
	now := p.now()
	filter := ""
	if strings.TrimSpace(a.Date) != "" {
		d, err := parseDate(a.Date, now.Location())
		if err != nil {
			return "", err
		}
		filter = d.Format(dateLayout)
	}
	cs, err := st.LoadCommitments()
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "没有登记任何约定。", nil
	}
	sortCommitments(cs)
	var b strings.Builder
	matched := 0
	for _, c := range cs {
		if filter != "" && c.Date != filter {
			continue
		}
		matched++
		fmt.Fprintf(&b, "- [%s] %s %s%s %s（%s）", c.ID, fmtDateShort(c.Date, now.Location()), c.Start, dashEnd(c.End), c.Title, c.Flex)
		if c.WithUser {
			b.WriteString("，和对方")
		}
		if len(c.With) > 0 {
			b.WriteString("，和 " + strings.Join(c.With, "、"))
		}
		if c.Place != "" {
			b.WriteString("，在 " + c.Place)
		}
		if c.Note != "" {
			b.WriteString("；" + c.Note)
		}
		if c.Planned {
			b.WriteString("（已排进当天的表）")
		}
		b.WriteString("\n")
	}
	if matched == 0 {
		return fmt.Sprintf("共 %d 条约定，%s 没有安排。", len(cs), fmtDateCN(filter, now.Location())), nil
	}
	return fmt.Sprintf("共 %d 条：\n%s", matched, strings.TrimRight(b.String(), "\n")), nil
}
