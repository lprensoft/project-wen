package plugin

import "context"

// ActionDef 描述插件对外暴露的一个操作入口（设置页插件卡片上的按钮）。
// Label 可随插件状态变化（如「扫码绑定」在绑定后变「重新绑定」），界面每次列表刷新取最新值。
type ActionDef struct {
	Key         string `json:"key"` // 限小写字母开头的小写字母、数字与下划线
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// 操作的运行状态。
const (
	ActionIdle    = "idle"    // 尚未开始（或上次结果已被新一次覆盖前的初始态）
	ActionPending = "pending" // 进行中，界面应继续轮询
	ActionDone    = "done"    // 成功结束
	ActionError   = "error"   // 失败结束
)

// ActionState 是操作的当前进展，供界面轮询展示。
// 核心不关心 Image 的内容——它只是「操作过程中需要给用户看的一张图」（如二维码），
// 只经内存下发，不落盘。
type ActionState struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"` // 人类可读的进展或结果说明
	Image   []byte `json:"image,omitempty"`   // 可选 PNG，界面渲染为 data URI
}

// Actionable 由需要「用户在界面上主动触发一段流程」的插件实现（可选）。
// Actions 必须廉价（会在 Manager 持锁的列表路径上调用，与 SystemPrompt 同一契约）；
// StartAction 应立即返回，长流程放后台 goroutine 并自带超时；进行中重复调用表示重新开始。
// 实现自行对状态加锁——StartAction 与 ActionState 会被 HTTP 处理并发调用。
type Actionable interface {
	Actions() []ActionDef
	StartAction(ctx context.Context, key string) error
	ActionState(key string) (ActionState, error)
}
