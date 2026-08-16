package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"wen/internal/agent/tools"
	"wen/internal/llm"
	"wen/internal/session"
)

// mockProvider 按预设脚本依次响应每轮调用，并记录收到的请求。
type mockProvider struct {
	turns []mockTurn
	calls int
	reqs  []llm.ChatRequest
}

type mockTurn struct {
	content   string
	reasoning string
	toolCalls []llm.ToolCall
}

func (m *mockProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	turn := m.turns[m.calls]
	m.calls++
	m.reqs = append(m.reqs, req)
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, r := range turn.reasoning {
			ch <- llm.StreamEvent{Type: llm.EventReasoningDelta, Content: string(r)}
		}
		// 文本按字符流式下发，模拟真实增量
		for _, r := range turn.content {
			ch <- llm.StreamEvent{Type: llm.EventContentDelta, Content: string(r)}
		}
		if len(turn.toolCalls) > 0 {
			ch <- llm.StreamEvent{Type: llm.EventToolCalls, ToolCalls: turn.toolCalls}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

// echoTool 原样返回参数中的 text 字段。
type echoTool struct{}

func (echoTool) Name() string             { return "echo" }
func (echoTool) Description() string      { return "echo text back" }
func (echoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (echoTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct{ Text string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	return "echo: " + p.Text, nil
}

func TestRunToolLoop(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := store.Create()

	provider := &mockProvider{turns: []mockTurn{
		// 第一轮：模型请求调用工具
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)}}},
		// 第二轮：模型给出最终回答
		{content: "工具返回了 echo: hi"},
	}}

	registry := tools.NewRegistry()
	registry.Register(echoTool{})

	ag := New(provider, registry, store, Options{Model: "test", MaxTurns: 5})

	var events []Event
	ag.Run(context.Background(), meta.ID, "调用一下 echo", func(ev Event) { events = append(events, ev) })

	// 事件序列检查
	var types []EventType
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	if types[len(types)-1] != EventDone {
		t.Fatalf("last event = %v, want done; all events: %v", types[len(types)-1], types)
	}
	hasType := func(want EventType) bool {
		for _, tp := range types {
			if tp == want {
				return true
			}
		}
		return false
	}
	for _, want := range []EventType{EventToolStart, EventToolResult, EventDelta} {
		if !hasType(want) {
			t.Errorf("missing event %v in %v", want, types)
		}
	}
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolResult != "echo: hi" {
			t.Errorf("tool result = %q", ev.ToolResult)
		}
		if ev.Type == EventError {
			t.Errorf("unexpected error event: %s", ev.Error)
		}
	}

	// 持久化检查：user / assistant(tool_calls) / tool / assistant 共 4 条
	gotMeta, msgs, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d stored messages, want 4", len(msgs))
	}
	roles := []string{msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role}
	want := []string{"user", "assistant", "tool", "assistant"}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("message[%d].role = %q, want %q", i, roles[i], want[i])
		}
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "echo" {
		t.Errorf("assistant tool_calls not persisted: %+v", msgs[1].ToolCalls)
	}
	if msgs[3].Content != "工具返回了 echo: hi" {
		t.Errorf("final content = %q", msgs[3].Content)
	}
	// 首条消息生成标题
	if gotMeta.Title != "调用一下 echo" {
		t.Errorf("title = %q", gotMeta.Title)
	}
}

func TestRunPlainChat(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &mockProvider{turns: []mockTurn{{content: "你好"}}}

	ag := New(provider, tools.NewRegistry(), store, Options{Model: "test"})

	var final string
	var done bool
	ag.Run(context.Background(), meta.ID, "hi", func(ev Event) {
		if ev.Type == EventDelta {
			final += ev.Content
		}
		if ev.Type == EventDone {
			done = true
		}
	})
	if final != "你好" || !done {
		t.Errorf("final = %q, done = %v", final, done)
	}
	if provider.calls != 1 {
		t.Errorf("provider called %d times, want 1", provider.calls)
	}
}

func TestThinkingEventsAndPersistence(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &mockProvider{turns: []mockTurn{{reasoning: "推理一下", content: "答案"}}}

	ag := New(provider, tools.NewRegistry(), store, Options{Model: "test", Thinking: "high"})

	var thinking string
	ag.Run(context.Background(), meta.ID, "问题", func(ev Event) {
		if ev.Type == EventThinking {
			thinking += ev.Content
		}
	})
	if thinking != "推理一下" {
		t.Errorf("thinking events = %q", thinking)
	}
	// Thinking 配置透传到请求
	if provider.reqs[0].Thinking != "high" {
		t.Errorf("request thinking = %q", provider.reqs[0].Thinking)
	}
	// reasoning 持久化到 session
	_, msgs, _ := store.Get(meta.ID)
	if msgs[1].Reasoning != "推理一下" {
		t.Errorf("stored reasoning = %q", msgs[1].Reasoning)
	}
}

