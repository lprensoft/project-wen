package plugin

import (
	"context"
	"testing"
)

// stopPlugin 记录 Init 与 Stop 的调用次数；Stop 里可选地回调 Manager 验证不死锁。
type stopPlugin struct {
	inits   int
	stops   int
	onStop  func()
	lastCfg map[string]any
}

func (p *stopPlugin) Name() string        { return "stop_plugin" }
func (p *stopPlugin) Description() string { return "测试停止钩子" }
func (p *stopPlugin) Init(_ InitContext, cfg map[string]any) error {
	p.inits++
	p.lastCfg = cfg
	return nil
}
func (p *stopPlugin) SystemPrompt() string { return "" }
func (p *stopPlugin) Tools() []Tool        { return nil }
func (p *stopPlugin) Stop() {
	p.stops++
	if p.onStop != nil {
		p.onStop()
	}
}
func (p *stopPlugin) ConfigFields() []ConfigField {
	return []ConfigField{{Key: "n", Label: "数值", Type: FieldInt, Default: 1}}
}

// 禁用调用 Stop，重新启用会重新 Init；Stop 中回调 Manager 不死锁。
func TestStoppableEnableDisable(t *testing.T) {
	m := NewManager(InitContext{}, "")
	p := &stopPlugin{}
	p.onStop = func() { m.List() } // Stop 在锁外调用，回读 Manager 必须可行
	if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if p.inits != 1 {
		t.Fatalf("注册启用后 Init 次数 = %d，期望 1", p.inits)
	}

	if err := m.SetEnabled("stop_plugin", false); err != nil {
		t.Fatal(err)
	}
	if p.stops != 1 {
		t.Fatalf("禁用后 Stop 次数 = %d，期望 1", p.stops)
	}

	if err := m.SetEnabled("stop_plugin", true); err != nil {
		t.Fatal(err)
	}
	if p.inits != 2 {
		t.Fatalf("重新启用后 Init 次数 = %d，期望 2（停止后必须重新 Init）", p.inits)
	}
}

// SetConfig 对已初始化的 Stoppable 插件先 Stop 再以新配置 Init。
func TestStoppableSetConfig(t *testing.T) {
	m := NewManager(InitContext{}, "")
	p := &stopPlugin{}
	if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetConfig("stop_plugin", map[string]any{"n": 7}); err != nil {
		t.Fatal(err)
	}
	if p.stops != 1 || p.inits != 2 {
		t.Fatalf("SetConfig 后 stops=%d inits=%d，期望 1/2", p.stops, p.inits)
	}
	if got := CfgInt(p.lastCfg, "n", 0); got != 7 {
		t.Fatalf("新配置未生效，n=%d", got)
	}
}

// StopAll 停掉所有已初始化的 Stoppable 插件；禁用的（从未 Init 的）不受影响。
func TestStopAll(t *testing.T) {
	m := NewManager(InitContext{}, "")
	a, b := &stopPlugin{}, &stopPlugin{}
	// 同名会冲突，b 换个壳
	if err := m.Register(a, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(renamed{b, "stop_plugin_b"}, PluginConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	if a.stops != 1 {
		t.Fatalf("启用插件应被 StopAll 停止，stops=%d", a.stops)
	}
	if b.stops != 0 {
		t.Fatalf("未初始化的插件不该被 Stop，stops=%d", b.stops)
	}
}

// renamed 给测试插件换名字（工具与行为透传）。
type renamed struct {
	*stopPlugin
	name string
}

func (r renamed) Name() string { return r.name }

// RunTurn 经 Manager 下发时自动带上发起方标记。
func TestInitCtxInjectsOrigin(t *testing.T) {
	var gotOrigin string
	base := InitContext{RunTurn: func(ctx context.Context, sessionID, input string) (string, error) {
		gotOrigin = TurnOriginFrom(ctx)
		return "", nil
	}}
	m := NewManager(base, "")
	ictx := m.initCtxFor("some_plugin")
	if _, err := ictx.RunTurn(context.Background(), "sid", "in"); err != nil {
		t.Fatal(err)
	}
	if gotOrigin != "some_plugin" {
		t.Fatalf("发起方 = %q，期望 some_plugin", gotOrigin)
	}
}
