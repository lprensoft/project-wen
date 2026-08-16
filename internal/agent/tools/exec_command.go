package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const execOutputMaxBytes = 32 * 1024

// ExecCommand 在工作目录下执行 shell 命令，带超时与输出截断。
type ExecCommand struct {
	Workdir string
	Timeout time.Duration
}

func (t *ExecCommand) Name() string { return "exec_command" }

func (t *ExecCommand) Description() string {
	return "在工作目录下执行一条 shell 命令并返回其输出（stdout 与 stderr 合并）。使用的 shell 见系统环境信息。有超时限制，输出过长会被截断。"
}

func (t *ExecCommand) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "要执行的 shell 命令"}
		},
		"required": ["command"]
	}`)
}

func (t *ExecCommand) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", p.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", p.Command)
	}
	cmd.Dir = t.Workdir

	out, err := cmd.CombinedOutput()
	out = normalizeEncoding(out)

	truncated := false
	if len(out) > execOutputMaxBytes {
		out = out[:execOutputMaxBytes]
		for len(out) > 0 && !utf8.Valid(out) {
			out = out[:len(out)-1]
		}
		truncated = true
	}
	result := string(out)
	if truncated {
		result += "\n...[输出超过 32KB，已截断]"
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

// normalizeEncoding 处理中文 Windows 下 cmd 的 GBK 输出：非法 UTF-8 时尝试按 GBK 解码。
func normalizeEncoding(out []byte) []byte {
	if utf8.Valid(out) {
		return out
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(out); err == nil {
		return decoded
	}
	return out
}
