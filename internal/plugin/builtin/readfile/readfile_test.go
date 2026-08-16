package readfile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wen/internal/plugin"
)

func TestReadRelativeAndTruncate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("你好世界"), 0o644)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("字", 100)), 0o644)

	p := New()
	if err := p.Init(plugin.InitContext{Workdir: dir}, map[string]any{"max_bytes": 30}); err != nil {
		t.Fatal(err)
	}
	tool := p.Tools()[0]

	args, _ := json.Marshal(map[string]string{"path": "a.txt"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if out != "你好世界" {
		t.Errorf("out = %q", out)
	}

	args, _ = json.Marshal(map[string]string{"path": "big.txt"})
	out, err = tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已截断") {
		t.Errorf("expected truncation note: %q", out)
	}
	// 截断不能切坏多字节字符
	for _, r := range out {
		if r == '�' {
			t.Fatalf("broken rune in output: %q", out)
		}
	}
}

func TestMissingFile(t *testing.T) {
	p := New()
	p.Init(plugin.InitContext{Workdir: t.TempDir()}, nil)
	args, _ := json.Marshal(map[string]string{"path": "nope.txt"})
	if _, err := p.Tools()[0].Execute(context.Background(), args); err == nil {
		t.Fatal("expected error for missing file")
	}
}
