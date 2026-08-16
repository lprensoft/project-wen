package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const readFileMaxBytes = 64 * 1024

// ReadFile 读取文本文件内容，超过 64KB 截断。
type ReadFile struct {
	Workdir string // 相对路径的基准目录
}

func (t *ReadFile) Name() string { return "read_file" }

func (t *ReadFile) Description() string {
	return "读取指定路径的文本文件内容。相对路径基于工作目录解析。文件超过 64KB 时截断。"
}

func (t *ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "要读取的文件路径（绝对或相对）"}
		},
		"required": ["path"]
	}`)
}

func (t *ReadFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	path := p.Path
	if !filepath.IsAbs(path) && t.Workdir != "" {
		path = filepath.Join(t.Workdir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	truncated := false
	if len(data) > readFileMaxBytes {
		data = data[:readFileMaxBytes]
		// 避免把多字节字符切成半个
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		truncated = true
	}
	out := string(data)
	if truncated {
		out += "\n...[内容超过 64KB，已截断]"
	}
	return out, nil
}
