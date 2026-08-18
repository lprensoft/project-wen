package memory

import (
	"fmt"
	"strings"
)

// StatusLines 报一行当前的记忆行为：多久提炼一次、会不会淡忘、时限是多少。
//
// 刻意不报条数。StatusLines 拿不到本轮的可见域（接口没有这个参数，它在 Manager
// 持锁的路径上被调用），而条数本身就会泄漏「存在什么」——启用 dual_persona 时，
// 一个人格的状态输出里出现另一个人格的记忆条数，藏住的就只是内容而不是存在性了。
// 要看有多少条，list_memories 是按可见域算的。
func (p *Plugin) StatusLines() []string {
	s := p.snapshot()
	if s.store == nil {
		return nil
	}
	var parts []string
	if s.turnExtract {
		parts = append(parts, fmt.Sprintf("每 %d 轮对话提炼一次", s.turnEvery))
	}
	if s.autoExtract {
		parts = append(parts, "压缩前提炼一次")
	}
	if s.decay {
		parts = append(parts, fmt.Sprintf("标记为会淡忘的记忆 %d 天未提及只剩要点、%d 天后移出",
			s.blurDays, s.forgetDays))
	}
	if len(parts) == 0 {
		return []string{"🧠 记忆：只在模型主动保存时记录"}
	}
	return []string{"🧠 记忆：" + strings.Join(parts, "；")}
}
