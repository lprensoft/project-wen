package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// depPlugin 声明依赖与冲突，用于验证依赖机制。
type depPlugin struct {
	fakePlugin
	requires  []string
	conflicts []string
}

func (p *depPlugin) Requires() []string  { return p.requires }
func (p *depPlugin) Conflicts() []string { return p.conflicts }

func newDep(name string, requires ...string) *depPlugin {
	return &depPlugin{fakePlugin: fakePlugin{name: name}, requires: requires}
}

func statusOf(m *Manager, name string) Status {
	for _, st := range m.List() {
		if st.Name == name {
			return st
		}
	}
	return Status{}
}

func TestEnableBlockedByUnmetDependency(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: false})
	m.Register(newDep("top", "base"), PluginConfig{Enabled: false})

	err := m.SetEnabled("top", true)
	if err == nil {
		t.Fatal("依赖未启用时应拒绝启用")
	}
	if !strings.Contains(err.Error(), "base") {
		t.Errorf("错误文案应指明缺哪个依赖: %v", err)
	}
	if st := statusOf(m, "top"); st.Enabled || !slices.Equal(st.Unmet, []string{"base"}) {
		t.Errorf("top 状态不对: enabled=%v unmet=%v", st.Enabled, st.Unmet)
	}

	if err := m.SetEnabled("base", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled("top", true); err != nil {
		t.Fatalf("依赖满足后应可启用: %v", err)
	}
	if st := statusOf(m, "top"); len(st.Unmet) != 0 || !slices.Equal(st.Requires, []string{"base"}) {
		t.Errorf("top 状态不对: %+v", st)
	}
}

func TestEnableBlockedByMissingDependency(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(newDep("top", "never_registered"), PluginConfig{Enabled: false})
	if err := m.SetEnabled("top", true); err == nil {
		t.Fatal("依赖未注册时也应拒绝启用")
	}
}

func TestEnableCheckedBeforeInit(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: false})
	top := newDep("top", "base")
	m.Register(top, PluginConfig{Enabled: false})

	_ = m.SetEnabled("top", true)
	if top.inited {
		t.Error("依赖不满足时不应调用 Init（不能产生副作用）")
	}
}

func TestDisableRejectedWhileDepended(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: true})
	m.Register(newDep("top", "base"), PluginConfig{Enabled: true})

	err := m.SetEnabled("base", false)
	if err == nil {
		t.Fatal("被依赖的插件不应能直接禁用")
	}
	if !strings.Contains(err.Error(), "top") {
		t.Errorf("错误文案应指明该先关谁: %v", err)
	}
	if !statusOf(m, "base").Enabled {
		t.Error("被拒绝的禁用不应改动状态")
	}

	// 自上而下关就没问题
	if err := m.SetEnabled("top", false); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled("base", false); err != nil {
		t.Fatalf("依赖方已关闭后应可禁用: %v", err)
	}
}

func TestResolveIndependentOfRegistrationOrder(t *testing.T) {
	// 依赖方先注册：register 阶段无法校验，必须靠 Resolve 收尾
	m := NewManager(InitContext{}, "")
	m.Register(newDep("top", "base"), PluginConfig{Enabled: true})
	m.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: false})

	if !statusOf(m, "top").Enabled {
		t.Fatal("Resolve 之前 top 应仍是配置里的启用状态")
	}
	m.Resolve()
	if statusOf(m, "top").Enabled {
		t.Error("Resolve 应关掉依赖未满足的插件")
	}
}

func TestResolveIsTransitive(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: false})
	m.Register(newDep("mid", "base"), PluginConfig{Enabled: true})
	m.Register(newDep("top", "mid"), PluginConfig{Enabled: true})

	m.Resolve()
	if statusOf(m, "mid").Enabled || statusOf(m, "top").Enabled {
		t.Error("关掉 base 应连带让 mid 与 top 都判定为依赖未满足")
	}
}

