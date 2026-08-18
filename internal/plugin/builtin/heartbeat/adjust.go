package heartbeat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"wen/internal/plugin"
)

// judgeTimeout 是单次动态判定的时长上限。
const judgeTimeout = 2 * time.Minute

// OnTurnEnd 观察每轮对话：真人交互的轮次刷新活跃时间并重置心跳时钟，动态心跳开启时
// 另起 goroutine 让辅助模型判定聊天热度并调整间隔。后台轮次（含心跳自己）一律忽略。
// 本方法在轮次收尾的同步路径上被调用，必须快速返回。
func (p *Plugin) OnTurnEnd(_ context.Context, ev plugin.TurnEndEvent) {
	if ev.Origin != "" || !ev.Interactive {
		return
	}
	p.mu.Lock()
	prevActive := p.lastActive
	p.lastActive = ev.EndedAt
	// 真人刚聊完，心跳倒计时从此刻重新开始：心跳是「没人说话时才主动开口」的机制，
	// 聊天途中插进来的心跳既打断对话，也让间隔配置失去意义。
	p.resetClockLocked(ev.EndedAt)
	dynamic, complete, busy := p.dynamic, p.complete, p.adjusting
	ctx := p.ctx
	if dynamic && complete != nil && !busy && ctx != nil {
		p.adjusting = true
	} else {
		dynamic = false // 不判定
	}
	p.mu.Unlock()

	if !dynamic {
		return
	}
	// 判定要调模型，不能挡住轮次收尾；同一时刻最多一次在途，堆积没有意义
	go p.judge(ctx, complete, ev, prevActive)
}

// judge 让辅助模型根据本轮对话与节奏信息给出「加快 / 放缓 / 保持」，按其结果调整间隔。
// 提升与降低的幅度对折/翻倍，限幅在配置的最快与最慢之间；解析失败按保持处理。
func (p *Plugin) judge(ctx context.Context, complete plugin.CompleteFunc, ev plugin.TurnEndEvent, prevActive time.Time) {
	defer func() {
		p.mu.Lock()
		p.adjusting = false
		p.mu.Unlock()
	}()

	p.mu.Lock()
	cur := p.cur
	p.mu.Unlock()

	gap := "未知（这是启动后的第一轮对话）"
	if !prevActive.IsZero() {
		gap = ev.StartedAt.Sub(prevActive).Round(time.Second).String()
	}
	prompt := fmt.Sprintf(`你在为一个定时心跳功能判定节奏。心跳会按固定间隔主动唤醒助手跟进对话，间隔应随聊天热度伸缩：
- 聊天激烈、发生争执、用户对话题明显感兴趣、回复间隔很短 → 应该加快
- 语气冷漠、心情平静、对话题不感兴趣、回复间隔很长 → 应该放缓
- 介于两者之间或信息不足 → 保持

当前心跳间隔：%s
距上一轮对话的间隔：%s

本轮对话：
用户：%s
助手：%s

只输出以下三个词中的一个，不要输出任何其他内容：加快、放缓、保持`,
		cur, gap, truncate(ev.UserInput, 500), truncate(ev.FinalText, 500))

	jctx, cancel := context.WithTimeout(ctx, judgeTimeout)
	defer cancel()
	out, err := complete(jctx, prompt)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("heartbeat: 动态判定失败，节奏保持不变: %v", err)
		}
		return
	}

	verdict := strings.TrimSpace(out)
	p.mu.Lock()
	switch {
	case strings.Contains(verdict, "加快"):
		p.cur = p.clamp(p.cur / 2)
	case strings.Contains(verdict, "放缓"):
		p.cur = p.clamp(p.cur * 2)
	default: // 保持，或模型没按格式回答
		p.mu.Unlock()
		return
	}
	next := p.cur
	wake := p.wake
	dir := p.stateDir
	p.mu.Unlock()

	// 同步写：judge 本身就跑在自己的 goroutine 里，再起一个只会让这次写盘脱离
	// 插件的生命周期——停掉插件、甚至进程退出之后才落地
	persistInterval(dir, next)
	log.Printf("heartbeat: 动态判定「%s」，心跳间隔调整为 %v", verdict, next)
	select { // 唤醒循环重算下一次心跳时刻
	case wake <- struct{}{}:
	default:
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
