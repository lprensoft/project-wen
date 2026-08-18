package agent

import (
	"strings"
	"testing"
)

func TestEnvContextIsStableAcrossTurns(t *testing.T) {
	got := envContext("D:\\work")
	for _, want := range []string{"[系统环境]", "工作目录: D:\\work"} {
		if !strings.Contains(got, want) {
			t.Errorf("环境块缺少 %q:\n%s", want, got)
		}
	}
	// 时间不能放在这里：它每轮都变，会连带作废整段 system 与历史的缓存前缀，
	// 而且离生成位置最远，最容易被历史里出现过的旧时刻盖过去。它归 turnStateBlock。
	for _, unwanted := range []string{"当前时间", "星期"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("环境块必须整轮不变，不该出现 %q:\n%s", unwanted, got)
		}
	}
	if got != envContext("D:\\work") {
		t.Error("同样入参两次调用应当逐字节相同，否则缓存前缀每轮作废")
	}
}
