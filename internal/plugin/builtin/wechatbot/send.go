package wechatbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"wen/internal/mdtext"
)

// send 把文本发到微信：微信聊天不渲染 markdown，先转纯文本再按上限分段逐条发送。
// 每条消息必须回带入站消息的 context_token，否则不会关联到正确的对话窗口。
// 发送失败记日志不重试（官方可能限流，重试只会雪上加霜）。
func (p *Plugin) send(ctx context.Context, userID, text, contextToken string) {
	p.mu.Lock()
	creds := p.creds
	p.mu.Unlock()
	if !creds.valid() {
		log.Printf("wechat_bot: 未绑定，丢弃待发消息")
		return
	}
	if contextToken == "" {
		log.Printf("wechat_bot: 缺少 context_token，无法回复 %s", userID)
		return
	}

	chunks := mdtext.ChunkMarkdown(mdtext.ToPlainText(text), chunkLimit)
	total := len(chunks)
	for i, chunk := range chunks {
		if total > 1 {
			chunk = fmt.Sprintf("(%d/%d) %s", i+1, total, chunk)
		}
		if err := p.sendMessage(ctx, creds, userID, chunk, contextToken); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("wechat_bot: 发送失败: %v", err)
		}
	}
}

// sendMessage 发送单条文本消息（message_state=FINISH，非流式）。
func (p *Plugin) sendMessage(ctx context.Context, creds credentials, userID, text, contextToken string) error {
	body := map[string]any{
		"msg": wxMessage{
			ToUserID:     userID,
			ClientID:     randomClientID(),
			MessageType:  msgTypeBot,
			MessageState: stateFinish,
			ContextToken: contextToken,
			ItemList:     []messageItem{{Type: 1, TextItem: &textItem{Text: text}}},
		},
		"base_info": baseInfo{ChannelVersion: channelVersion},
	}
	raw, err := apiPost(ctx, creds.BaseURL, "/ilink/bot/sendmessage", body, creds.BotToken, apiTimeout)
	if err != nil {
		return err
	}
	// 响应体通常为空对象；带 ret/errcode 的失败也要暴露出来
	var resp struct {
		Ret     int    `json:"ret"`
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &resp); err == nil && (resp.Ret != 0 || resp.Errcode != 0) {
		return fmt.Errorf("sendmessage: ret=%d errcode=%d %s", resp.Ret, resp.Errcode, resp.Errmsg)
	}
	return nil
}
