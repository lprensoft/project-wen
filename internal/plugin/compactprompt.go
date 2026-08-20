package plugin

import "context"

// CompactPrompter 是插件的可选能力：为压缩摘要的生成提示词追加一段要求。
//
// 核心的压缩提示词是按任务型对话写的（保留诉求、事实、工具结果、待办；丢弃寒暄），
// 而有些对话要保住的不只是事实——关系的状态、情绪的走向、未了的话头、彼此的称呼
// 与说话方式。什么算「值得保住」由插件按自己的领域定义，核心只负责把这些要求拼进
// 提示词，并声明它们优先于基础要求。
//
// 与 SystemPrompt 同契约：廉价、无副作用，不发起模型调用。ctx 带本组历史的可见域
// （plugin.ScopeFrom），按域压缩时每组各调一次。返回空串表示本次不追加。
type CompactPrompter interface {
	CompactPrompt(ctx context.Context) string
}
