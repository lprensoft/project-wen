package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenSource 管理 App Access Token：惰性获取、过期前提前刷新、并发只发一次请求。
// 请求头形式为 `Authorization: QQBot {token}`（旧的 Bot {appid}.{token} 已废弃）。
type tokenSource struct {
	mu        sync.Mutex
	appID     string
	secret    string
	tokenURL  string
	client    *http.Client
	token     string
	expiresAt time.Time
}

// refreshAhead 在过期前多久就开始换新 token：官方在到期前约 60 秒会下发新 token
// 且新旧并行有效，提前 5 分钟留足余量。
const refreshAhead = 5 * time.Minute

func newTokenSource(appID, secret string) *tokenSource {
	return &tokenSource{
		appID:    appID,
		secret:   secret,
		tokenURL: defTokenURL,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Get 返回一个当前有效的 access token，必要时同步刷新。
func (t *tokenSource) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expiresAt) > refreshAhead {
		return t.token, nil
	}
	body, _ := json.Marshal(map[string]string{"appId": t.appID, "clientSecret": t.secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 access token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		// 官方接口这里返回的是字符串形式的数字
		ExpiresIn json.Number `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("获取 access token 失败（HTTP %d）: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	ttl, _ := out.ExpiresIn.Int64()
	if ttl <= 0 {
		ttl = 7200
	}
	t.token = out.AccessToken
	t.expiresAt = time.Now().Add(time.Duration(ttl) * time.Second)
	return t.token, nil
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
