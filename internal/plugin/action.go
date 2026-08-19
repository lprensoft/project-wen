package plugin

import (
	"context"
	"strings"
)

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
// 核心不关心 Image 与 Link 的内容——它们只是「操作过程中需要给用户看的东西」，
// 只经内存下发，不落盘。
type ActionState struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"` // 人类可读的进展或结果说明
	Image   []byte `json:"image,omitempty"`   // 可选 PNG，界面渲染为 data URI
	// Link 是操作涉及的一个链接原文（如编码进二维码的 URL）。图形界面用 Image，
	// 终端界面拿它自行渲染（如字符二维码）——同一内容的两种呈现。
	Link string `json:"link,omitempty"`
}

// Actionable 由需要「用户在界面上主动触发一段流程」的插件实现（可选）。
// Actions 必须廉价（每次刷新插件列表都会调用，与 SystemPrompt 同一契约）；
// StartAction 应立即返回，长流程放后台 goroutine 并自带超时；进行中重复调用表示重新开始。
// 实现自行对状态加锁——StartAction 与 ActionState 会被 HTTP 处理并发调用。
// ctx 可能带有配置弹窗里尚未保存的草稿值，用 ActionValuesFrom 取（须在 StartAction 内同步取出）。
type Actionable interface {
	Actions() []ActionDef
	StartAction(ctx context.Context, key string) error
	ActionState(key string) (ActionState, error)
}

// ---------- 操作的草稿配置值 ----------

// actionValuesKey 是 ctx 中草稿配置值的键。
type actionValuesKey struct{}

// WithActionValues 把「配置弹窗里当前填写、尚未保存的值」附到 ctx 上，供操作读取。
//
// 存在的理由是「测试」类操作：用户填完一项配置想先验证它能不能用，再决定要不要保存，
// 而 StartAction 只能看到已保存的配置。走 ctx 而不是给 StartAction 加参数，是为了不动
// 已有实现的签名——不关心草稿值的操作照旧不用管它。
//
// 值未经校验，就是界面原样提交的表单内容，插件自行按 CfgString / CfgInt 取用。
func WithActionValues(ctx context.Context, values map[string]any) context.Context {
	if len(values) == 0 {
		return ctx
	}
	return context.WithValue(ctx, actionValuesKey{}, values)
}

// ActionValuesFrom 取出本次操作附带的草稿配置值；没有则返回 nil。
//
// 必须在 StartAction 内**同步**取出：ctx 属于那个 HTTP 请求，响应发出后就失效了，
// 而长流程是在后台 goroutine 里跑的，拿着 ctx 过去读会读到已经作废的东西。
func ActionValuesFrom(ctx context.Context) map[string]any {
	v, _ := ctx.Value(actionValuesKey{}).(map[string]any)
	return v
}

// ActionValueOr 从草稿值里取一个字符串配置项，草稿里没有（或为空）时回落到 saved。
// 「测试」类操作取参数的通用写法：以界面上正在填的为准，没填过就用已保存的。
func ActionValueOr(values map[string]any, key, saved string) string {
	if v := strings.TrimSpace(CfgString(values, key, "")); v != "" {
		return v
	}
	return saved
}
