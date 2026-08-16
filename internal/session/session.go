package session

import (
	"time"

	"wen/internal/llm"
)

// Meta 是每个 session 文件的第一行记录。
type Meta struct {
	Type      string    `json:"type"` // 恒为 "meta"
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// StoredMessage 是 JSONL 中的一条消息记录（meta 行之后）。
type StoredMessage struct {
	llm.Message
	TS time.Time `json:"ts"`
}
