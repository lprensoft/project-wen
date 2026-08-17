package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wen/internal/session"
)

var errNotReady = fmt.Errorf("会话检索尚未就绪")

// ---------- search_sessions ----------

type searchTool struct{ p *Plugin }

func (t *searchTool) Name() string { return "search_sessions" }

func (t *searchTool) Description() string {
	return "在历史对话中检索原文，可按关键词、日期范围过滤，返回命中的会话及其摘录。" +
		"用于回头查证当时具体说了什么。检索范围同时包含现存会话与被压缩前的历史归档，" +
		"归档命中后可用 read_archive 读取全文。"
}

func (t *searchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"keyword": {"type": "string", "description": "关键词，对消息内容做包含匹配；留空则只按日期筛选"},
			"from": {"type": "string", "description": "起始日期（含），格式 YYYY-MM-DD"},
			"to": {"type": "string", "description": "结束日期（含），格式 YYYY-MM-DD"},
			"limit": {"type": "integer", "description": "最多返回多少个会话，默认 10"}
		}
	}`)
}

func (t *searchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Keyword string `json:"keyword"`
		From    string `json:"from"`
		To      string `json:"to"`
		Limit   int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	if a.Limit <= 0 {
		a.Limit = 10
	}
	from, err := parseDay(a.From, false)
	if err != nil {
		return "", err
	}
	to, err := parseDay(a.To, true)
	if err != nil {
		return "", err
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return "", fmt.Errorf("结束日期早于起始日期")
	}

	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}
	cands, err := candidates(s.dir, from, to)
	if err != nil {
		return "", fmt.Errorf("读取会话目录失败: %w", err)
	}
	// 现存会话为空也要继续查归档：被压缩过的内容只在归档里
	scanned, truncatedScan := cands, false
	if len(scanned) > s.maxScanSessions {
		scanned, truncatedScan = scanned[:s.maxScanSessions], true
	}

	kw := strings.ToLower(strings.TrimSpace(a.Keyword))
	var b strings.Builder
	hits := 0
	for _, c := range scanned {
		if hits >= a.Limit {
			break
		}
		meta, msgs, err := s.store.Get(c.id)
		if err != nil {
			continue // 会话可能正好被删除，跳过而不是让整次检索失败
		}
		var matched []session.StoredMessage
		for _, m := range msgs {
			if !inRange(m.TS, from, to) {
				continue
			}
			if kw != "" && !strings.Contains(strings.ToLower(m.Content), kw) {
				continue
			}
			matched = append(matched, m)
		}
		if len(matched) == 0 {
			continue
		}
		hits++

		title := meta.Title
		if title == "" {
			title = "(无标题)"
		}
		fmt.Fprintf(&b, "## %s ｜ %s ｜ 命中 %d 条\n", title, c.id, len(matched))
		shown := matched
		if len(shown) > s.maxSnippets {
			shown = shown[:s.maxSnippets]
		}
		for _, m := range shown {
			kind := roleLabel(m.Role)
			if m.Kind == "summary" {
				kind = "压缩摘要"
			}
			fmt.Fprintf(&b, "- [%s %s] %s\n", m.TS.Format("2006-01-02 15:04"), kind, snippet(m, a.Keyword, 160))
		}
		if len(matched) > len(shown) {
			fmt.Fprintf(&b, "- （另有 %d 条命中未列出，可用 read_session 读取该会话）\n", len(matched)-len(shown))
		}
		b.WriteString("\n")
	}

	// 被压缩过的会话只剩一条摘要，其原文在归档里——这部分不查就是盲区
	archives, err := searchArchives(s.archiveDir, a.Keyword, from, to, s.maxSnippets)
	if err == nil && len(archives) > 0 {
		b.WriteString("## 压缩归档\n")
		for _, h := range archives {
			fmt.Fprintf(&b, "- %s ｜ %s\n", h.mod.Format("2006-01-02 15:04"), h.name)
			for _, line := range h.matches {
				fmt.Fprintf(&b, "  %s\n", truncateRunes(line, 160))
			}
		}
		b.WriteString("\n")
	}

	if hits == 0 && len(archives) == 0 {
		return fmt.Sprintf("扫描了 %d 个会话，没有符合条件的内容。", len(scanned)), nil
	}
	head := fmt.Sprintf("在 %d 个会话中命中 %d 个", len(scanned), hits)
	if len(archives) > 0 {
		head += fmt.Sprintf("，另有 %d 个压缩归档命中", len(archives))
	}
	if truncatedScan {
		head += fmt.Sprintf("（仅扫描了最近 %d 个会话，更早的未读取）", s.maxScanSessions)
	}
	return clip(head+"：\n\n"+strings.TrimRight(b.String(), "\n"), s.maxBytes), nil
}

// ---------- read_archive ----------

type archiveTool struct{ p *Plugin }

func (t *archiveTool) Name() string { return "read_archive" }

func (t *archiveTool) Description() string {
	return "读取一份压缩归档的内容，即某个会话在被压缩摘要替换之前的完整原文。" +
		"归档文件名取自 search_sessions 的结果；不传文件名则列出全部归档。"
}

func (t *archiveTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "归档文件名；留空则列出全部归档"},
			"keyword": {"type": "string", "description": "只返回包含该关键词的行；留空返回全文"}
		}
	}`)
}

