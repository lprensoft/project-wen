package agent

import (
	"context"
	"encoding/json"

	"wen/internal/llm"
)

// 提示词追踪：把每次 LLM 调用实际发出的请求体（裁剪后的完整消息与工具声明）
// 作为一个事件发出，供界面调试查看。
//
// 之所以做成按请求开启而不是常开：一条用户消息最多会触发 MaxTurns 次调用，
// 每次都携带完整上下文，整轮序列化下来可能是几 MB。关着的时候一个字节都不组装，
// 代价是它只对开启之后的轮次生效——回溯不了，这是有意的取舍。
type promptTraceKey struct{}

// WithPromptTrace 标记本轮需要发出 EventPrompt。
func WithPromptTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, promptTraceKey{}, true)
}

// promptTraceEnabled 判断本轮是否需要发出 EventPrompt。
func promptTraceEnabled(ctx context.Context) bool {
	on, _ := ctx.Value(promptTraceKey{}).(bool)
	return on
}

// marshalPromptTrace 把请求体转成界面用的 JSON。这里另起一个结构而不是直接
// 序列化 llm.ChatRequest：后者是核心类型，不该为了一个调试视图挂上 json 标签，
// 何况呈现给人看的字段名与顺序本就该由展示方决定。
func marshalPromptTrace(req llm.ChatRequest) (json.RawMessage, error) {
	return json.Marshal(struct {
		Model       string         `json:"model"`
		Thinking    string         `json:"thinking"`
		Temperature float64        `json:"temperature"`
		MaxTokens   int            `json:"max_tokens"`
		Tools       []llm.ToolSpec `json:"tools,omitempty"`
		Messages    []llm.Message  `json:"messages"`
	}{
		Model:       req.Model,
		Thinking:    req.Thinking,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
		Messages:    req.Messages,
	})
}
