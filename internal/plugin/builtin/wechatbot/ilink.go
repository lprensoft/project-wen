package wechatbot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// iLink Bot API 的 HTTP 封装。所有接口在同一基址下（扫码用公共基址，绑定后用
// 服务端下发的专属 baseurl），JSON over HTTP，对照官方 openclaw-weixin 包移植。

// channelVersion 随每个请求的 base_info 上报，取官方渠道包的版本号。
const channelVersion = "1.0.2"

const (
	longPollTimeout = 35 * time.Second // getupdates / 扫码状态长轮询的客户端超时
	apiTimeout      = 15 * time.Second // 普通接口超时
)

type baseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

// ---------- 报文结构（bytes 字段在 JSON 中是 base64 字符串） ----------

type textItem struct {
	Text string `json:"text"`
}

type voiceItem struct {
	Text string `json:"text,omitempty"` // 语音转文字内容（微信侧自带转写）
}

type messageItem struct {
	Type      int        `json:"type"` // 1=文本 2=图片 3=语音 4=文件 5=视频
	TextItem  *textItem  `json:"text_item,omitempty"`
	VoiceItem *voiceItem `json:"voice_item,omitempty"`
}

// 消息发起方与状态。
const (
	msgTypeUser = 1 // 用户发出
	msgTypeBot  = 2 // Bot 发出
	stateFinish = 2 // 完整消息（非流式）
)

type wxMessage struct {
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	ItemList     []messageItem `json:"item_list,omitempty"`
}

type getUpdatesResp struct {
	Ret                  int         `json:"ret"`
	Errcode              int         `json:"errcode"`
	Errmsg               string      `json:"errmsg"`
	Msgs                 []wxMessage `json:"msgs"`
	GetUpdatesBuf        string      `json:"get_updates_buf"`
	LongpollingTimeoutMs int         `json:"longpolling_timeout_ms"`
}

// errcodeExpired 是服务端「会话过期」错误码，收到后暂停请求一段时间（官方行为）。
const errcodeExpired = -14

// ---------- HTTP ----------

// randomUIN 生成 X-WECHAT-UIN 头：随机 uint32 → 十进制字符串 → base64，每次请求都变。
func randomUIN() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := binary.BigEndian.Uint32(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

// apiPost 向 iLink 接口发一个 JSON POST。token 为空时不带 Authorization（扫码前）。
func apiPost(ctx context.Context, base, endpoint string, body any, token string, timeout time.Duration) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, base+endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", randomUIN())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(data), 200))
	}
	return data, nil
}

// apiGet 发一个 GET（扫码二维码与状态接口，无鉴权）。
func apiGet(ctx context.Context, base, endpoint string, headers map[string]string, timeout time.Duration) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, base+endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(data), 200))
	}
	return data, nil
}

// randomClientID 生成发送消息的 client_id。
func randomClientID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "wen-" + base64.RawURLEncoding.EncodeToString(b[:])
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
