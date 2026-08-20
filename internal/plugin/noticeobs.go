package plugin

import "context"

// 会话注记的旁听。注记本身（落盘、Web 界面实时展示）见 agent 侧的 AppendNotice；
// 这里补的是「注记落盘之后广播给插件」的通路，使 IM 通道能把后台工作留下的说明
// （提炼、清扫这类）按各自的开关推给远端用户。核心照旧不解释注记内容。

// NoticeEvent 是一条刚落盘的会话注记。
type NoticeEvent struct {
	SessionID string
	// Origin 是发起方插件名（由 Manager 注入，插件无法伪装）；前台写入为空。
	Origin string
	// Tag 是注记所属的可见域标签；空标签为共享域。
	Tag  string
	Text string
}

// NoticeObserver 由想旁听会话注记的插件实现。
// 在注记写入方的 goroutine 上被调用（可能是后台任务，也可能是工具执行的同步路径），
// 实现必须快速返回，耗时工作自行开 goroutine。
type NoticeObserver interface {
	OnNotice(ctx context.Context, ev NoticeEvent)
}
