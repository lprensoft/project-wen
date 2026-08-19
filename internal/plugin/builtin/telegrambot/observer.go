package telegrambot

import (
	"context"
	"strings"

	"wen/internal/imbot"
	"wen/internal/plugin"
)

// OnTurnEnd 观察每轮对话的结束：后台轮次（心跳、定时任务等其他插件发起）落在
// 绑定到 Telegram 用户的会话上时，把助手的最终文本推送过去——否则结果只写进
// 会话文件，对面的人永远看不到。前台轮次与本插件自己发起的轮次不推：它们各有
// 回复渠道。
//
// Telegram 没有「被动回复窗口」之类的限制，直接按 chat_id 发即可，不需要像微信
// 那样保存回复凭据。广播发生在轮次收尾的同步路径上，发送工作放 goroutine；
// 不能用广播的 ctx——发起方（如心跳）在轮次结束后立即取消它。
func (p *Plugin) OnTurnEnd(_ context.Context, ev plugin.TurnEndEvent) {
	if ev.Origin == "" || ev.Origin == p.Name() {
		return
	}
	// 另一条通道发起的轮次：投递责任归它那一侧的路由，观察者一律不插手。
	// 没有这一句，两条通道绑在同一会话上时，一边的前台回复会被另一边再推一遍
	// ——分通道功能正是要让它们绑在同一会话上。
	if imbot.IsChannel(ev.Origin) {
		return
	}
	// 装了分通道路由时，后台轮次也跟着人格走：不归我服务的会话不推
	if !imbot.ServedBy(p.Name(), ev.SessionID) {
		return
	}
	if strings.TrimSpace(ev.FinalText) == "" {
		return
	}

	p.mu.Lock()
	if p.cancel == nil { // 已停止
		p.mu.Unlock()
		return
	}
	core, pctx := p.core, p.ctx
	users := core.UsersFor(ev.SessionID)
	if len(users) == 0 {
		p.mu.Unlock()
		return
	}
	p.wg.Add(len(users))
	p.mu.Unlock()

	for _, chatID := range users {
		go func(chatID string) {
			defer p.wg.Done()
			p.send(pctx, chatID, ev.FinalText, "")
		}(chatID)
	}
}

// push 主动推送：Telegram 按 chat_id 直发，没有被动回复窗口的限制，发不出去由
// send 记日志容忍，所以恒报「已交给平台」。
func (p *Plugin) push(ctx context.Context, chatID, text string) bool {
	p.send(ctx, chatID, text, "")
	return true
}
