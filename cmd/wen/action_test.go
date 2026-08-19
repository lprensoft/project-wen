package main

import (
	"strings"
	"testing"
)

func TestTerminalQRShape(t *testing.T) {
	out, err := terminalQR("https://example.com/bind?code=abc123")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("二维码只有 %d 行，不像一个完整的码", len(lines))
	}
	for i, l := range lines {
		// 每行都要以颜色重置收尾，否则会把黑白底色漏给后面的输出
		if !strings.HasSuffix(l, "\x1b[0m") {
			t.Fatalf("第 %d 行未重置颜色", i)
		}
		// 黑白必须显式指定，不跟随终端配色（深色主题下反色的码扫不出来）
		if !strings.Contains(l, "\x1b[30;") && !strings.Contains(l, "\x1b[97;") {
			t.Fatalf("第 %d 行没有显式颜色", i)
		}
	}
	// 半块渲染：行数约为模块行数的一半，且各行显示宽度一致
	w := strings.Count(lines[0], "▀")
	for i, l := range lines {
		if strings.Count(l, "▀") != w {
			t.Fatalf("第 %d 行宽度不一致", i)
		}
	}
}

func TestHalfBlockColors(t *testing.T) {
	cases := []struct {
		top, bottom bool
		want        string
	}{
		{true, true, "\x1b[30;40m▀"},    // 全黑
		{false, false, "\x1b[97;107m▀"}, // 全白
		{true, false, "\x1b[30;107m▀"},  // 上黑下白
		{false, true, "\x1b[97;40m▀"},   // 上白下黑
	}
	for _, c := range cases {
		if got := halfBlock(c.top, c.bottom); got != c.want {
			t.Errorf("halfBlock(%v,%v) = %q, want %q", c.top, c.bottom, got, c.want)
		}
	}
}
