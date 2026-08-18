package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/session"
)

// 提示词追踪默认关闭，开启后每次 LLM 调用各发一条，内容为实际提交的请求体。

func TestPromptTraceOffByDefault(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &mockProvider{turns: []mockTurn{{content: "好的"}}}
	ag := New(provider, newTestManager(t), store, Options{Model: "test"})

	var got []Event
	ag.Run(context.Background(), meta.ID, "你好", func(ev Event) { got = append(got, ev) })

	for _, ev := range got {
		if ev.Type == EventPrompt {
			t.Fatal("未开启追踪却收到了 prompt 事件")
		}
	}
}

func TestPromptTraceEmitsRequest(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()

	// 两次调用：先请求工具，再给最终回答——追踪应各发一条
	provider := &mockProvider{turns: []mockTurn{
		{toolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)}}},
		{content: "结束"},
	}}
	ag := New(provider, newTestManager(t, echoPlugin{}, promptPlugin{}), store,
		Options{Model: "test-model", MaxTurns: 5, Thinking: "off"})

	var prompts []json.RawMessage
	ag.Run(WithPromptTrace(context.Background()), meta.ID, "调用一下 echo", func(ev Event) {
		if ev.Type == EventPrompt {
			prompts = append(prompts, ev.Prompt)
		}
	})

	if len(prompts) != 2 {
		t.Fatalf("收到 %d 条 prompt 事件，期望 2 条（每次 LLM 调用一条）", len(prompts))
	}

	var first struct {
		Model    string `json:"model"`
		Thinking string `json:"thinking"`
		Tools    []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(prompts[0], &first); err != nil {
		t.Fatalf("prompt 事件不是合法 JSON: %v", err)
	}
	if first.Model != "test-model" || first.Thinking != "off" {
		t.Errorf("模型参数未随请求带出: model=%q thinking=%q", first.Model, first.Thinking)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "echo" {
		t.Errorf("工具声明未随请求带出: %+v", first.Tools)
	}
	// system 打头，且插件注入的片段确实在里面——这正是这个调试视图要给人看的东西
	if len(first.Messages) == 0 || first.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("首条消息不是 system: %+v", first.Messages)
	}
	if !strings.Contains(first.Messages[0].Content, "插件注入的提示词片段") {
		t.Error("system 消息里没有插件注入的提示词片段")
	}

	// 第二次调用发生在工具执行之后，上下文应当更长
	var second struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(prompts[1], &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) <= len(first.Messages) {
		t.Errorf("第二次调用的消息数 %d 未超过第一次的 %d，工具往返没有反映进去",
			len(second.Messages), len(first.Messages))
	}
}
