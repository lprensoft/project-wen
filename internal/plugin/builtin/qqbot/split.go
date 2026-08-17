package qqbot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// chunkLimit 是单条消息的 rune 上限（QQ 上限约 5000，留足余量）。
const chunkLimit = 3500

// mdOffTTL 是「该用户不支持 markdown」判定的有效期，过期后重新探测。
const mdOffTTL = 6 * time.Hour

// send 把文本发到 QQ：默认按原生 markdown（msg_type=2，单聊已对所有机器人开放），
// 发送前过方言降级管线；收到「不允许发送原生 markdown」(40034012) 时按用户记忆
// 降级为纯文本重发，消息不丢。每段独立判定被动回复额度，超限降级为主动消息。
func (p *Plugin) send(ctx context.Context, openid, text, replyMsgID string) {
	p.mu.Lock()
	limiter, format := p.limiter, p.format
	p.mu.Unlock()

	useMD := format != formatPlain && p.markdownAllowed(openid)
	text = sanitizeForQQ(text)

	var chunks []string
	if useMD {
		chunks = chunkMarkdown(text, chunkLimit)
	} else {
		chunks = chunkMarkdown(toPlainText(text), chunkLimit)
	}

	total := len(chunks)
	for i, chunk := range chunks {
		if total > 1 {
			chunk = fmt.Sprintf("(%d/%d) %s", i+1, total, chunk)
		}
		msgID, seq := "", 0
		if ok, s := limiter.next(replyMsgID); ok {
			msgID, seq = replyMsgID, s
		}

		var err error
		if useMD {
			err = p.sendC2CMarkdown(ctx, openid, chunk, msgID, seq)
			if err != nil && isMarkdownForbidden(err) {
				// 平台按机器人/用户粒度拒绝原生 markdown：记住并当场降级纯文本重发
				p.disableMarkdown(openid)
				useMD = false
				log.Printf("qq_bot: 平台拒绝原生 markdown，对 %s 降级为纯文本（%v 后重新探测）", openid, mdOffTTL)
				err = p.sendC2C(ctx, openid, toPlainText(chunk), msgID, seq)
			}
		} else {
			err = p.sendC2C(ctx, openid, chunk, msgID, seq)
		}
		if err != nil {
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

// isMarkdownForbidden 判定「不允许发送原生 markdown」类错误（错误码 40034012）。
func isMarkdownForbidden(err error) bool {
	s := err.Error()
	return strings.Contains(s, "40034012") || strings.Contains(s, "markdown")
}

// markdownAllowed 查询该用户的 markdown 能力缓存（默认允许，被拒后在 TTL 内关闭）。
func (p *Plugin) markdownAllowed(openid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.mdOff[openid]
	if !ok {
		return true
	}
	if time.Now().After(until) {
		delete(p.mdOff, openid)
		return true
	}
	return false
}

func (p *Plugin) disableMarkdown(openid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mdOff[openid] = time.Now().Add(mdOffTTL)
}

// chunkMarkdown 按行把文本切成不超过 limit 个 rune 的分段，且保证 markdown 结构完整：
// 代码块中途要断开时先补 ``` 闭合、下一段以原围栏行重开（否则第二段整体变成代码/
// 代码整体变成正文）；超长单行硬切（不能整体保留，平台会拒收超限消息）。
func chunkMarkdown(s string, limit int) []string {
	var chunks []string
	var cur []string
	curLen := 0
	inCode := false
	fence := "```"

	flush := func() {
		if len(cur) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(cur, "\n"))
		if text != "" {
			chunks = append(chunks, text)
		}
		cur, curLen = nil, 0
	}
	appendLine := func(line string) {
		cur = append(cur, line)
		curLen += len([]rune(line)) + 1
	}

	for _, line := range strings.Split(s, "\n") {
		lineRunes := []rune(line)
		// 超长单行：先冲掉当前段，再按 limit 硬切成独立段
		if len(lineRunes) > limit {
			if inCode {
				appendLine(fence)
			}
			flush()
			for i := 0; i < len(lineRunes); i += limit {
				end := min(i+limit, len(lineRunes))
				chunks = append(chunks, string(lineRunes[i:end]))
			}
			if inCode {
				appendLine(fence)
			}
			continue
		}
		if curLen+len(lineRunes)+1 > limit {
			if inCode {
				appendLine("```") // 闭合本段的代码块
				flush()
				appendLine(fence) // 下一段重开同款围栏
			} else {
				flush()
			}
		}
		appendLine(line)
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if !inCode {
				fence = strings.TrimSpace(line)
			}
			inCode = !inCode
		}
	}
	flush()
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	return chunks
}