func TestResolveKeepsUserIntentPersisted(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "plugins.state.json")

	// 用户开着 top，但这次启动时 base 是关的
	m1 := NewManager(InitContext{}, statePath)
	m1.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: false})
	m1.Register(newDep("top", "base"), PluginConfig{Enabled: true})
	m1.Resolve()
	if statusOf(m1, "top").Enabled {
		t.Fatal("依赖未满足时 top 应被强制关闭")
	}
	// 任何后续的开关操作都会全量重写状态文件，此时不能把强制关闭固化成用户意图
	if err := m1.SetEnabled("base", true); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]persistedEntry
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if !state["top"].Enabled {
		t.Error("被强制关闭的插件应按「用户想开」持久化，否则依赖恢复后也回不来")
	}

	// 重启且依赖已满足：top 自动回来
	m2 := NewManager(InitContext{}, statePath)
	m2.Register(&fakePlugin{name: "base"}, PluginConfig{Enabled: true})
	m2.Register(newDep("top", "base"), PluginConfig{Enabled: false})
	m2.Resolve()
	if !statusOf(m2, "top").Enabled {
		t.Error("依赖恢复后 top 应自动回到启用状态")
	}
}

func TestDependencyCycleDetected(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(newDep("a", "b"), PluginConfig{Enabled: true})
	m.Register(newDep("b", "a"), PluginConfig{Enabled: true})

	// 不能栈溢出，且成环的插件一律判为依赖不满足
	m.Resolve()
	if statusOf(m, "a").Enabled || statusOf(m, "b").Enabled {
		t.Error("成环的插件应保持禁用")
	}
	if err := m.SetEnabled("a", true); err == nil {
		t.Error("成环的插件不应能启用")
	}
}

func TestSelfDependencyDetected(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(newDep("solo", "solo"), PluginConfig{Enabled: true})
	m.Resolve()
	if statusOf(m, "solo").Enabled {
		t.Error("自依赖等同成环，应保持禁用")
	}
}

func TestConflictsReportedSymmetrically(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "shell"}, PluginConfig{Enabled: true})
	isolator := &depPlugin{fakePlugin: fakePlugin{name: "isolator"}, conflicts: []string{"shell"}}
	m.Register(isolator, PluginConfig{Enabled: true})

	if got := statusOf(m, "isolator").Conflicting; !slices.Equal(got, []string{"shell"}) {
		t.Errorf("声明方应看到冲突: %v", got)
	}
	// 只有一方声明，双方都应看到——否则被冲突的那张卡片上什么都没有
	if got := statusOf(m, "shell").Conflicting; !slices.Equal(got, []string{"isolator"}) {
		t.Errorf("被声明方也应看到冲突: %v", got)
	}

	// 冲突只告警不阻止启用
	if err := m.SetEnabled("isolator", false); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled("isolator", true); err != nil {
		t.Errorf("冲突不应阻止启用: %v", err)
	}
	if got := statusOf(m, "shell").Conflicting; len(got) != 1 {
		t.Errorf("重新启用后冲突应重新出现: %v", got)
	}
}

func TestConflictsOnlyCountEnabled(t *testing.T) {
	m := NewManager(InitContext{}, "")
	m.Register(&fakePlugin{name: "shell"}, PluginConfig{Enabled: false})
	m.Register(&depPlugin{fakePlugin: fakePlugin{name: "isolator"}, conflicts: []string{"shell"}},
		PluginConfig{Enabled: true})

	if got := statusOf(m, "isolator").Conflicting; len(got) != 0 {
		t.Errorf("未启用的冲突项不该告警: %v", got)
	}
}

func TestStateFilePermissions(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows 不按 Unix 权限位表达文件权限")
	}
	statePath := filepath.Join(t.TempDir(), "plugins.state.json")
	m := NewManager(InitContext{}, statePath)
	m.Register(&fakePlugin{name: "x"}, PluginConfig{Enabled: true})
	if err := m.SetEnabled("x", false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("状态文件含插件配置，权限应为 0600，得到 %o", perm)
	}
}
