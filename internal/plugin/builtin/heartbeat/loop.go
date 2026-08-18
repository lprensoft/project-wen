package heartbeat

import (
	"context"
	"errors"
	"log"
	"time"

	"wen/internal/plugin"
)

// loop 是心跳插件唯一的常驻 goroutine：按当前间隔发心跳、按固定周期做空闲衰减。
// 下一次心跳时刻始终按「上次心跳 + 当前间隔」推算，衰减 tick 与间隔调整都不会
// 重置倒计时——否则每 15 分钟一次的衰减检查会让更长的心跳间隔永远走不完。
func (p *Plugin) loop(ctx context.Context) {
	defer p.wg.Done()
	decay := time.NewTicker(decayEvery)
	defer decay.Stop()

	for {
		p.mu.Lock()
		next := p.lastBeat.Add(p.cur)
		p.mu.Unlock()

		timer := time.NewTimer(time.Until(next)) // 已过期时零延时触发
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.wake: // 间隔被动态调整，重算下一次心跳时刻
			timer.Stop()
		case <-decay.C:
			timer.Stop()
			p.maybeDecay()
		case <-timer.C:
			p.beat(ctx)
		}
	}
}

// resetClockLocked 把心跳倒计时的起点推到 at，并唤醒循环重算下一次心跳时刻。
// 只前推不回拨：事件时间戳缺失或落后于已记录的起点时保持原值，避免把倒计时拨回过去
// 反而立刻触发一次心跳。调用方需持有 p.mu。
func (p *Plugin) resetClockLocked(at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	if !at.After(p.lastBeat) {
		return
	}
	p.lastBeat = at
	select { // 循环正挂在旧的到期时刻上，不叫醒它这次重置要到下次心跳后才生效
	case p.wake <- struct{}{}:
	default:
	}
}

// beat 执行一次心跳：挑最近活跃的会话（没有就新建），以心跳提示词跑一轮对话。
// 会话忙时跳过本次——心跳排队毫无意义，下个周期自然会再来。
func (p *Plugin) beat(ctx context.Context) {
	p.mu.Lock()
	p.lastBeat = time.Now()
	runTurn, prompt := p.runTurn, p.prompt
	p.mu.Unlock()

	sid, err := p.pickSession()
	if err != nil {
		log.Printf("heartbeat: 找不到可用会话，本次心跳跳过: %v", err)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, beatTimeout)
	defer cancel()
	// 心跳提示词是一次性输入：只发给当轮模型，不留在后续上下文，界面不按用户消息展示
	tctx = plugin.WithEphemeralInput(tctx)
	if _, err := runTurn(tctx, sid, prompt); err != nil {
		if errors.Is(err, plugin.ErrSessionBusy) {
			log.Printf("heartbeat: 会话 %s 忙，本次心跳跳过", sid)
		} else if ctx.Err() == nil { // 停止时的取消错误不值得记
			log.Printf("heartbeat: 心跳轮次失败: %v", err)
		}
	}
}

// pickSession 返回最近活跃的会话：LastActiveAt 最大者；旧会话没有该字段回落 CreatedAt。
// 一个会话都没有时新建一个。
func (p *Plugin) pickSession() (string, error) {
	p.mu.Lock()
	sessions, newSession := p.sessions, p.newSession
	p.mu.Unlock()

	metas, err := sessions.List()
	if err != nil {
		return "", err
	}
	bestID := ""
	var bestAt time.Time
	for _, m := range metas {
		at := m.CreatedAt
		if m.LastActiveAt != nil {
			at = *m.LastActiveAt
		}
		if bestID == "" || at.After(bestAt) {
			bestID, bestAt = m.ID, at
		}
	}
	if bestID != "" {
		return bestID, nil
	}
	if newSession == nil {
		return "", errors.New("没有会话且当前环境不支持新建")
	}
	return newSession()
}

// maybeDecay 空闲衰减：距最近真人交互超过一个当前间隔时，把间隔放缓一档（×1.5），
// 直到最慢间隔。动态心跳关闭时不衰减——那是「固定节奏」的含义。
func (p *Plugin) maybeDecay() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dynamic || p.cur >= p.maxIv {
		return
	}
	if !p.lastActive.IsZero() && time.Since(p.lastActive) < p.cur {
		return
	}
	p.cur = p.clamp(p.cur * 3 / 2)
	go p.saveInterval(p.cur)
	log.Printf("heartbeat: 无人聊天，心跳放缓至 %v", p.cur)
}