func (t *archiveTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name    string `json:"name"`
		Keyword string `json:"keyword"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	s := t.p.snapshot()
	if s.archiveDir == "" {
		return "", fmt.Errorf("当前没有启用压缩归档")
	}

	if strings.TrimSpace(a.Name) == "" {
		all, err := searchArchives(s.archiveDir, "", time.Time{}, time.Time{}, 0)
		if err != nil {
			return "", err
		}
		if len(all) == 0 {
			return "还没有任何压缩归档。", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 份压缩归档（按时间从新到旧）：\n", len(all))
		for _, h := range all {
			fmt.Fprintf(&b, "- %s ｜ %s\n", h.mod.Format("2006-01-02 15:04"), h.name)
		}
		return clip(strings.TrimRight(b.String(), "\n"), s.maxBytes), nil
	}

	// 归档名来自本插件自己的输出，仍要确认它没有指向目录外
	name := filepath.Base(strings.TrimSpace(a.Name))
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	raw, err := os.ReadFile(filepath.Join(s.archiveDir, name))
	if err != nil {
		return "", fmt.Errorf("没有名为 %q 的归档", a.Name)
	}
	text := string(raw)

	if kw := strings.ToLower(strings.TrimSpace(a.Keyword)); kw != "" {
		var kept []string
		for line := range strings.SplitSeq(text, "\n") {
			if strings.Contains(strings.ToLower(line), kw) {
				kept = append(kept, line)
			}
		}
		if len(kept) == 0 {
			return fmt.Sprintf("归档 %s 中没有包含 %q 的内容。", name, a.Keyword), nil
		}
		text = strings.Join(kept, "\n")
	}
	return clip(text, s.maxBytes), nil
}

// ---------- read_session ----------

type readTool struct{ p *Plugin }

func (t *readTool) Name() string { return "read_session" }

func (t *readTool) Description() string {
	return "读取指定会话的消息内容。会话 id 取自 search_sessions 的结果。可按日期范围或关键词收窄。"
}

func (t *readTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "会话 id"},
			"keyword": {"type": "string", "description": "只返回包含该关键词的消息；留空返回全部"},
			"from": {"type": "string", "description": "起始日期（含），格式 YYYY-MM-DD"},
			"to": {"type": "string", "description": "结束日期（含），格式 YYYY-MM-DD"}
		},
		"required": ["id"]
	}`)
}

func (t *readTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID      string `json:"id"`
		Keyword string `json:"keyword"`
		From    string `json:"from"`
		To      string `json:"to"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("需要提供会话 id")
	}
	from, err := parseDay(a.From, false)
	if err != nil {
		return "", err
	}
	to, err := parseDay(a.To, true)
	if err != nil {
		return "", err
	}

	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}
	if _, err := os.Stat(sessionPath(s.dir, a.ID)); err != nil {
		return "", fmt.Errorf("没有 id 为 %q 的会话", a.ID)
	}
	meta, msgs, err := s.store.Get(a.ID)
	if err != nil {
		return "", err
	}

	kw := strings.ToLower(strings.TrimSpace(a.Keyword))
	var b strings.Builder
	title := meta.Title
	if title == "" {
		title = "(无标题)"
	}
	fmt.Fprintf(&b, "# %s ｜ %s ｜ 创建于 %s ｜ 共 %d 条消息\n\n",
		title, a.ID, meta.CreatedAt.Format("2006-01-02 15:04"), len(msgs))

	shown := 0
	for _, m := range msgs {
		if !inRange(m.TS, from, to) {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(m.Content), kw) {
			continue
		}
		shown++
		kind := roleLabel(m.Role)
		if m.Kind == "summary" {
			kind = "压缩摘要"
		}
		fmt.Fprintf(&b, "### %s %s\n\n", m.TS.Format("2006-01-02 15:04"), kind)
		if m.Content != "" {
			b.WriteString(m.Content)
			b.WriteString("\n\n")
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "调用工具 `%s`：%s\n\n", tc.Name, truncateRunes(string(tc.Arguments), 200))
		}
	}
	if shown == 0 {
		return fmt.Sprintf("会话 %s 共 %d 条消息，没有符合条件的。", a.ID, len(msgs)), nil
	}
	return clip(strings.TrimRight(b.String(), "\n"), s.maxBytes), nil
}
