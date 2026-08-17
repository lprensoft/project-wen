package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
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

// messagesOf 剥掉存储层的附加字段，只留下消息本身。
func messagesOf(stored []session.StoredMessage) []llm.Message {
	out := make([]llm.Message, 0, len(stored))
	for _, m := range stored {
		out = append(out, m.Message)
	}
	return out
}

const compactPrompt = `你是一个对话压缩器。请将下面的对话历史压缩成一份结构化摘要，要求：
- 保留：用户的核心诉求与目标、已确认的事实与结论、重要的工具调用及其关键结果、未完成的任务与待办事项
- 丢弃：寒暄客套、重复内容、已被后续纠正的失败中间过程
- 用中文输出，条理清晰、尽量精炼，直接输出摘要本身，不要额外说明

对话历史：
`

const summaryPrefix = "以下是之前对话内容的压缩摘要：\n\n"

// minSummarizeTokens 以下的分组不单独调模型生成摘要，原文原样保留。
// 一两轮对话的摘要信息量不会超过原文，却要付一次真实的模型调用。
const minSummarizeTokens = 1500

// groupSeparator 在流式播放多组摘要时插入，避免它们在界面上糊成一段。
const groupSeparator = "\n\n———\n\n"

// Compact 将 session 历史压缩为摘要消息。
// 摘要生成过程通过 emit 以 delta 事件流式发布，结束时发布 done 或 error。
// 与对话轮次共用轮次锁：压缩的 Replace 与并发轮次的 Append 竞态会丢消息。
func (a *Agent) Compact(ctx context.Context, sessionID string, emit func(Event)) {
	l := a.turnLock(sessionID)
	l.Lock()
	defer l.Unlock()
	provider, opts := a.snapshot()
	if err := a.compact(ctx, provider, opts, sessionID, emit); err != nil {
		emit(Event{Type: EventError, Error: err.Error()})
		return
	}
	emit(Event{Type: EventDone})
}

// CompactTurn 供插件以编程方式压缩会话（经 InitContext.Compact 暴露）。
// 与 RunTurn 相同的不排队语义：会话忙时立即返回 plugin.ErrSessionBusy。
func (a *Agent) CompactTurn(ctx context.Context, sessionID string) error {
	l := a.turnLock(sessionID)
	if !l.TryLock() {
		return plugin.ErrSessionBusy
	}
	defer l.Unlock()
	provider, opts := a.snapshot()
	return a.compact(ctx, provider, opts, sessionID, func(Event) {})
}

// scopeGroup 是历史中属于同一可见域的一段消息。
type scopeGroup struct {
	tag  string
	msgs []session.StoredMessage
	est  int // 该组的估算 token 量
}

// groupByTag 按可见域标签分组，组内保持原顺序。
//
// 组序按标签「最后一次出现」升序：当前活跃的可见域因此排在最后，压缩之后
// 「最后一条带标签消息」仍然指向它，依靠历史推导可见域状态的插件不会因压缩而反转。
func groupByTag(history []session.StoredMessage) []scopeGroup {
	idx := map[string]int{} // 标签 -> 在 groups 中的位置
	var groups []scopeGroup
	lastSeen := map[string]int{}
	for i, m := range history {
		g, ok := idx[m.Tag]
		if !ok {
			idx[m.Tag] = len(groups)
			g = len(groups)
			groups = append(groups, scopeGroup{tag: m.Tag})
		}
		groups[g].msgs = append(groups[g].msgs, m)
		groups[g].est += estimateTokens(m.Message)
		lastSeen[m.Tag] = i
	}
	// 按最后出现位置排序
	for i := 1; i < len(groups); i++ {
		for j := i; j > 0 && lastSeen[groups[j-1].tag] > lastSeen[groups[j].tag]; j-- {
			groups[j-1], groups[j] = groups[j], groups[j-1]
		}
	}
	return groups
}

