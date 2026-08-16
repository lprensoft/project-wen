package execcmd

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"wen/internal/plugin"
)

func TestExecCommandOutput(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("测试用例针对 Windows cmd")
	}
	p := New()
	if err := p.Init(plugin.InitContext{}, nil); err != nil {
		t.Fatal(err)
	}
	tool := p.Tools()[0]
	args, _ := json.Marshal(map[string]string{"command": "type not_exist_file_xyz.txt"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result) {
		t.Errorf("result is not valid UTF-8: %q", result)
	}
	if strings.ContainsRune(result, '�') {
		t.Errorf("result contains mojibake: %q", result)
	}
	if !strings.Contains(result, "命令退出异常") {
		t.Errorf("missing exit status note: %q", result)
	}
}

func TestConfigTimeout(t *testing.T) {
	p := New()
	if err := p.Init(plugin.InitContext{Workdir: "C:\\"}, map[string]any{"timeout_seconds": 5}); err != nil {
		t.Fatal(err)
	}
	if p.timeout.Seconds() != 5 {
		t.Errorf("timeout = %v", p.timeout)
	}
	if p.workdir != "C:\\" {
		t.Errorf("workdir = %q", p.workdir)
	}
}
