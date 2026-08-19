package telegrambot

import (
	"context"
	"strings"

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
