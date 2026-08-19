package bodysense

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("接触记录尚未就绪")

const (
	// maxTouchesPerCall 限制一次上报的条数。计数没有回退手段，一次幻觉不该能加上
	// 几十次，所以宁可让模型分两轮报。
	maxTouchesPerCall = 10
	maxListBytes      = 8 * 1024
)

// ---------- record_touch ----------

type recordTool struct{ p *Plugin }

func (t *recordTool) Name() string { return "record_touch" }

func (t *recordTool) Description() string {
	return "记录角色身体的某些部位被触碰，并返回这些部位累计的接触次数与熟悉阶段。" +
		"发现接触时先调用它拿到阶段，再写这一处的反应；一轮里的多处接触合并成一次调用。" +
		"只记角色自己被触碰的部位——角色伸手去碰对方时碰到的是对方的身体，不在此列。"
}

// Schema 的 part 用配置生成的 enum。Agent 每次请求都重新取工具声明，
// 因此改了部位表即时生效，不存在缓存过期。
func (t *recordTool) Schema() json.RawMessage {
	enum, err := json.Marshal(partNames(t.p.snapshot().parts))
	if err != nil || len(enum) == 0 {
		enum = []byte("[]")
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"touches": {
				"type": "array",
				"description": "本轮真实发生的接触，最多 ` + fmt.Sprint(maxTouchesPerCall) + ` 项；同一部位重复出现只算一次",
				"items": {
					"type": "object",
					"properties": {
						"part": {"type": "string", "description": "被触碰的部位，只能从给出的取值中选", "enum": ` + string(enum) + `},
						"action": {"type": "string", "description": "接触的方式，一两个字，如 牵、握、抚、靠、吻；可省略"}
					},
					"required": ["part"]
				}
			}
		},
		"required": ["touches"]
	}`)
}

func (t *recordTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Touches []struct {
			Part   string `json:"part"`
			Action string `json:"action"`
		} `json:"touches"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if len(a.Touches) == 0 {
		return "", fmt.Errorf("没有要记录的接触")
	}
	if len(a.Touches) > maxTouchesPerCall {
		return "", fmt.Errorf("一次最多记录 %d 处接触，本次给了 %d 处，请分次上报",
			maxTouchesPerCall, len(a.Touches))
	}

	s := t.p.snapshot()
	touches := make([]Touch, 0, len(a.Touches))
	for _, in := range a.Touches {
		part, ok := findPart(s.parts, in.Part)
		if !ok {
			// 列出的是配置表里的部位名，与可见域无关。改成「列出已记录的部位」会让
			// 不可读域的存在性从报错文案里漏出去。
			return "", fmt.Errorf("没有名为 %q 的部位，可选：%s",
				strings.TrimSpace(in.Part), strings.Join(partNames(s.parts), "、"))
		}
		touches = append(touches, Touch{Part: part.Name, Action: in.Action})
	}

	store := t.p.writeStore(ctx)
	if store == nil {
		return "", errNotReady
	}
	local, err := store.Record(touches)
	if err != nil {
		return "", err
	}

	// 回显必须是本轮可读域求和后的值，与 [身体接触记录] 里注入的口径一致；
	// 回显写入域自己的计数会让模型看到一个更小的数，当场自相矛盾。
	sums := map[string]view{}
	if views, _, err := t.p.visibleViews(ctx); err == nil {
		for _, v := range views {
			sums[strings.ToLower(v.Name)] = v
		}
	}

	var b strings.Builder
	b.WriteString("已记录：")
	for i, st := range local {
		if i > 0 {
			b.WriteString("；")
		}
		count, privacy := st.Count, 0
		if v, ok := sums[strings.ToLower(st.Part)]; ok {
			count, privacy = v.Count, v.Privacy
		} else if part, ok := findPart(s.parts, st.Part); ok {
			privacy = part.Privacy
		}
		fmt.Fprintf(&b, "%s ×%d（%s · %s）", st.Part, count, stageOf(count, s.pace), privacyLabel(privacy))
	}
	return b.String(), nil
}

// ---------- adjust_body_state ----------

