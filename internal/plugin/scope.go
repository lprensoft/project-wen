package plugin

import (
	"context"
	"slices"
	"time"

	"wen/internal/llm"
)

// Scope 描述一轮对话的可见域：本轮产生的消息归属哪个标签，以及哪些标签的历史与
// 数据在本轮可读。零值表示不做任何限制（没有插件参与裁决时的默认行为）。
//
// 标签的语义完全由插件定义，核心只做三件事：给消息打标签、按标签过滤历史、
// 按标签分组压缩。标签值会被插件用来拼持久化目录（分库、分目录归档），
// 因此必须与插件名同样的字符集，由 Manager.DecideScope 校验。
type Scope struct {
	Write string   // 本轮新消息落盘时打的标签；空串 = 共享（所有域可读）
	Read  []string // 可读标签白名单；nil = 不限制
}

// CanRead 判断某标签的内容在本轮是否可读。
// 空标签恒可读：这既让升级前没有标签的历史数据继续可用，也让「共享」成为
// 不需要任何插件参与就成立的默认语义。
func (s Scope) CanRead(tag string) bool {
	if tag == "" || s.Read == nil {
		return true
	}
	return slices.Contains(s.Read, tag)
}

// IsZero 表示该 Scope 未参与裁决。
func (s Scope) IsZero() bool { return s.Write == "" && s.Read == nil }

// TaggedMessage 是带可见域标签的历史消息，供钩子只读检视。
// 不直接用 session.StoredMessage：plugin 包不引入 session 依赖，
// 沿用 CompactEvent.History 已有的「由核心负责转换」的做法。
type TaggedMessage struct {
	llm.Message
	Tag  string    // 可见域标签，空串 = 共享
	Kind string    // "" 普通消息；"summary" 压缩摘要
	TS   time.Time // 落盘时间
}

// TurnEvent 描述一轮对话的开始。
type TurnEvent struct {
	SessionID string
	UserInput string
	History   []TaggedMessage // 该会话的完整历史，未按可见域过滤
	Scope     Scope           // 仅在 TurnPrompt 阶段有效（此时已完成裁决）
}

// ScopeDecider 是插件的可选能力：决定本轮对话的可见域。
//
// 可见域是单所有者机制——按注册顺序第一个返回非零 Scope 的插件胜出。不做多插件
// 合并：Write 来自一个插件而 Read 来自另一个是无法推理的组合。
type ScopeDecider interface {
	DecideScope(ctx context.Context, ev TurnEvent) (Scope, error)
}

// TurnPrompter 是插件的可选能力：按本轮上下文追加一次性的系统提示词片段（不落盘）。
//
// 与 SystemPrompt() 的区别是它拿得到会话 id 与已裁决的可见域，因此能注入随会话
// 与可见域变化的内容。SystemPrompt() 的契约是廉价、无副作用、随时可调（列表接口
// 会对禁用插件也调用它），不宜为此改签名。
type TurnPrompter interface {
	TurnPrompt(ctx context.Context, ev TurnEvent) (string, error)
}

type scopeKey struct{}

// WithScope 把本轮可见域放进 context，供工具执行时读取。
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom 取出本轮可见域；未设置时返回零值（不限制），使不感知可见域的调用方
// 与未启用任何裁决插件时的行为一致。
func ScopeFrom(ctx context.Context) Scope {
	if s, ok := ctx.Value(scopeKey{}).(Scope); ok {
		return s
	}
	return Scope{}
}