// largestGroup 返回估算 token 最多的分组下标。
func largestGroup(groups []scopeGroup) int {
	best := 0
	for i, g := range groups {
		if g.est > groups[best].est {
			best = i
		}
	}
	return best
}

// scopeForTag 构造「只读该标签与共享内容、写入该标签」的可见域，供压缩期间的插件回调
// 使用（决定归档落哪个目录、提炼出的记忆写哪个库）。这里是收紧的：提炼某个可见域的
// 记忆时，不该把别的可见域的内容混进去。
func scopeForTag(tag string) plugin.Scope {
	if tag == "" {
		return plugin.Scope{}
	}
	return plugin.Scope{Write: tag, Read: []string{tag}}
}

func (a *Agent) compact(ctx context.Context, provider llm.Provider, opts Options, sessionID string, emit func(Event)) error {
	_, history, err := a.store.Get(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if len(history) == 0 {
		return fmt.Errorf("会话为空，无需压缩")
	}

	groups := groupByTag(history)
	// 无论体量如何，token 最多的那一组必须真的被压缩：否则上下文已超阈值却什么都没减少，
	// 自动压缩会在其后的每一轮空转重试，每次还白付一次模型调用。
	biggest := largestGroup(groups)

	out := make([]session.StoredMessage, 0, len(history))
	emitted := false
	for i, g := range groups {
		if i != biggest && g.est < minSummarizeTokens {
			// 原样保留（整组保留，tool 配对不受影响）。也不广播 OnCompact：
			// 原文没有丢失，不存在「信息即将消失」的时机。
			out = append(out, g.msgs...)
			continue
		}
		if emitted {
			emit(Event{Type: EventDelta, Content: groupSeparator})
		}
		summary, err := a.summarize(ctx, provider, opts, g.msgs, emit)
		if err != nil {
			return err
		}
		emitted = true

		// 历史即将被物理删除，先广播给插件（可借此归档），其注记随摘要一并留在会话里
		gctx := plugin.WithScope(ctx, scopeForTag(g.tag))
		notes := a.plugins.NotifyCompact(gctx, plugin.CompactEvent{
			SessionID: sessionID,
			Scope:     g.tag,
			History:   messagesOf(g.msgs),
			Summary:   summary,
		})
		content := summaryPrefix + summary
		if len(notes) > 0 {
			content += "\n\n" + strings.Join(notes, "\n")
		}
		out = append(out, session.StoredMessage{
			Message: llm.Message{Role: llm.RoleUser, Content: content},
			Kind:    session.KindSummary,
			Tag:     g.tag,
			TS:      time.Now(),
		})
	}

	if err := a.store.Replace(sessionID, out); err != nil {
		return err
	}
	// 旧的实测用量对应压缩前的上下文，清除以免误导
	_ = a.store.SetUsage(sessionID, nil)
	return nil
}

// summarize 让模型把一组消息压成一段摘要，过程以 delta 事件流式发布。
func (a *Agent) summarize(ctx context.Context, provider llm.Provider, opts Options, msgs []session.StoredMessage, emit func(Event)) (string, error) {
	events, err := provider.ChatStream(ctx, llm.ChatRequest{
		Model:       opts.Model,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: compactPrompt + serializeHistory(msgs)}},
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
		Thinking:    "off", // 压缩追求速度，不启用思考
	})
	if err != nil {
		return "", err
	}
	var summary strings.Builder
	for ev := range events {
		switch ev.Type {
		case llm.EventContentDelta:
			summary.WriteString(ev.Content)
			emit(Event{Type: EventDelta, Content: ev.Content})
		case llm.EventError:
			return "", ev.Err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(summary.String()) == "" {
		return "", fmt.Errorf("摘要生成失败：模型返回为空")
	}
	return summary.String(), nil
}

// serializeHistory 把消息序列化为可读文本，工具结果做截断避免超长。
// 一次性输入（心跳提示词等）不进摘要：它们是机器注入的模板文案，不是对话内容。
func serializeHistory(msgs []session.StoredMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Kind == session.KindEphemeral {
			continue
		}
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