type adjustStateTool struct{ p *Plugin }

func (t *adjustStateTool) Name() string { return "adjust_body_state" }

func (t *adjustStateTool) Description() string {
	return "按本轮演绎调整角色此刻的身体状态（唤起与疲劳，各 0-100），并返回调整后的状态。" +
		"报的是变化量而不是目标值。出现明显改变身体状态的事时先调用它，再据此写这一轮的反应；" +
		"一轮最多调一次，两项可以同时报。"
}

func (t *adjustStateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"arousal_delta": {"type": "integer", "description": "唤起的变化量，正数推高、负数平复。轻微的事几点，剧烈的事几十点；不变可省略"},
			"fatigue_delta": {"type": "integer", "description": "疲劳的变化量，正数更累、负数恢复。不变可省略"},
			"reason": {"type": "string", "description": "这次变化的起因，一句话"}
		},
		"required": ["reason"]
	}`)
}

func (t *adjustStateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ArousalDelta int    `json:"arousal_delta"`
		FatigueDelta int    `json:"fatigue_delta"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}

	s := t.p.snapshot()
	// 写入本轮写入域：与接触计数同一套分域规则
	store := t.p.stateStoreFor(plugin.ScopeFrom(ctx).Write)
	if store == nil {
		return "", errNotReady
	}
	st, appliedA, appliedF, err := store.Apply(a.ArousalDelta, a.FatigueDelta,
		s.stateMaxDelta, s.arousalDecay, s.fatigueDecay, a.Reason, time.Now())
	if err != nil {
		return "", err
	}

	// 回显本轮可读域合并后的口径，与 [当前身体状态] 注入一致
	if merged, ok, err := t.p.visibleState(ctx); err == nil && ok {
		st.Arousal, st.Fatigue = merged.Arousal, merged.Fatigue
	}
	out := fmt.Sprintf("身体状态：唤起 %s（%d，本次 %+d），疲劳 %s（%d，本次 %+d）",
		arousalBand(st.Arousal), st.Arousal, appliedA,
		fatigueBand(st.Fatigue), st.Fatigue, appliedF)
	// 拦截生效时要把规则告诉模型，否则它只会换个更大的数字再试一次
	var capped []string
	if appliedA != a.ArousalDelta {
		capped = append(capped, fmt.Sprintf("唤起原报 %+d", a.ArousalDelta))
	}
	if appliedF != a.FatigueDelta {
		capped = append(capped, fmt.Sprintf("疲劳原报 %+d", a.FatigueDelta))
	}
	if len(capped) > 0 {
		out += fmt.Sprintf("。变化量按单次上限 ±%d 收了（%s）", s.stateMaxDelta, strings.Join(capped, "，"))
	}
	return out + "。", nil
}

// ---------- list_body_state ----------

type listTool struct{ p *Plugin }

func (t *listTool) Name() string { return "list_body_state" }

func (t *listTool) Description() string {
	return "列出各部位已累计的接触次数与熟悉阶段，用于记录未完整注入、或需要确认某处是否被碰过时。"
}

func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *listTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.p.snapshot().base == "" {
		return "", errNotReady
	}
	// 只数、只列本轮可读的记录：条数本身也会泄漏「存在什么」
	views, orphans, err := t.p.visibleViews(ctx)
	if err != nil {
		return "", err
	}
	if len(views) == 0 && orphans == 0 {
		return "还没有任何部位的接触记录。", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个部位有接触记录：\n", len(views))
	for _, v := range views {
		fmt.Fprintf(&b, "- %s：%d 次（%s）· %s\n", v.Name, v.Count, v.Stage, privacyLabel(v.Privacy))
	}
	if line := lastTouchLine(views); line != "" {
		b.WriteString(line + "\n")
	}
	if orphans > 0 {
		fmt.Fprintf(&b, "（另有 %d 条记录的部位已不在当前部位表中，改回原名即可恢复）\n", orphans)
	}

	out, truncated := plugin.TruncateUTF8([]byte(strings.TrimRight(b.String(), "\n")), maxListBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[超过 %d 字节，已截断]", maxListBytes)
	}
	return res, nil
}
