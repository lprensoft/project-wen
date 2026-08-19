package main

import (
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-runewidth"
)

// 列表项必须严格占一行。
//
// huh 的 Select 视口按**行**计算高度，而选项标签是会折行的：一旦某项折成两三行，
// 光标往下挪一格就可能滚动好几行，表现为「刚进来按一下方向键，第一项就消失了」。
// 顶层菜单没这个毛病，只是因为它四项都短、装得下。
//
// 所以标签一律按终端宽度截断，并把列表高度设成实际项数——装得下就根本不滚动。

// 终端尺寸取不到时的兜底值。80x24 是最保守的假设，宁可截短也不要折行。
const (
	fallbackWidth  = 80
	fallbackHeight = 24
)

func termSize() (width, height int) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 || h <= 0 {
		return fallbackWidth, fallbackHeight
	}
	return w, h
}

// labelWidth 是一行标签可用的列数。留出 huh 自己画的边框、光标前缀与右侧余量，
// 少算几列比多算安全：多算就会折行，正是要避免的事。
func labelWidth() int {
	w, _ := termSize()
	w -= 8
	if w < 20 {
		return 20
	}
	return w
}

// fit 按显示宽度截断（中文占两列，不能按字符数算），超出部分以省略号收尾。
func fit(s string) string {
	return runewidth.Truncate(s, labelWidth(), "…")
}

// pad 按显示宽度右补空格，用于把插件名对齐成一列。
func pad(s string, width int) string {
	return runewidth.FillRight(s, width)
}

// listHeight 返回交给 huh Select 的字段高度；chrome 是该字段自己的标题与说明行数。
//
// huh 的 Height 是**含标题与说明**的字段总高度：它在 updateViewportHeight 里先扣掉
// 这两块，剩下的才是选项视口。此前这里把高度设成裸的项数，视口于是比项数少
// chrome 行，最后几项永远在视口外——光标一往下走就滚动，第一项被顶出屏幕。
//
// 全部装得下时返回 0（= 不设高度）：huh 会自动把视口设成选项总数，从机制上杜绝
// 滚动，而不是靠把行数算得刚刚好。真装不下才返回屏幕可用行数，那时滚动是必要的。
func listHeight(items, chrome int) int {
	_, h := termSize()
	avail := h - 3 // 帮助行与上下留白（标题与说明已计入 chrome）
	if items+chrome <= avail {
		return 0
	}
	if minTotal := chrome + 5; avail < minTotal {
		return minTotal
	}
	return avail
}

// fitLines 按行截断多行说明文字。说明区同样会折行，一折就把列表往下挤，
// 于是屏幕能显示的选项又少了几个。
func fitLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = fit(l)
	}
	return strings.Join(lines, "\n")
}
