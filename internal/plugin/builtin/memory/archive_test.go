package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
)

func sampleEvent() plugin.CompactEvent {
	return plugin.CompactEvent{
		SessionID: "20260817-103426-8cb8ea",
		Summary:   "这是摘要",
		History: []llm.Message{
			{Role: llm.RoleUser, Content: "用户说的话"},
			{Role: llm.RoleAssistant, Content: "助手的回答", Reasoning: "思考过程",
				ToolCalls: []llm.ToolCall{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)}}},
			{Role: llm.RoleTool, Content: "工具返回的内容"},
		},
	}
}

func archiveFiles(t *testing.T, p *Plugin) []string {
	t.Helper()
	des, err := os.ReadDir(p.snapshot().archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

func TestOnCompactArchivesFullHistory(t *testing.T) {
	p := newTestPlugin(t, nil)
	ev := sampleEvent()

	note, err := p.OnCompact(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	names := archiveFiles(t, p)
	if len(names) != 1 {
		t.Fatalf("应生成 1 个归档，实际 %v", names)
	}
	if !strings.Contains(note, names[0]) {
		t.Errorf("注记应指出归档文件名: %q（文件 %q）", note, names[0])
	}

	raw, err := os.ReadFile(filepath.Join(p.snapshot().archiveDir, names[0]))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	// 归档的意义在于保住原文：每一部分都不能丢，也不做截断
	for _, want := range []string{
		ev.SessionID, "这是摘要", "用户说的话", "助手的回答", "思考过程", "echo", "工具返回的内容",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("归档缺少 %q:\n%s", want, body)
		}
	}
}

func TestOnCompactSkippedWhenDisabled(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"max_archives": 0})
	note, err := p.OnCompact(context.Background(), sampleEvent())
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Errorf("关闭归档时不应返回注记: %q", note)
	}
	if names := archiveFiles(t, p); len(names) != 0 {
		t.Errorf("关闭归档时不应写文件: %v", names)
	}
}

func TestOnCompactSkipsEmptyHistory(t *testing.T) {
	p := newTestPlugin(t, nil)
	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s", Summary: "空"})
	if err != nil || note != "" {
		t.Errorf("空历史应直接跳过: note=%q err=%v", note, err)
	}
}

func TestPruneArchivesKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Hour)
	for i := range 5 {
		name := filepath.Join(dir, fmt.Sprintf("会话%d-x.md", i))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// 故意让文件名字典序与时间序相反，验证淘汰依据是时间而不是文件名
		mod := base.Add(time.Duration(5-i) * time.Hour)
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	// 无关文件不应被计入或删除
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("keep"), 0o644)

	pruneArchives(dir, 2)

	des, _ := os.ReadDir(dir)
	var kept []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".md") {
			kept = append(kept, de.Name())
		}
	}
	if len(kept) != 2 {
		t.Fatalf("应保留 2 个归档，实际 %v", kept)
	}
	for _, n := range kept {
		if n != "会话0-x.md" && n != "会话1-x.md" {
			t.Errorf("保留了较旧的归档: %v", kept)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "readme.txt")); err != nil {
		t.Error("非归档文件不应被删除")
	}
}

func TestOnCompactPrunesToLimit(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"max_archives": 3})
	for i := range 6 {
		ev := sampleEvent()
		ev.SessionID = fmt.Sprintf("会话%d", i)
		if _, err := p.OnCompact(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if names := archiveFiles(t, p); len(names) != 3 {
		t.Errorf("应只保留 3 个归档，实际 %d 个: %v", len(names), names)
	}
}

func TestPluginImplementsLifecycle(t *testing.T) {
	var _ plugin.Lifecycle = New()

	// 通过 Manager 广播也要能走通（这是核心真正调用的路径）
	m := plugin.NewManager(plugin.InitContext{}, filepath.Join(t.TempDir(), "s.json"))
	p := New()
	if err := m.Register(p, plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	notes := m.NotifyCompact(context.Background(), sampleEvent())
	if len(notes) != 1 || !strings.Contains(notes[0], "已归档") {
		t.Errorf("Manager 广播未收到归档注记: %v", notes)
	}
}
