package session

import (
	"time"

	"wen/internal/llm"
)

// Usage 是最近一次 LLM 请求的实测 token 用量（来自 API usage 字段）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Meta 是每个 session 文件的第一行记录。
type Meta struct {
	Type      string    `json:"type"` // 恒为 "meta"
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	LastUsage *Usage    `json:"last_usage,omitempty"`
	// Tag 是生成该标题的那一轮消息所属的可见域标签（空串 = 共享）。
	// 标题取自首条用户消息，因此它和消息一样需要归属，否则会话检索会把不可读域的
	// 内容通过标题漏出去。
	Tag string `json:"tag,omitempty"`
}

// KindSummary 标记压缩摘要消息（Kind 为空则是普通消息）。
const KindSummary = "summary"

// StoredMessage 是 JSONL 中的一条消息记录（meta 行之后）。
type StoredMessage struct {
	llm.Message
	Kind string    `json:"kind,omitempty"` // "" 普通消息；KindSummary 压缩摘要
	Tag  string    `json:"tag,omitempty"`  // 可见域标签，空串 = 共享（所有域可读）
	TS   time.Time `json:"ts"`
}
