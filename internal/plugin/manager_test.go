package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

type fakeTool struct{ name string }

func (t fakeTool) Name() string            { return t.name }
func (t fakeTool) Description() string     { return "测试工具" }
func (t fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t fakeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

type fakePlugin struct {
	name    string
	prompt  string
	tools   []Tool
	initErr error
	inited  bool
}

func (p *fakePlugin) Name() string        { return p.name }
func (p *fakePlugin) Description() string { return "测试插件 " + p.name }
func (p *fakePlugin) Init(InitContext, map[string]any) error {
	if p.initErr != nil {
		return p.initErr
	}
	p.inited = true
	return nil
}
func (p *fakePlugin) SystemPrompt() string { return p.prompt }
func (p *fakePlugin) Tools() []Tool        { return p.tools }

func TestRegisterEnableAggregate(t *testing.T) {
	m := NewManager(InitContext{}, "")
	a := &fakePlugin{name: "a", prompt: "提示A", tools: []Tool{fakeTool{"tool_a"}}}
	b := &fakePlugin{name: "b", tools: []Tool{fakeTool{"tool_b"}}}

	if err := m.Register(a, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(b, PluginConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if !a.inited || b.inited {
		t.Errorf("init state wrong: a=%v b=%v", a.inited, b.inited)
	}
	if got := m.EnabledTools(); len(got) != 1 || got[0].Name() != "tool_a" {
		t.Errorf("EnabledTools = %v", got)
	}
	if got := m.SystemPrompts(); len(got) != 1 || got[0] != "提示A" {
		t.Errorf("SystemPrompts = %v", got)
	}
	if _, ok := m.FindTool("tool_b"); ok {
		t.Error("disabled plugin tool should not be found")
	}

	// 运行时启用 b
	if err := m.SetEnabled("b", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.FindTool("tool_b"); !ok {
		t.Error("tool_b should be found after enable")
	}
	if len(m.EnabledTools()) != 2 {
		t.Errorf("expected 2 tools, got %d", len(m.EnabledTools()))
	}
}

func TestToolNameConflictRejected(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "a", tools: []Tool{fakeTool{"dup"}}}, PluginConfig{Enabled: true})
	err := m.Register(&fakePlugin{name: "b", tools: []Tool{fakeTool{"dup"}}}, PluginConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestInitFailureKeepsDisabled(t *testing.T) {
	m := NewManager(InitContext{}, "")
	bad := &fakePlugin{name: "bad", initErr: fmt.Errorf("boom")}
	if err := m.Register(bad, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].Enabled {
		t.Error("plugin with failing Init should stay disabled")
	}
	if err := m.SetEnabled("bad", true); err == nil {
		t.Error("SetEnabled should surface Init error")
	}
}

func TestStatePersistOverridesConfig(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugins.state.json")

	m1 := NewManager(InitContext{}, statePath)
	m1.Register(&fakePlugin{name: "x"}, PluginConfig{Enabled: true})
	if err := m1.SetEnabled("x", false); err != nil {
		t.Fatal(err)
	}

	// 重启：配置初始 enabled=true，但持久化状态应覆盖为 false
	m2 := NewManager(InitContext{}, statePath)
	m2.Register(&fakePlugin{name: "x"}, PluginConfig{Enabled: true})
	if m2.List()[0].Enabled {
		t.Error("persisted disabled state should override config initial value")
	}
}
