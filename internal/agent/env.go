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
		time.Now().Format("2006-01-02 15:04 Mon"),
	)
}
