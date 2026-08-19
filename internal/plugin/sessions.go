package plugin

import "time"

// SessionQuery 是核心提供给插件的会话只读查询。
//
// 后台类插件需要的只有两件事：把活儿落在哪个会话上，以及一个记下来的会话是否还在。
// 这些从前靠下发整个会话目录（InitContext.SessionDir）来满足，代价是为回答一个
// 布尔值换来了全部对话的读写权限。真正需要读会话正文的只有检索与归档类插件，
// 那种插件的功能定义本来就是「读全部会话」，宽权限在它身上名副其实。
type SessionQuery interface {
	// LastActive 返回最近活跃的会话 id：按最近一次真人交互的时间排序，旧会话
	// 没有该字段时回落创建时间。一个会话都没有时返回空 id，不算错误。
	//
	// 第二个返回值是该会话最近一次真人交互的时间，零值表示未知——只认真正的
	// 交互时间，不拿创建时间充数。
	LastActive() (id string, activeAt time.Time, err error)
	// LastInteraction 返回所有会话中最近一次真人交互的时间，零值表示从未有过。
	// 空闲判定要的是这个：一个刚被创建的会话并不代表有人来过。
	LastInteraction() (time.Time, error)
	// Exists 判断会话是否仍然存在。
	Exists(id string) bool
}
