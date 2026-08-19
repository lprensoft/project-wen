package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

type setIntervalTool struct{ p *Plugin }

func (t *setIntervalTool) Name() string { return "set_heartbeat_interval" }

func (t *setIntervalTool) Description() string {
	return "设定隔多久之后再主动开口找用户，调用后倒计时从此刻重新开始。" +
		"话题正热、或者答应了要跟进某件事，就把它调短；" +
		"对方说要去忙、要去睡，或者眼下确实没什么可跟进的，就调长。" +
		"拿不准时不必调用——不调用就沿用当前节奏。"
}

func (t *setIntervalTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"minutes": {"type": "integer", "description": "多少分钟之后再主动开口"},
			"reason": {"type": "string", "description": "这样定的理由，一句话，如「她说要去睡了」"}
		},
		"required": ["minutes", "reason"]
	}`)
}

func (t *setIntervalTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
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

	out := "好，下次主动开口在 " + humanDur(next) + "后。"
	if next != want {
		out += fmt.Sprintf("（你报的是 %d 分钟，已按设置的范围 %s ~ %s 收到 %s）",
			a.Minutes, humanDur(minIv), humanDur(maxIv), humanDur(next))
	}
	return out, nil
}
