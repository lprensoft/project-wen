package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"wen/internal/llm"
	"wen/internal/plugin"
)

// extractPrompt 让模型从即将被删除的历史里挑出值得长期保留的结论。
// 刻意要求"没有就返回空数组"——宁可不记，也不要为了凑数造出噪音记忆。
const extractPrompt = `请从下面的对话历史中挑出**值得长期记住**的结论，供之后的对话使用。

只挑这四类：
- 偏好：对方明确表达的、长期有效的工作方式或表达方式要求
- 约定：项目或协作上确定下来的规则
- 事实：环境、配置、结构等不易重新得知的信息
- 踩坑：已经验证过的失败原因与正确做法

不要挑：一次性的任务细节、临时中间结果、可以随时重新读取的内容、尚未确认的猜测。
宁可少挑也不要凑数；确实没有值得保留的内容时返回空数组 []。

已经保存过的记忆（标题）如下，不要重复挑出同样的内容：
%s

按下面的 JSON 数组格式输出，不要输出任何其它文字：
[{"name":"简短标题","description":"一句话摘要","type":"偏好|约定|事实|踩坑","content":"完整内容"}]

对话历史：
%s`

// candidate 是模型返回的一条待保存记忆。
type extracted struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Content     string `json:"content"`
}

// extractMemories 用一次独立的模型调用从历史中提炼记忆并保存，返回实际新增的条目。
// 已存在同名记忆时跳过而不是覆盖：压缩摘要里的内容可能已经是残缺副本，覆盖会让原文退化。
func (p *Plugin) extractMemories(ctx context.Context, s settings, complete plugin.CompleteFunc, history []llm.Message) ([]Entry, error) {
	store := p.writeStore(ctx)
	if store == nil {
		return nil, errNotReady
	}
	// 已存在的标题只取本轮可读范围：跨域取的话，不可读域的标题会莫名压掉
	// 本域一条合法的保存
	existing, err := p.visibleEntries(ctx)
	if err != nil {
		return nil, err
	}
	titles := "（暂无）"
	if len(existing) > 0 {
		names := make([]string, 0, len(existing))
		for _, e := range existing {
			names = append(names, e.Type+"/"+e.Name)
		}
		titles = strings.Join(names, "\n")
	}

	raw, err := complete(ctx, fmt.Sprintf(extractPrompt, titles, serializeHistory(history, s.maxExtractBytes)))
	if err != nil {
		return nil, err
	}
	items, err := parseExtracted(raw)
	if err != nil {
		return nil, err
	}
	if len(items) > s.maxExtract {
		items = items[:s.maxExtract]
	}

	var saved []Entry
	for _, it := range items {
		if strings.TrimSpace(it.Name) == "" || strings.TrimSpace(it.Content) == "" {
			continue
		}
		if _, ok := findEntry(existing, it.Name); ok {
			continue // 已有同名记忆，不覆盖
		}
		e, err := store.Save(Entry{
			Name:        it.Name,
			Description: it.Description,
			Type:        it.Type,
			Content:     it.Content,
		}, false)
		if err != nil {
			continue // 单条非法（如分类取值不对）不影响其余
		}
		saved = append(saved, e)
	}
	return saved, nil
}

// parseExtracted 解析模型输出。模型常把 JSON 包在 ``` 围栏里或前后带说明文字，
// 因此先剥围栏、再退回到截取最外层的方括号。
func parseExtracted(raw string) ([]extracted, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:] // 跳过 ```json 这类语言标记
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	if start := strings.IndexByte(s, '['); start >= 0 {
		if end := strings.LastIndexByte(s, ']'); end > start {
			s = s[start : end+1]
		}
	}

	var items []extracted
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, fmt.Errorf("提炼结果不是合法的 JSON 数组: %w", err)
	}
	return items, nil
}

// serializeHistory 把历史压成便于提炼的纯文本。工具结果与思考过程一律截断——
// 提炼要的是结论，长篇的中间过程只会稀释信号并推高这次调用的成本。
func serializeHistory(msgs []llm.Message, maxBytes int) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			fmt.Fprintf(&b, "用户: %s\n", m.Content)
		case llm.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "助手: %s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "[调用工具 %s %s]\n", tc.Name, truncateRunes(string(tc.Arguments), 160))
			}
		case llm.RoleTool:
			fmt.Fprintf(&b, "[工具结果] %s\n", truncateRunes(m.Content, 300))
		}
	}
	out, truncated := plugin.TruncateUTF8([]byte(b.String()), maxBytes)
	res := string(out)
	if truncated {
		res += "\n...[历史过长已截断]"
	}
	return res
}
