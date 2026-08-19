package larkbot

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

// tokenSource 管理 tenant_access_token：惰性获取、过期前提前刷新、并发只发一次请求。
// 请求头形式为 `Authorization: Bearer {token}`。
type tokenSource struct {
	mu        sync.Mutex
	appID     string
	secret    string
	base      string
	client    *http.Client
	token     string
	expiresAt time.Time
}

// refreshAhead 在过期前多久开始换新 token。官方 token 最长 2 小时，
// 且建议剩余不足 30 分钟就换——照办。
const refreshAhead = 30 * time.Minute

func newTokenSource(appID, secret, base string, client *http.Client) *tokenSource {
	return &tokenSource{appID: appID, secret: secret, base: base, client: client}
}

// Get 返回一个当前有效的 tenant_access_token，必要时同步刷新。
func (t *tokenSource) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expiresAt) > refreshAhead {
		return t.token, nil
	}

	body, _ := json.Marshal(map[string]string{"app_id": t.appID, "app_secret": t.secret})
	url := t.base + "/open-apis/auth/v3/tenant_access_token/internal"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 tenant_access_token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"` // 秒
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("获取 tenant_access_token 失败（HTTP %d）: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("获取 tenant_access_token 失败: code=%d %s", out.Code, out.Msg)
	}
	ttl := out.Expire
	if ttl <= 0 {
		ttl = 7200
	}
	t.token = out.TenantAccessToken
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
