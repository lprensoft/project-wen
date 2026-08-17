package qqbot

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// chunkRunes 是单条 QQ 消息的长度上限（经验值，官方对超长文本会拒绝）。
const chunkRunes = 1500

// send 把文本分段发到 QQ。每段独立判定被动回复额度：还有额度就挂在触发消息上
//（附递增 msg_seq），超窗或超次降级为主动消息；主动消息受官方策略限制，
// 失败只记日志——这是平台约束，不是可修复的错误。
func (p *Plugin) send(ctx context.Context, openid, text, replyMsgID string) {
	p.mu.Lock()
	limiter := p.limiter
	p.mu.Unlock()

	for _, chunk := range splitText(text, chunkRunes) {
		msgID, seq := "", 0
		if ok, s := limiter.next(replyMsgID); ok {
			msgID, seq = replyMsgID, s
		}
		if err := p.sendC2C(ctx, openid, chunk, msgID, seq); err != nil {
			if ctx.Err() != nil {
				return
			}
			kind := "被动回复"
			if msgID == "" {
				kind = "主动消息（官方策略下可能受限）"
			}
			log.Printf("qq_bot: %s发送失败: %v", kind, err)
		}
	}
}

// splitText 把长文本按段落边界优先切成不超过 max 个 rune 的分段，多段时加 (i/n) 标注。
func splitText(s string, max int) []string {
	runes := []rune(s)
	if len(runes) <= max {
		return []string{s}
	}
	var parts []string
	for len(runes) > 0 {
		n := min(max, len(runes))
		cut := n
		if n < len(runes) {
			// 尽量在换行处断开，避免劈开句子
			for i := n; i > n/2; i-- {
				if runes[i-1] == '\n' {
					cut = i
					break
				}
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, fmt.Sprintf("(%d/%d) %s", i+1, len(parts), part))
	}
	return out
}
