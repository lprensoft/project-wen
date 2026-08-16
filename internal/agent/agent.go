package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
	"wen/internal/session"
)

type EventType string

const (
	EventDelta        EventType = "delta"         // 助手文本增量
	EventThinking     EventType = "thinking"      // 思考内容增量
	EventToolStart    EventType = "tool_start"    // 开始执行工具
	EventToolResult   EventType = "tool_result"   // 工具执行结果
	EventCompactStart EventType = "compact_start" // 自动压缩开始
	EventCompactDelta EventType = "compact_delta" // 自动压缩摘要增量
	EventCompactDone  EventType = "compact_done"  // 自动压缩结束（Error 非空表示失败）
	EventDone         EventType = "done"          // 本轮请求结束
	EventError        EventType = "error"
)

// autoCompactThreshold 会话上下文占用达到窗口的该比例时触发自动压缩。
const autoCompactThreshold = 0.90

// Event 是 Agent 运行过程中对外发布的事件（server 将其转为 SSE 帧）。
type Event struct {
	Type       EventType       `json:"type"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolArgs   json.RawMessage `json:"tool_args,omitempty"`
	ToolResult string          `json:"tool_result,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type Options struct {
	Model         string
	Temperature   float64
	MaxTokens     int
	SystemPrompt  string
	MaxTurns      int
	Workdir       string // 工具的工作目录，写入环境块
	Thinking      string // 思考模式：off 或强度档位
	ContextLength int    // 模型上下文窗口（token），超出预算时裁剪最旧轮次
}

type Agent struct {
	provider llm.Provider
	plugins  *plugin.Manager
	store    *session.Store
	opts     Options
}

func New(provider llm.Provider, plugins *plugin.Manager, store *session.Store, opts Options) *Agent {
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = 20
	}
	return &Agent{provider: provider, plugins: plugins, store: store, opts: opts}
}

const titleMaxRunes = 30

// Run 处理一条用户消息：写入 session、驱动工具循环、通过 emit 实时发布事件。
// 返回前一定会 emit 一个 done 或 error 事件。
func (a *Agent) Run(ctx context.Context, sessionID, userInput string, emit func(Event)) {
	if err := a.run(ctx, sessionID, userInput, emit); err != nil {
		emit(Event{Type: EventError, Error: err.Error()})
		return
	}
	emit(Event{Type: EventDone})
}

func (a *Agent) run(ctx context.Context, sessionID, userInput string, emit func(Event)) error {
	meta, history, err := a.store.Get(sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}

	// 首条用户消息生成标题
	if meta.Title == "" {
		title := []rune(userInput)
		if len(title) > titleMaxRunes {
			title = title[:titleMaxRunes]
		}
		_ = a.store.SetTitle(sessionID, string(title))
	}

	userMsg := llm.Message{Role: llm.RoleUser, Content: userInput}
	if err := a.append(sessionID, userMsg); err != nil {
		return err
	}

	// 组装上下文：system（环境块 + 启用插件的提示词片段 + 可选配置提示词）+ 历史 + 本条
	msgs := make([]llm.Message, 0, len(history)+2)
	parts := []string{envContext(a.opts.Workdir)}
	parts = append(parts, a.plugins.SystemPrompts()...)
	if a.opts.SystemPrompt != "" {
		parts = append(parts, a.opts.SystemPrompt)
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: strings.Join(parts, "\n\n")})
	for _, h := range history {
		msgs = append(msgs, h.Message)
	}
	msgs = append(msgs, userMsg)

	for turn := 0; turn < a.opts.MaxTurns; turn++ {
		content, reasoning, toolCalls, usage, err := a.stream(ctx, msgs, emit)
		if err != nil {
			return err
		}
		if usage != nil {
			// 记录实测用量（供 /status 展示与自动压缩判断）
			_ = a.store.SetUsage(sessionID, &session.Usage{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
			})
		}

		// reasoning 一并持久化并留在上下文里：带工具调用时 DeepSeek 要求回传
		assistant := llm.Message{Role: llm.RoleAssistant, Content: content, Reasoning: reasoning, ToolCalls: toolCalls}
		if err := a.append(sessionID, assistant); err != nil {
			return err
		}
		msgs = append(msgs, assistant)

		if len(toolCalls) == 0 {
			a.maybeAutoCompact(ctx, sessionID, usage, emit)
			return nil // 无工具调用，本轮结束
		}

		for _, call := range toolCalls {
			emit(Event{Type: EventToolStart, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Arguments})
			result := a.execute(ctx, call)
			emit(Event{Type: EventToolResult, ToolCallID: call.ID, ToolName: call.Name, ToolResult: result})

			toolMsg := llm.Message{Role: llm.RoleTool, Content: result, ToolCallID: call.ID}
			if err := a.append(sessionID, toolMsg); err != nil {
				return err
			}
			msgs = append(msgs, toolMsg)
		}
	}
	return fmt.Errorf("reached max_turns (%d) without a final answer", a.opts.MaxTurns)
}

