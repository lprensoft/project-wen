// Package execcmd 提供执行 shell 命令的系统插件。
package execcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"wen/internal/plugin"
)

const outputMaxBytes = 32 * 1024

// Plugin 是 exec_command 系统插件（不注入提示词）。
type Plugin struct {
	workdir string
	timeout time.Duration
}

func New() *Plugin { return &Plugin{timeout: 60 * time.Second} }

func (p *Plugin) Name() string        { return "exec_command" }
func (p *Plugin) Description() string { return "在工作目录下执行 shell 命令" }
func (p *Plugin) SystemPrompt() string { return "" }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{{
		Key:         "timeout_seconds",
		Label:       "命令超时（秒）",
		Type:        plugin.FieldInt,
		Description: "单条命令的最长执行时间，超时后终止并把已有输出返回给模型。",
		Default:     60,
		Min:         plugin.IntPtr(1),
		Max:         plugin.IntPtr(3600),
	}}
}

func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	p.workdir = ictx.Workdir
	p.timeout = time.Duration(plugin.CfgInt(cfg, "timeout_seconds", 60)) * time.Second
	return nil
}

func (p *Plugin) Tools() []plugin.Tool { return []plugin.Tool{&tool{p: p}} }

type tool struct {
	p *Plugin
}

func (t *tool) Name() string { return "exec_command" }

func (t *tool) Description() string {
	return "在工作目录下执行一条 shell 命令并返回其输出（stdout 与 stderr 合并）。使用的 shell 见系统环境信息。有超时限制，输出过长会被截断。"
}

func (t *tool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "要执行的 shell 命令"}
		},
		"required": ["command"]
	}`)
}

func (t *tool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := t.p.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", a.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", a.Command)
	}
	cmd.Dir = t.p.workdir

	out, err := cmd.CombinedOutput()
	out = plugin.NormalizeEncoding(out)
	out, truncated := plugin.TruncateUTF8(out, outputMaxBytes)

	result := string(out)
	if truncated {
		result += fmt.Sprintf("\n...[输出超过 %d 字节，已截断]", outputMaxBytes)
	}
	if ctx.Err() == context.DeadlineExceeded {
		result += fmt.Sprintf("\n[命令超时（%s），已终止]", timeout)
		return result, nil
	}
	if err != nil {
		// 命令非零退出也把输出带回给模型，方便其自行判断
		result += fmt.Sprintf("\n[命令退出异常: %v]", err)
	}
	return result, nil
}
