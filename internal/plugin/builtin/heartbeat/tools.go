package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

type setIntervalTool struct{ p *Plugin }

func (t *setIntervalTool) Name() string { return "set_heartbeat_interval" }

func (t *setIntervalTool) Description() string {
	return "设定隔多久之后再主动开口找用户，调用后倒计时从此刻重新开始。" +
		"话题正热、或者答应了要跟进某件事，就把它调短；" +
		"对方说要去忙、要去睡，或者眼下确实没什么可跟进的，就调长。" +
		"配置了情境提示词时，可顺带用 context 声明当下的情境，下次醒来按那套提示词行事。" +
		"拿不准时不必调用——不调用就沿用当前节奏。"
}

// Schema 的 context 用配置生成的 enum。Agent 每次请求都重新取工具声明，
// 因此改了情境配置即时生效。没配置任何情境时不声明该参数——一个只有保留值的
// 参数只会诱导模型去传它。
func (t *setIntervalTool) Schema() json.RawMessage {
	base := `"minutes": {"type": "integer", "description": "多少分钟之后再主动开口"},
			"reason": {"type": "string", "description": "这样定的理由，一句话，如「她说要去睡了」"}`
	names := contextNames(t.p.snapshotContexts())
	if len(names) > 0 {
		enum, err := json.Marshal(append(names, contextDefault))
		if err == nil {
			base += `,
			"context": {"type": "string", "description": "当下的情境，决定下次心跳用哪套提示词；不传则沿用当前情境，传「` +
				contextDefault + `」回到默认提示词", "enum": ` + string(enum) + `}`
		}
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			` + base + `
		},
		"required": ["minutes", "reason"]
	}`)
}

func (t *setIntervalTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if a.Minutes <= 0 {
		return "", fmt.Errorf("间隔要是一个正整数分钟")
	}

	p := t.p
	p.mu.Lock()
	if !p.dynamic {
		cur := p.cur
		p.mu.Unlock()
		// 拦截生效时要把规则说清楚，否则模型只会换个数字再试一次
		return "", fmt.Errorf("当前是固定节奏（每 %s），节奏不可调整。如需可调，请在设置页打开心跳插件的「动态心跳」",
			humanDur(cur))
	}

	ctxNote := ""
	if a.Context != "" {
		switch {
		case a.Context == contextDefault:
			p.curContext = ""
			ctxNote = "，情境回到默认"
		default:
			if _, ok := p.contexts[a.Context]; !ok {
				names := contextNames(p.contexts)
				p.mu.Unlock()
				if len(names) == 0 {
					return "", fmt.Errorf("尚未配置任何情境提示词，context 参数不可用")
				}
				return "", fmt.Errorf("没有名为 %q 的情境，可选：%s", a.Context, strings.Join(names, "、"))
			}
			p.curContext = a.Context
			ctxNote = fmt.Sprintf("，情境切到「%s」", a.Context)
		}
	}

	want := time.Duration(a.Minutes) * time.Minute
	p.cur = p.normalize(want)
	p.adjusted = true
	// 倒计时从此刻重算：「N 分钟之后再开口」说的是从现在起算。resetClockLocked
	// 只前推不回拨，因此这里不会把已经临近的一次心跳推回过去。
	p.resetClockLocked(time.Now())
	next, minIv, maxIv := p.cur, p.minIv, p.maxIv
	dir, st := p.snapshotStateLocked()
	p.mu.Unlock()

	// 同步写：Execute 本就在轮次的同步路径上，另起 goroutine 会让这次写盘
	// 脱离插件的生命周期
	persistState(dir, st)
	log.Printf("heartbeat: 模型设定心跳间隔为 %v（%s）", next, a.Reason)

	out := "好，下次主动开口在 " + humanDur(next) + "后" + ctxNote + "。"
	if next != want {
		out += fmt.Sprintf("（你报的是 %d 分钟，已按设置的范围 %s ~ %s 收到 %s）",
			a.Minutes, humanDur(minIv), humanDur(maxIv), humanDur(next))
	}
	return out, nil
}

// snapshotContexts 取一份情境表快照（Schema 生成用）。
func (p *Plugin) snapshotContexts() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contexts
}

// ---------- pause_heartbeat ----------

// maxPauseMinutes 限制单次暂停时长：暂停不是关闭，睡一觉的量级封顶。
const maxPauseMinutes = 24 * 60

type pauseTool struct{ p *Plugin }

func (t *pauseTool) Name() string { return "pause_heartbeat" }

func (t *pauseTool) Description() string {
	return "暂停主动开口一段时间，到点恢复常规节奏（睡醒回到平常状态）——对方说要去睡、" +
		"要出门长时间离开时用它，免得在不合适的时候被定时唤醒。" +
		"期间对方随时来消息不受影响，且对方一说话暂停即自动取消。"
}

func (t *pauseTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"minutes": {"type": "integer", "description": "暂停多少分钟（最长 ` + fmt.Sprint(maxPauseMinutes) + `，即一天）。睡一夜按 420-540 报"},
			"reason": {"type": "string", "description": "暂停的理由，一句话，如「她说要睡了」"}
		},
		"required": ["minutes", "reason"]
	}`)
}

func (t *pauseTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if a.Minutes <= 0 {
		return "", fmt.Errorf("暂停时长要是一个正整数分钟")
	}
	capped := min(a.Minutes, maxPauseMinutes)

	p := t.p
	p.mu.Lock()
	if !p.dynamic {
		cur := p.cur
		p.mu.Unlock()
		return "", fmt.Errorf("当前是固定节奏（每 %s），不可暂停。如需可调，请在设置页打开心跳插件的「动态心跳」",
			humanDur(cur))
	}
	now := time.Now()
	p.pausedAt = now
	p.pausedUntil = now.Add(time.Duration(capped) * time.Minute)
	// 循环可能正挂在一个更早的到期时刻上，叫醒它按暂停重算
	select {
	case p.wake <- struct{}{}:
	default:
	}
	until := p.pausedUntil
	dir, st := p.snapshotStateLocked()
	p.mu.Unlock()

	persistState(dir, st)
	log.Printf("heartbeat: 模型暂停心跳至 %s（%s）", until.Format("2006-01-02 15:04"), a.Reason)

	out := "好，接下来 " + humanDur(time.Duration(capped)*time.Minute) + "内不再主动开口，到点恢复常规节奏；对方一说话即恢复。"
	if capped != a.Minutes {
		out += fmt.Sprintf("（你报的是 %d 分钟，已按上限收到 %d 分钟）", a.Minutes, capped)
	}
	return out, nil
}
