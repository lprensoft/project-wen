package agent

import (
	"fmt"
	"runtime"
	"time"
)

// envContext 生成注入 system 消息开头的系统环境块，
// 让模型无需在每个工具描述里重复平台信息。内容为纯功能性描述。
func envContext(workdir string) string {
	shell := "sh -c"
	if runtime.GOOS == "windows" {
		shell = "cmd /C（请使用 Windows 命令语法，如 dir、type、findstr）"
	}
	locale := systemLocale()
	if locale == "" {
		locale = "未知"
	}
	return fmt.Sprintf(
		"[系统环境]\n操作系统: %s/%s\nShell: %s\n工作目录: %s\n系统区域与语言: %s\n当前时间: %s",
		runtime.GOOS, runtime.GOARCH, shell, workdir, locale,
		formatNow(time.Now()),
	)
}

var weekdayCN = [...]string{"日", "一", "二", "三", "四", "五", "六"}

// formatNow 输出中文时间：星期用中文而不是 Mon 这类英文缩写，并带上时区偏移。
// 时间是模型唯一无法自行获知却又必须与之保持一致的事实，格式上不该再给它额外的
// 解析负担；不写时区的话，跨时区的相对时间推算会悄悄错开。
func formatNow(t time.Time) string {
	return fmt.Sprintf("%s 星期%s %s",
		t.Format("2006-01-02 15:04"), weekdayCN[int(t.Weekday())], t.Format("Z07:00"))
}
