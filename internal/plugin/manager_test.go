package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	name     string
	prompt   string
	tools    []Tool
	initErr  error
	inited   bool
	lastICtx InitContext // 最近一次 Init 收到的运行环境
}

func (p *fakePlugin) Name() string        { return p.name }
func (p *fakePlugin) Description() string { return "测试插件 " + p.name }
func (p *fakePlugin) Init(ictx InitContext, _ map[string]any) error {
	p.lastICtx = ictx
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

// configurablePlugin 记录最近一次 Init 收到的配置，用于验证配置生效链路。
type configurablePlugin struct {
	fakePlugin
	lastCfg map[string]any
}

func (p *configurablePlugin) Init(ictx InitContext, cfg map[string]any) error {
	p.lastCfg = cfg
	return p.fakePlugin.Init(ictx, cfg)
}

func (p *configurablePlugin) ConfigFields() []ConfigField {
	return []ConfigField{
		{Key: "size", Label: "大小", Type: FieldInt, Default: 100, Min: IntPtr(10), Max: IntPtr(1000)},
		{Key: "mode", Label: "模式", Type: FieldSelect, Default: "fast",
			Options: []ConfigOption{{Value: "fast", Label: "快"}, {Value: "slow", Label: "慢"}}},
	}
}

func newConfigurable(name string) *configurablePlugin {
	return &configurablePlugin{fakePlugin: fakePlugin{name: name}}
}

func TestListExposesConfigFieldsAndValues(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(newConfigurable("c"), PluginConfig{Enabled: true, Config: map[string]any{"size": 200}})

	st := m.List()[0]
	if len(st.ConfigFields) != 2 {
		t.Fatalf("expected 2 config fields, got %d", len(st.ConfigFields))
	}
	if st.Config["size"] != 200 {
		t.Errorf("size = %v, want 200（配置文件值）", st.Config["size"])
	}
	if st.Config["mode"] != "fast" {
		t.Errorf("mode = %v, want fast（默认值）", st.Config["mode"])
	}
}

func TestSetConfigAppliesAndValidates(t *testing.T) {
	m := NewManager(InitContext{}, "")
	p := newConfigurable("c")
	m.Register(p, PluginConfig{Enabled: true})

	// 字符串形式的数字应被接受并转成整数，并立刻以新配置重新 Init
	if err := m.SetConfig("c", map[string]any{"size": "300", "mode": "slow"}); err != nil {
		t.Fatal(err)
	}
	if p.lastCfg["size"] != 300 || p.lastCfg["mode"] != "slow" {
		t.Errorf("Init 收到的配置 = %v", p.lastCfg)
	}

	// 越界与非法候选值应被拒绝，且不改动已生效配置
	if err := m.SetConfig("c", map[string]any{"size": 5000}); err == nil {
		t.Error("超出上限应报错")
	}
	if err := m.SetConfig("c", map[string]any{"size": 100, "mode": "x"}); err == nil {
		t.Error("非法候选值应报错")
	}
	if m.List()[0].Config["size"] != 300 {
		t.Errorf("失败的保存不应改动配置: %v", m.List()[0].Config)
	}
}

func TestSetConfigRejectsPluginWithoutFields(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "plain"}, PluginConfig{Enabled: true})
	if err := m.SetConfig("plain", map[string]any{"a": 1}); err == nil {
		t.Error("无可配置项的插件应拒绝保存")
	}
	if err := m.SetConfig("missing", nil); err == nil {
		t.Error("不存在的插件应报错")
	}
}

func TestConfigPersistsAcrossRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugins.state.json")

	m1 := NewManager(InitContext{}, statePath)
	m1.Register(newConfigurable("c"), PluginConfig{Enabled: true})
	if err := m1.SetConfig("c", map[string]any{"size": 250, "mode": "slow"}); err != nil {
		t.Fatal(err)
	}

	// 重启：界面上保存的配置优先于配置文件，未涉及的键仍取配置文件值
	m2 := NewManager(InitContext{}, statePath)
	p2 := newConfigurable("c")
	m2.Register(p2, PluginConfig{Enabled: true, Config: map[string]any{"size": 100, "other": "keep"}})
	if p2.lastCfg["size"] != 250 || p2.lastCfg["mode"] != "slow" {
		t.Errorf("持久化配置未生效: %v", p2.lastCfg)
	}
	if p2.lastCfg["other"] != "keep" {
		t.Errorf("配置文件中未被覆盖的键应保留: %v", p2.lastCfg)
	}
}

