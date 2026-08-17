package plugin

import "context"

// ConfirmRequest 是一次待用户确认的高风险操作。
type ConfirmRequest struct {
	Source string `json:"source"` // 发起确认的插件名
	Title  string `json:"title"`  // 一句话说明要做什么，如「执行命令」
	Detail string `json:"detail"` // 具体内容原文，如命令本身。界面上原样展示，不要在这里做省略
	Reason string `json:"reason"` // 为什么需要确认，如命中的风险类别
}

// ConfirmFunc 请求用户确认一次操作，返回 true 表示用户同意。
//
// 它是「不可逆操作交由人判断」的通用通道：核心不知道什么算危险，由插件自己判定并发起
// 请求。返回 error 表示这次确认没能完成（用户离开、超时、连接断开），调用方必须按拒绝
// 处理——拿不到答复不等于得到许可。
type ConfirmFunc func(ctx context.Context, req ConfirmRequest) (bool, error)

type confirmKey struct{}

// WithConfirmer 把确认通道放进 context，供工具执行时取用。
// 它必须按请求注入而不是放进 InitContext：确认要回到发起这轮对话的那个界面，
// 而插件是进程级单实例。
func WithConfirmer(ctx context.Context, fn ConfirmFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, confirmKey{}, fn)
}

// ConfirmerFrom 取出确认通道。第二个返回值为 false 表示当前没有可交互的用户
// （无人值守运行、或调用方没有接入界面），插件应据此按自己的默认策略处理，
// **不要当作已获同意**。
func ConfirmerFrom(ctx context.Context) (ConfirmFunc, bool) {
	fn, ok := ctx.Value(confirmKey{}).(ConfirmFunc)
	return fn, ok && fn != nil
}
