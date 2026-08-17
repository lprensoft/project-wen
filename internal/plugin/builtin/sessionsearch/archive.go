package sessionsearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
)

// OnCompact 在会话历史被摘要替换之前把原始历史原样归档。
//
// 自动压缩是无人值守触发的，且会物理删除整段历史；归档是这些原文唯一的挽回途径，
// 也让 search_sessions 能覆盖已经被压缩过的会话——否则那些会话只剩一条摘要，
// 恰好是最值得回查的部分成了盲区。
//
// 这里只做文件写入，不发起模型调用；从历史中提炼长期记忆是 memory 插件订阅同一个
// 钩子完成的事。
func (p *Plugin) OnCompact(_ context.Context, ev plugin.CompactEvent) (string, error) {
	s := p.snapshot()
	if s.archiveDir == "" || s.maxArchives <= 0 || len(ev.History) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(s.archiveDir, 0o755); err != nil {
		return "", fmt.Errorf("创建归档目录失败: %w", err)
	}

	name := fmt.Sprintf("%s-%s.md", sanitizeID(ev.SessionID), time.Now().Format("20060102-150405"))
	path := filepath.Join(s.archiveDir, name)
	raw := []byte(renderArchive(ev))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return "", fmt.Errorf("写入归档失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("写入归档失败: %w", err)
	}
	pruneArchives(s.archiveDir, s.maxArchives)

	return fmt.Sprintf("（本次压缩前的完整历史已归档：%s）", name), nil
}

// sanitizeID 只保留字母数字与连字符下划线，避免异常的会话 id 影响归档文件名。
func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "session"
	}
	return out
}

// renderArchive 把历史渲染成可读的 Markdown。与压缩用的序列化不同，
// 这里不做任何截断——归档的意义就在于保住原文。
func renderArchive(ev plugin.CompactEvent) string {
	var b strings.Builder
	b.WriteString("# 压缩归档\n\n")
	fmt.Fprintf(&b, "- 会话：%s\n", ev.SessionID)
	fmt.Fprintf(&b, "- 归档时间：%s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 消息数：%d\n\n", len(ev.History))

	b.WriteString("## 摘要\n\n")
	b.WriteString(ev.Summary)
	b.WriteString("\n\n## 原始历史\n\n")

	for _, m := range ev.History {
		switch m.Role {
		case llm.RoleUser:
			b.WriteString("### 用户\n\n")
			b.WriteString(m.Content)
			b.WriteString("\n\n")
		case llm.RoleAssistant:
			b.WriteString("### 助手\n\n")
			if m.Reasoning != "" {
				b.WriteString("<思考>\n")
				b.WriteString(m.Reasoning)
				b.WriteString("\n</思考>\n\n")
			}
			if m.Content != "" {
				b.WriteString(m.Content)
				b.WriteString("\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "调用工具 `%s`：\n\n```json\n%s\n```\n\n", tc.Name, string(tc.Arguments))
			}
		case llm.RoleTool:
			b.WriteString("### 工具结果\n\n```\n")
			b.WriteString(m.Content)
			b.WriteString("\n```\n\n")
		case llm.RoleSystem:
			b.WriteString("### 系统\n\n")
			b.WriteString(m.Content)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// pruneArchives 只保留最近的 max 个归档。
// 按修改时间而不是文件名排序：文件名以会话 id 打头，字典序会先按会话分组，不是时间序。
func pruneArchives(dir string, max int) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		items = append(items, item{de.Name(), fi.ModTime()})
	}
	if len(items) <= max {
		return
	}
	slices.SortFunc(items, func(a, b item) int { return a.mod.Compare(b.mod) })
	for _, it := range items[:len(items)-max] {
		_ = os.Remove(filepath.Join(dir, it.name))
	}
}

// archiveHit 是归档文件中的一处命中。
type archiveHit struct {
	name    string
	mod     time.Time
	matches []string
}

// searchArchives 在归档文件里做包含匹配，返回命中的文件与摘录。
func searchArchives(dir, keyword string, from, to time.Time, maxSnippets int) ([]archiveHit, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	kw := strings.ToLower(strings.TrimSpace(keyword))

	var hits []archiveHit
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		// 归档写入时间即其内容的时间下界，可据此按日期筛选
		if !inRange(fi.ModTime(), from, to) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		text := string(raw)
		if kw != "" && !strings.Contains(strings.ToLower(text), kw) {
			continue
		}
		hit := archiveHit{name: de.Name(), mod: fi.ModTime()}
		if kw != "" {
			for line := range strings.SplitSeq(text, "\n") {
				if strings.Contains(strings.ToLower(line), kw) {
					hit.matches = append(hit.matches, strings.TrimSpace(line))
					if len(hit.matches) >= maxSnippets {
						break
					}
				}
			}
		}
		hits = append(hits, hit)
	}
	slices.SortFunc(hits, func(a, b archiveHit) int { return b.mod.Compare(a.mod) })
	return hits, nil
}
