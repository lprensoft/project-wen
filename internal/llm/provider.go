package llm

import (
	"context"
	"encoding/json"
)

// Role 常量与 OpenAI 协议一致。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning_content,omitempty"` // 思考内容；带工具调用时必须回传给 API
	// ReasoningBlocks 保留 provider 原始的思考块（含签名），跨轮回传时需要。
	// 仅 Anthropic 使用；OpenAI 兼容模式忽略此字段，只用 Reasoning。
	ReasoningBlocks []ReasoningBlock `json:"reasoning_blocks,omitempty"`
	ToolCalls       []ToolCall       `json:"tool_calls,omitempty"`   // assistant 发起的工具调用
	ToolCallID      string           `json:"tool_call_id,omitempty"` // role=tool 时对应的调用 ID
}

// ReasoningBlock 是一个原样保留的思考块。Anthropic 要求带签名原样回传，
// 签名缺失或被改动会被 API 拒绝。
type ReasoningBlock struct {
	Type      string `json:"type"`                // thinking / redacted_thinking
	Text      string `json:"text,omitempty"`      // 思考文本
	Signature string `json:"signature,omitempty"` // 思考块签名
	Data      string `json:"data,omitempty"`      // redacted_thinking 的密文
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolSpec 描述一个可供模型调用的工具。
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage // 参数 JSON Schema
}

type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolSpec
	Temperature float64
	MaxTokens   int
	// Thinking 思考模式："off" 关闭；"low"/"medium"/"high"/"xhigh"/"max" 为思考强度
	//（DeepSeek 服务端将 medium/xhigh 归并为 high）。思考开启时 Temperature 不生效。
	Thinking string
}

type EventType int

const (
	EventContentDelta   EventType = iota // Content 为文本增量
	EventReasoningDelta                  // Content 为思考内容增量
	EventToolCalls                       // ToolCalls 为累积完整的工具调用
	EventReasoning                       // Reasoning 为累积完整的思考块（含签名）
	EventUsage                           // Usage 为本次请求的实测 token 用量
	EventDone
	EventError
)

// Usage 是 API 返回的实测 token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type StreamEvent struct {
	Type      EventType
	Content   string
	ToolCalls []ToolCall
	Reasoning []ReasoningBlock
	Usage     *Usage
	Err       error
}

// Provider 是 LLM 后端的统一接口。
// ChatStream 返回的 channel 依次吐出事件，流结束（含出错）后关闭。
type Provider interface {
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
