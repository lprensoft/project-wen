package bodysense

import (
	"fmt"
	"time"
)

// 熟悉的快慢。它作用在次数上而不是阈值上：一次乘法，没有取整歧义，
// 也不会因为缩放让某一档的阈值塌成 0。
const (
	paceSlow   = "slow"
	paceNormal = "normal"
	paceFast   = "fast"
)

// 五档熟悉度。分档取对数形状——人的适应本来就是对数的，头几次的差别最大，
// 到二十次以后再多几次已经没有分别。
//
// 「初次」必须单独成档：需求里最要紧的信号就是「第一次被碰到会僵住」，
// 把 1 和 2 合成一档正好把它糊掉。
const (
	stageFirst    = "初次"
	stageStrange  = "生疏"
	stageAdapting = "适应"
	stageFamiliar = "熟悉"
	stageUsual    = "习以为常"
)

// stageOrder 是注入时按阶段归并的顺序（最熟悉的在前）。
var stageOrder = []string{stageUsual, stageFamiliar, stageAdapting, stageStrange, stageFirst}

// effectiveCount 按快慢缩放实际次数。慢档至少留 1，否则第一次触碰会算不出阶段。
func effectiveCount(count int, pace string) int {
	if count <= 0 {
		return 0
	}
	switch pace {
	case paceFast:
		return count * 2
	case paceSlow:
		if n := count / 2; n >= 1 {
			return n
		}
		return 1
	default:
		return count
	}
}

// stageOf 按累计次数给出熟悉阶段。次数为 0 时返回空串——没记录过的部位不该出现在
// 任何输出里。
func stageOf(count int, pace string) string {
	switch n := effectiveCount(count, pace); {
	case n <= 0:
		return ""
	case n == 1:
		return stageFirst
	case n <= 3:
		return stageStrange
	case n <= 9:
		return stageAdapting
	case n <= 19:
		return stageFamiliar
	default:
		return stageUsual
	}
}

// humanizeGap 把时间间隔说成人话。分档与措辞和 roleplay 的同名函数保持一致
// （那个函数在 roleplay 包内未导出，这里复制一份同规格实现），
// 整数截断：90 秒说「1 分钟」，45 天说「1 个月」。
func humanizeGap(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%d 个月", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%d 年", int(d.Hours()/24/365))
	}
}
