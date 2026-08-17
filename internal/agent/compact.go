package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wen/internal/llm"
	"wen/internal/session"
)

// EstimateHistoryTokens 估算一组消息的 token 总量（粗略值，供状态展示与预算裁剪）。
func EstimateHistoryTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m)
	}
	return total
}

const compactPrompt = `你是一个对话压缩器。请将下面的对话历史压缩成一份结构化摘要，要求：
- 保留：用户的核心诉求与目标、已确认的事实与结论、重要的工具调用及其关键结果、未完成的任务与待办事项
- 丢弃：寒暄客套、重复内容、已被后续纠正的失败中间过程
- 用中文输出，条理清晰、尽量精炼，直接输出摘要本身，不要额外说明

对话历史：
`

const summaryPrefix = "以下是之前对话内容的压缩摘要：\n\n"

// Compact 将 session 历史压缩为一条摘要消息。
// 摘要生成过程通过 emit 以 delta 事件流式发布，结束时发布 done 或 error。
func (a *Agent) Compact(ctx context.Context, sessionID string, emit func(Event)) {
	provider, opts := a.snapshot()
	if err := a.compact(ctx, provider, opts, sessionID, emit); err != nil {
		emit(Event{Type: EventError, Error: err.Error()})
		return
	}
	emit(Event{Type: EventDone})
}

func (a *Agent) compact(ctx context.Context, provider llm.Provider, opts Options, sessionID string, emit func(Event)) error {
	_, history, err := a.store.Get(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if len(history) == 0 {
		return fmt.Errorf("会话为空，无需压缩")
	}

	events, err := provider.ChatStream(ctx, llm.ChatRequest{
		Model:       opts.Model,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: compactPrompt + serializeHistory(history)}},
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
		Thinking:    "off", // 压缩追求速度，不启用思考
	})
	if err != nil {
		return err
	}

	var summary strings.Builder
	for ev := range events {
		switch ev.Type {
		case llm.EventContentDelta:
			summary.WriteString(ev.Content)
			emit(Event{Type: EventDelta, Content: ev.Content})
		case llm.EventError:
			return ev.Err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(summary.String()) == "" {
		return fmt.Errorf("摘要生成失败：模型返回为空")
	}

	if err := a.store.Replace(sessionID, []session.StoredMessage{{
		Message: llm.Message{Role: llm.RoleUser, Content: summaryPrefix + summary.String()},
		Kind:    "summary",
		TS:      time.Now(),
	}}); err != nil {
		return err
	}
	// 旧的实测用量对应压缩前的上下文，清除以免误导
	_ = a.store.SetUsage(sessionID, nil)
	return nil
}

// serializeHistory 把消息序列化为可读文本，工具结果做截断避免超长。
func serializeHistory(msgs []session.StoredMessage) string {
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
				fmt.Fprintf(&b, "[助手调用工具 %s，参数 %s]\n", tc.Name, truncateRunes(string(tc.Arguments), 200))
			}
		case llm.RoleTool:
			fmt.Fprintf(&b, "[工具结果] %s\n", truncateRunes(m.Content, 500))
		}
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(截断)"
}
