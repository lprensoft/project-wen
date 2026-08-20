// Package availability 是「角色此刻忙不忙」的进程级公告板：日程插件在活动开始时写、
// 结束时清，主动开口与消息通道这类插件据此决定要不要把下一拍压后、把入站消息晚一点
// 再处理。与 internal/cue 同级、同样不落盘、核心不知道它。
//
// 与 cue 的分别在于存的东西不同：cue 存**事件**（「刚下起雨」），消费即清，按
// (Source, Key) 幂等；这里存**状态**（「在图书馆查资料，重忙，到 16:30」），查询不清，
// 每个写入方只有一条，靠 Until 自然过期。相同的部分：只在内存、必须带过期时刻、
// 单槽位叫醒回调、写入方自己的持久化才是权威——日程插件重启后从当天的表里
// 「进行中」的那一项重建，公告板自己不需要回答「重启后这条还算数吗」。
package availability

import (
	"sync"
	"time"
)

// Level 是忙的程度，由低到高。
type Level int

const (
	Free  Level = iota // 空闲
	Light              // 轻忙：能随时回
	Heavy              // 重忙：只能简短回
	Away               // 不回：完全顾不上
)

// levelNames 是各档的中文说法，与日程工具的 busy 参数同一套词。
var levelNames = [...]string{"空闲", "轻忙", "重忙", "不回"}

// String 返回档位的中文说法。
func (l Level) String() string {
	if l < Free || int(l) >= len(levelNames) {
		return "未知"
	}
	return levelNames[l]
}

// ParseLevel 把中文说法解析成档位；认不出时返回 Free 与 false。
func ParseLevel(s string) (Level, bool) {
	for i, n := range levelNames {
		if n == s {
			return Level(i), true
		}
	}
	return Free, false
}

// State 是一个写入方宣布的忙碌状态。
type State struct {
	Source   string    // 写入方插件名
	Activity string    // 在忙什么，一句话
	Level    Level     // 忙的程度
	Since    time.Time // 从何时开始
	Until    time.Time // 预计到几点；过期即视为空闲
}

var (
	mu     sync.Mutex
	states []State // 每个 Source 至多一条，按首次写入顺序
	notify func()
)

// Set 写入（或覆盖）一个写入方的状态。Until 不在未来、或 Level 为空闲的直接丢弃——
// 「空闲」用 Clear 表达，不占一条记录。随后叫醒读取方（若装了回调）。
func Set(s State) {
	if s.Source == "" || s.Level <= Free || !s.Until.After(time.Now()) {
		return
	}
	if s.Since.IsZero() {
		s.Since = time.Now()
	}
	mu.Lock()
	pruneLocked(time.Now())
	replaced := false
	for i := range states {
		if states[i].Source == s.Source {
			states[i], replaced = s, true
			break
		}
	}
	if !replaced {
		states = append(states, s)
	}
	fn := notify
	mu.Unlock()

	// 回调在锁外：读取方的回调可能取它自己的锁，嵌在这里会定死锁顺序
	if fn != nil {
		fn()
	}
}

// Clear 撤掉一个写入方的状态（宣布忙完）。没有记录时什么都不发生；有记录时
// 同样叫醒读取方——压后的那一拍也许可以提前回来了。
func Clear(source string) {
	mu.Lock()
	removed := false
	for i := range states {
		if states[i].Source == source {
			states = append(states[:i], states[i+1:]...)
			removed = true
			break
		}
	}
	fn := notify
	mu.Unlock()
	if removed && fn != nil {
		fn()
	}
}

// Current 返回此刻生效的状态：未过期的记录里 Level 最高的一条（同级取先写入的）。
// 没有任何未过期记录时第二个返回值为 false。
func Current(now time.Time) (State, bool) {
	mu.Lock()
	defer mu.Unlock()
	pruneLocked(now)
	var (
		best  State
		found bool
	)
	for _, s := range states {
		if !found || s.Level > best.Level {
			best, found = s, true
		}
	}
	return best, found
}

// SetNotify 安装（或用 nil 卸下）状态变化时的叫醒回调。单槽位：与 cue.SetNotify
// 同款约定——只有一个按节奏主动开口的读取方。
func SetNotify(fn func()) {
	mu.Lock()
	defer mu.Unlock()
	notify = fn
}

// pruneLocked 清掉已过期的记录。调用方须持 mu。
func pruneLocked(now time.Time) {
	kept := states[:0]
	for _, s := range states {
		if s.Until.After(now) {
			kept = append(kept, s)
		}
	}
	states = kept
}
