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
}

// StoredMessage 是 JSONL 中的一条消息记录（meta 行之后）。
type StoredMessage struct {
	llm.Message
	Kind string    `json:"kind,omitempty"` // "" 普通消息；"summary" 压缩摘要
	TS   time.Time `json:"ts"`
}