func TestLoadLegacyStateFormat(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugins.state.json")
	if err := os.WriteFile(statePath, []byte(`{"x": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(InitContext{}, statePath)
	m.Register(&fakePlugin{name: "x"}, PluginConfig{Enabled: true})
	if m.List()[0].Enabled {
		t.Error("旧格式（name -> bool）状态文件应仍然生效")
	}
}

func TestStateDirIsolatedPerPlugin(t *testing.T) {
	base := t.TempDir()
	m := NewManager(InitContext{Workdir: "/w"}, filepath.Join(base, "plugins.state.json"))
	p := newConfigurable("c")
	if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(base, "plugins", "c")
	if p.lastICtx.StateDir != want {
		t.Errorf("Register 时 StateDir = %q, want %q", p.lastICtx.StateDir, want)
	}
	if p.lastICtx.Workdir != "/w" {
		t.Errorf("Workdir 不应被改动: %q", p.lastICtx.Workdir)
	}

	// SetConfig 与 SetEnabled 触发的重新 Init 也必须带上同一个目录
	p.lastICtx = InitContext{}
	if err := m.SetConfig("c", map[string]any{"size": 200}); err != nil {
		t.Fatal(err)
	}
	if p.lastICtx.StateDir != want {
		t.Errorf("SetConfig 后 StateDir = %q, want %q", p.lastICtx.StateDir, want)
	}

	m2 := NewManager(InitContext{}, filepath.Join(base, "plugins.state.json"))
	q := &fakePlugin{name: "other"}
	m2.Register(q, PluginConfig{Enabled: true})
	if q.lastICtx.StateDir == want {
		t.Error("不同插件不应共用同一个 StateDir")
	}
}

func TestStateDirEmptyWithoutStatePath(t *testing.T) {
	m := NewManager(InitContext{Workdir: "/w"}, "")
	p := &fakePlugin{name: "x"}
	m.Register(p, PluginConfig{Enabled: true})
	if p.lastICtx.StateDir != "" {
		t.Errorf("未配置状态文件时 StateDir 应为空，得到 %q", p.lastICtx.StateDir)
	}
}

func TestRegisterRecordsSource(t *testing.T) {
	m := NewManager(InitContext{}, "")
	if err := m.Register(&fakePlugin{name: "inner"}, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterExternal(&fakePlugin{name: "outer"}, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range m.List() {
		got[st.Name] = st.Source
	}
	if got["inner"] != SourceBuiltin || got["outer"] != SourceExternal {
		t.Errorf("来源记录错误: %v", got)
	}
	if SourceLabel(SourceBuiltin) != "内置" || SourceLabel(SourceExternal) != "外源" {
		t.Error("来源显示名不对")
	}
	// 外源插件同样受重名与工具冲突校验
	if err := m.RegisterExternal(&fakePlugin{name: "inner"}, PluginConfig{Enabled: true}); err == nil {
		t.Error("外源插件重名应被拒绝")
	}
	if err := m.RegisterExternal(&fakePlugin{name: "Bad"}, PluginConfig{Enabled: true}); err == nil {
		t.Error("外源插件名同样要校验")
	}
}

func TestRegisterRejectsInvalidName(t *testing.T) {
	for _, name := range []string{"", "Foo", "a-b", "1a", "../evil", "a b", "a/b"} {
		m := NewManager(InitContext{}, "")
		if err := m.Register(&fakePlugin{name: name}, PluginConfig{Enabled: true}); err == nil {
			t.Errorf("插件名 %q 应被拒绝", name)
		}
	}
	m := NewManager(InitContext{}, "")
	if err := m.Register(&fakePlugin{name: "read_file2"}, PluginConfig{Enabled: true}); err != nil {
		t.Errorf("合法插件名被拒绝: %v", err)
	}
}

// lifecyclePlugin 实现 Lifecycle，用于验证压缩事件广播。
type lifecyclePlugin struct {
	fakePlugin
	note  string
	err   error
	calls int
	got   CompactEvent
}

func (p *lifecyclePlugin) OnCompact(_ context.Context, ev CompactEvent) (string, error) {
	p.calls++
	p.got = ev
	return p.note, p.err
}

func TestNotifyCompact(t *testing.T) {
	m := NewManager(InitContext{}, "")
	on := &lifecyclePlugin{fakePlugin: fakePlugin{name: "a"}, note: "注记A"}
	off := &lifecyclePlugin{fakePlugin: fakePlugin{name: "b"}, note: "注记B"}
	boom := &lifecyclePlugin{fakePlugin: fakePlugin{name: "c"}, note: "注记C", err: fmt.Errorf("boom")}
	silent := &lifecyclePlugin{fakePlugin: fakePlugin{name: "d"}} // 返回空串
	plain := &fakePlugin{name: "e"}                               // 未实现 Lifecycle

	m.Register(on, PluginConfig{Enabled: true})
	m.Register(off, PluginConfig{Enabled: false})
	m.Register(boom, PluginConfig{Enabled: true})
	m.Register(silent, PluginConfig{Enabled: true})
	m.Register(plain, PluginConfig{Enabled: true})

	ev := CompactEvent{SessionID: "s1", Summary: "摘要"}
	notes := m.NotifyCompact(context.Background(), ev)

	if len(notes) != 1 || notes[0] != "注记A" {
		t.Errorf("notes = %v，应只收集启用插件的非空注记且跳过报错的插件", notes)
	}
	if off.calls != 0 {
		t.Error("禁用的插件不应收到压缩事件")
	}
	if boom.calls != 1 {
		t.Error("报错的插件应被调用过，且错误不阻断后续插件")
	}
	if on.got.SessionID != "s1" || on.got.Summary != "摘要" {
		t.Errorf("事件内容未透传: %+v", on.got)
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