func TestTrimToBudget(t *testing.T) {
	big := strings.Repeat("字", 3000) // ≈3000 token
	msgs := []llm.Message{
		{Role: "system", Content: "env"},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: "最新问题"},
	}
	// 预算只够最近内容：ContextLength - MaxTokens - 2048 ≈ 4952 token
	ag := New(&mockProvider{}, tools.NewRegistry(), nil, Options{ContextLength: 8000, MaxTokens: 1000})
	got := ag.trimToBudget(msgs)

	if got[0].Role != "system" {
		t.Fatal("system message must be kept")
	}
	if len(got) >= len(msgs) {
		t.Fatalf("expected trimming, got %d messages", len(got))
	}
	// 裁剪必须从整轮丢弃：剩余的第一条非 system 消息必须是 user
	if got[1].Role != "user" {
		t.Errorf("first non-system message role = %q, want user", got[1].Role)
	}
	if got[len(got)-1].Content != "最新问题" {
		t.Error("latest user message must be kept")
	}
	// 预算充足时不裁剪
	ag2 := New(&mockProvider{}, tools.NewRegistry(), nil, Options{ContextLength: 1000000, MaxTokens: 4096})
	if got2 := ag2.trimToBudget(msgs); len(got2) != len(msgs) {
		t.Errorf("should not trim under budget, got %d/%d", len(got2), len(msgs))
	}
}

func TestCompact(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()

	// 先跑一轮带工具的对话，制造可压缩的历史
	provider := &mockProvider{turns: []mockTurn{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)}}},
		{content: "第一轮回答"},
		{content: "这是压缩摘要内容"}, // Compact 使用的第三次调用
	}}
	registry := tools.NewRegistry()
	registry.Register(echoTool{})
	ag := New(provider, registry, store, Options{Model: "test", MaxTurns: 5, Thinking: "high"})
	ag.Run(context.Background(), meta.ID, "做点事", func(Event) {})

	var streamed string
	var done bool
	ag.Compact(context.Background(), meta.ID, func(ev Event) {
		if ev.Type == EventDelta {
			streamed += ev.Content
		}
		if ev.Type == EventDone {
			done = true
		}
		if ev.Type == EventError {
			t.Fatalf("compact error: %s", ev.Error)
		}
	})
	if !done || streamed != "这是压缩摘要内容" {
		t.Errorf("done=%v streamed=%q", done, streamed)
	}

	// 压缩请求：思考关闭、序列化历史包含工具信息
	compactReq := provider.reqs[len(provider.reqs)-1]
	if compactReq.Thinking != "off" {
		t.Errorf("compact thinking = %q, want off", compactReq.Thinking)
	}
	if !strings.Contains(compactReq.Messages[0].Content, "echo") {
		t.Error("serialized history should mention tool call")
	}

	// 历史被替换为单条 summary 消息
	_, msgs, _ := store.Get(meta.ID)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages after compact, want 1", len(msgs))
	}
	if msgs[0].Kind != "summary" || msgs[0].Role != "user" {
		t.Errorf("summary message = kind %q role %q", msgs[0].Kind, msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "这是压缩摘要内容") {
		t.Errorf("summary content = %q", msgs[0].Content)
	}
}

func TestSystemMessageContainsEnvContext(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &mockProvider{turns: []mockTurn{{content: "ok"}}}

	ag := New(provider, tools.NewRegistry(), store, Options{
		Model:        "test",
		SystemPrompt: "自定义提示词",
		Workdir:      "D:\\some\\dir",
	})
	ag.Run(context.Background(), meta.ID, "hi", func(Event) {})

	msgs := provider.reqs[0].Messages
	if msgs[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", msgs[0].Role)
	}
	sys := msgs[0].Content
	for _, want := range []string{"[系统环境]", "操作系统:", "工作目录: D:\\some\\dir", "自定义提示词"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system message missing %q:\n%s", want, sys)
		}
	}
	// 环境块在前，配置提示词拼接在后
	if strings.Index(sys, "[系统环境]") > strings.Index(sys, "自定义提示词") {
		t.Error("env context should come before the configured system prompt")
	}
}
