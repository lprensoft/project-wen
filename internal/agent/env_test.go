package agent

import (
	"strings"
	"testing"
	"time"
)

func TestFormatNowIsChineseAndCarriesZone(t *testing.T) {
	// 时间是模型唯一无法自行获知却又必须与之一致的事实，格式上不该再给它解析负担；
	// 不写时区的话，跨时区的相对时间推算会悄悄错开。
	tz := time.FixedZone("CST", 8*3600)
	got := formatNow(time.Date(2026, 8, 17, 21, 5, 0, 0, tz))
	if got != "2026-08-17 21:05 星期一 +08:00" {
		t.Errorf("formatNow = %q", got)
	}
	if got := formatNow(time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)); !strings.Contains(got, "星期日") {
		t.Errorf("周日应写作星期日: %q", got)
	}
}

func TestEnvContextIncludesTime(t *testing.T) {
	got := envContext("D:\\work")
	for _, want := range []string{"[系统环境]", "当前时间:", "工作目录: D:\\work", "星期"} {
		if !strings.Contains(got, want) {
			t.Errorf("环境块缺少 %q:\n%s", want, got)
		}
	}
}
