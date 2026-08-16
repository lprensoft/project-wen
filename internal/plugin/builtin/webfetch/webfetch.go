// Package webfetch 提供抓取网页文本内容的系统插件。
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"wen/internal/plugin"
)

const defaultMaxBytes = 64 * 1024

// Plugin 是 web_fetch 系统插件（注入一句提示词，演示注入链路）。
type Plugin struct {
	timeout  time.Duration
	maxBytes int
	client   *http.Client
}

func New() *Plugin {
	return &Plugin{timeout: 20 * time.Second, maxBytes: defaultMaxBytes}
}

func (p *Plugin) Name() string        { return "web_fetch" }
func (p *Plugin) Description() string { return "抓取网页并返回其文本内容" }

func (p *Plugin) SystemPrompt() string {
	return "你可以使用 fetch_url 工具抓取网页的文本内容。"
}

func (p *Plugin) Init(_ plugin.InitContext, cfg map[string]any) error {
	p.timeout = time.Duration(plugin.CfgInt(cfg, "timeout_seconds", 20)) * time.Second
	p.maxBytes = plugin.CfgInt(cfg, "max_bytes", defaultMaxBytes)
	p.client = &http.Client{Timeout: p.timeout}
	return nil
}

func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{&tool{p: p}} }

type tool struct {
	p *Plugin
}

func (t *tool) Name() string { return "fetch_url" }

func (t *tool) Description() string {
	return "抓取指定 URL 的网页并返回其文本内容（HTML 会剥离标签）。仅支持 http/https，内容过长会被截断。"
}

func (t *tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "要抓取的网页地址（http/https）"}
		},
		"required": ["url"]
	}`)
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("仅支持 http/https 地址")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "wen-agent/1.0")
	req.Header.Set("Accept", "text/html, text/plain;q=0.9, */*;q=0.5")

	client := t.p.client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 多读 1 字节以判断是否超限
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.p.maxBytes)+1))
	if err != nil {
		return "", err
	}
	raw = plugin.NormalizeEncoding(raw)
	raw, truncated := plugin.TruncateUTF8(raw, t.p.maxBytes)

	text := string(raw)
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "html") || looksLikeHTML(text) {
		text = stripHTML(text)
	}

	out := fmt.Sprintf("[HTTP %d] %s\n\n%s", resp.StatusCode, a.URL, strings.TrimSpace(text))
	if truncated {
		out += fmt.Sprintf("\n...[内容超过 %d 字节，已截断]", t.p.maxBytes)
	}
	return out, nil
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(s[:min(512, len(s))])
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript)\b.*?</\s*(script|style|noscript)\s*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
	reSpaces = regexp.MustCompile(`[ \t]{2,}`)
)

// stripHTML 粗剥 HTML：去掉 script/style/标签，压缩空白，保留可读文本。
func stripHTML(s string) string {
	s = reScript.ReplaceAllString(s, " ")
	// 常见块级标签换成换行，避免文本黏连
	s = regexp.MustCompile(`(?i)</\s*(p|div|li|h[1-6]|tr|br)\s*>|<br\s*/?>`).ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	s = reSpaces.ReplaceAllString(s, " ")
	// 去掉每行首尾空白后压缩连续空行
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, "\n")
	return reBlank.ReplaceAllString(s, "\n\n")
}