// stream 发起一次 LLM 调用，转发文本与思考增量，返回完整文本、思考内容、工具调用与实测用量。
func (a *Agent) stream(ctx context.Context, msgs []llm.Message, emit func(Event)) (string, string, []llm.ToolCall, *llm.Usage, error) {
	events, err := a.provider.ChatStream(ctx, llm.ChatRequest{
		Model:       a.opts.Model,
		Messages:    a.trimToBudget(msgs),
		Tools:       a.toolSpecs(),
		Temperature: a.opts.Temperature,
		MaxTokens:   a.opts.MaxTokens,
		Thinking:    a.opts.Thinking,
	})
	if err != nil {
		return "", "", nil, nil, err
	}

	var content, reasoning string
	var toolCalls []llm.ToolCall
	var usage *llm.Usage
	for ev := range events {
		switch ev.Type {
		case llm.EventContentDelta:
			content += ev.Content
			emit(Event{Type: EventDelta, Content: ev.Content})
		case llm.EventReasoningDelta:
			reasoning += ev.Content
			emit(Event{Type: EventThinking, Content: ev.Content})
		case llm.EventToolCalls:
			toolCalls = ev.ToolCalls
		case llm.EventUsage:
			usage = ev.Usage
		case llm.EventError:
			return "", "", nil, nil, ev.Err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", "", nil, nil, err
	}
	return content, reasoning, toolCalls, usage, nil
}

// maybeAutoCompact 在实测上下文占用达到窗口阈值时自动压缩会话。
func (a *Agent) maybeAutoCompact(ctx context.Context, sessionID string, usage *llm.Usage, emit func(Event)) {
	if usage == nil || a.opts.ContextLength <= 0 {
		return
	}
	used := usage.PromptTokens + usage.CompletionTokens
	if float64(used) < float64(a.opts.ContextLength)*autoCompactThreshold {
		return
	}
	emit(Event{Type: EventCompactStart})
	wrapped := func(ev Event) {
		if ev.Type == EventDelta {
			emit(Event{Type: EventCompactDelta, Content: ev.Content})
		}
	}
	if err := a.compact(ctx, sessionID, wrapped); err != nil {
		emit(Event{Type: EventCompactDone, Error: err.Error()})
		return
	}
	emit(Event{Type: EventCompactDone})
}

// estimateTokens 粗略估算消息 token 数：UTF-8 字节数 / 3
//（中文约 1 字 1 token，对英文略偏高，作为安全裕量）。
func estimateTokens(m llm.Message) int {
	n := len(m.Content) + len(m.Reasoning)
	for _, tc := range m.ToolCalls {
		n += len(tc.Name) + len(tc.Arguments)
	}
	return n/3 + 8
}

// trimToBudget 在超出上下文预算时，从最旧的对话轮次（以 user 消息开头）开始整轮丢弃，
// 始终保留 system 消息与最近的轮次，保证 tool_calls 与 tool 结果不会被拆散。
func (a *Agent) trimToBudget(msgs []llm.Message) []llm.Message {
	budget := a.opts.ContextLength - a.opts.MaxTokens - 2048 // 预留输出与协议开销
	if budget <= 0 {
		return msgs
	}
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m)
	}
	for total > budget && len(msgs) > 2 {
		// msgs[0] 为 system；找到下一轮的起点（msgs[1] 之后的第一条 user）
		next := -1
		for i := 2; i < len(msgs); i++ {
			if msgs[i].Role == llm.RoleUser {
				next = i
				break
			}
		}
		if next == -1 {
			break // 只剩当前轮，不能再裁
		}
		for _, m := range msgs[1:next] {
			total -= estimateTokens(m)
		}
		msgs = append(msgs[:1:1], msgs[next:]...)
	}
	return msgs
}

// toolSpecs 每次请求时从启用插件动态生成工具声明（运行时开关天然生效）。
func (a *Agent) toolSpecs() []llm.ToolSpec {
	enabled := a.plugins.EnabledTools()
	specs := make([]llm.ToolSpec, 0, len(enabled))
	for _, t := range enabled {
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return specs
}

func (a *Agent) execute(ctx context.Context, call llm.ToolCall) string {
	tool, ok := a.plugins.FindTool(call.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", call.Name)
	}
	result, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		// 错误也作为结果反馈给模型，让其决定下一步
		return fmt.Sprintf("error: %v", err)
	}
	if result == "" {
		result = "(empty output)"
	}
	return result
}

func (a *Agent) append(sessionID string, msg llm.Message) error {
	return a.store.Append(sessionID, session.StoredMessage{Message: msg, TS: time.Now()})
}
