package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WithStateDir 只改插件持久化目录的根，开关状态仍从状态文件读。
func TestWithStateDirOverridesOnlyStateDir(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "plugins.state.json")
	// 状态文件里 a 是关着的：证明开关仍从这里读
	if err := os.WriteFile(statePath, []byte(`{"a":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alt := filepath.Join(dir, "elsewhere")

	m := NewManager(InitContext{}, statePath, WithStateDir(alt))
	a := &fakePlugin{name: "a"}
	b := &fakePlugin{name: "b"}
	if err := m.Register(a, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(b, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if a.inited {
		t.Error("状态文件里关着的插件不该被初始化")
	}
	if want := filepath.Join(alt, "b"); b.lastICtx.StateDir != want {
		t.Errorf("StateDir = %q，期望 %q", b.lastICtx.StateDir, want)
	}
	if strings.HasPrefix(b.lastICtx.StateDir, filepath.Join(dir, "plugins")) {
		t.Error("不该再落到状态文件旁边的 plugins/ 目录")
	}
}

// WithSuppressed 让命中的插件只登记、不启用，且不改写用户意图。
func TestWithSuppressed(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "plugins.state.json")
	if err := os.WriteFile(statePath, []byte(`{"bg":{"enabled":true},"ok":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(InitContext{}, statePath, WithSuppressed(func(p Plugin) bool { return p.Name() == "bg" }))
	bg := &fakePlugin{name: "bg"}
	ok := &fakePlugin{name: "ok"}
	if err := m.Register(bg, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(ok, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if bg.inited {
		t.Error("被压住的插件不该初始化")
	}
	if !ok.inited {
		t.Error("未命中的插件照常启用")
	}
	var enabled []string
	for _, st := range m.List() {
		if st.Enabled {
			enabled = append(enabled, st.Name)
		}
	}
	if len(enabled) != 1 || enabled[0] != "ok" {
		t.Errorf("启用列表 = %v", enabled)
	}

	// 万一状态文件被重写，被压住的插件仍按「用户想开」写回
	m.mu.Lock()
	m.saveStateLocked()
	m.mu.Unlock()
	raw, _ := os.ReadFile(statePath)
	if !strings.Contains(string(raw), `"enabled": true`) || strings.Contains(string(raw), `"enabled": false`) {
		t.Errorf("状态文件应保留用户意图：%s", raw)
	}
}
