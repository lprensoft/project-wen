package bodysense

import (
	"testing"
	"time"
)

func TestStageBoundaries(t *testing.T) {
	cases := []struct {
		count int
		want  string
	}{
		{0, ""},
		{1, stageFirst},
		{2, stageStrange},
		{3, stageStrange},
		{4, stageAdapting},
		{9, stageAdapting},
		{10, stageFamiliar},
		{19, stageFamiliar},
		{20, stageUsual},
		{200, stageUsual},
	}
	for _, c := range cases {
		if got := stageOf(c.count, paceNormal); got != c.want {
			t.Errorf("stageOf(%d) = %q，期望 %q", c.count, got, c.want)
		}
	}
}

func TestStagePace(t *testing.T) {
	// 快档次数加倍：10 次就到「习以为常」
	if got := stageOf(10, paceFast); got != stageUsual {
		t.Errorf("快档 10 次 = %q，期望 %q", got, stageUsual)
	}
	// 慢档次数减半：20 次只到「熟悉」
	if got := stageOf(20, paceSlow); got != stageFamiliar {
		t.Errorf("慢档 20 次 = %q，期望 %q", got, stageFamiliar)
	}
	// 慢档下第一次仍要算「初次」，不能因为取整塌成 0
	if got := stageOf(1, paceSlow); got != stageFirst {
		t.Errorf("慢档 1 次 = %q，期望 %q", got, stageFirst)
	}
	// 零次在任何档位下都没有阶段
	for _, pace := range []string{paceSlow, paceNormal, paceFast} {
		if got := stageOf(0, pace); got != "" {
			t.Errorf("%s 档 0 次 = %q，期望空串", pace, got)
		}
	}
}

func TestHumanizeGap(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Second, "1 分钟"},
		{2 * time.Hour, "2 小时"},
		{50 * time.Hour, "2 天"},
		{45 * 24 * time.Hour, "1 个月"},
		{400 * 24 * time.Hour, "1 年"},
	}
	for _, c := range cases {
		if got := humanizeGap(c.d); got != c.want {
			t.Errorf("humanizeGap(%v) = %q，期望 %q", c.d, got, c.want)
		}
	}
}
