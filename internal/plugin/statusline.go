package plugin

// StatusReporter 由需要在状态命令中露出一行运行状况的插件实现（可选）。
// 核心只负责按注册顺序把启用插件的非空行拼进状态输出，不解释内容——
// 它不知道「心跳」「定时任务」这回事，插件自己决定要不要说、说什么。
//
// 契约与 SystemPrompt / Actions 相同：必须廉价、无副作用、可随时调用——
// 它在 Manager 持读锁的路径上被调用，实现里不要反向调用 Manager 的方法
// （包括 InitContext.Status），也不要在 Init 内调用状态查询，那是写锁内。
//
// 措辞由插件负责：一行一条、自带前缀（如「💓 心跳：…」）。状态输出是给人扫一眼的，
// 不要塞多行长文，也不要放随时间抖动到看不懂的精确值。
type StatusReporter interface {
	StatusLines() []string
}
