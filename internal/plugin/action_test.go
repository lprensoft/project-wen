package plugin

import (
	"context"
	"strings"
	"testing"
)

// actionPlugin 是实现 Actionable 的假插件。
type actionPlugin struct {
	fakePlugin
	started []string
	state   ActionState
}

func (p *actionPlugin) Actions() []ActionDef {
	return []ActionDef{{Key: "bind", Label: "绑定"}}
}

func (p *actionPlugin) StartAction(_ context.Context, key string) error {
	p.started = append(p.started, key)
	return nil
}

func (p *actionPlugin) ActionState(key string) (ActionState, error) {
	if key != "bind" {
		return ActionState{}, errNoSuchAction(key)
	}
	return p.state, nil
}

type errNoSuchAction string

func (e errNoSuchAction) Error() string { return "没有操作 " + string(e) }

// 启用且初始化成功的 Actionable 插件：List 暴露 Actions，Start/State 委派到插件。
func TestManagerActionDelegation(t *testing.T) {
	m := NewManager(InitContext{}, "")
	p := &actionPlugin{fakePlugin: fakePlugin{name: "act"}}
	p.state = ActionState{Status: ActionPending, Message: "进行中"}
	if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	list := m.List()
	if len(list) != 1 || len(list[0].Actions) != 1 || list[0].Actions[0].Key != "bind" {
		t.Fatalf("List 应暴露操作入口，得到 %+v", list)
	}

	if err := m.StartAction(context.Background(), "act", "bind"); err != nil {
		t.Fatal(err)
	}
	if len(p.started) != 1 || p.started[0] != "bind" {
		t.Fatalf("StartAction 未委派到插件: %v", p.started)
	}
	st, err := m.ActionState("act", "bind")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != ActionPending || st.Message != "进行中" {
		t.Fatalf("ActionState = %+v", st)
	}
}

// 禁用插件、未实现 Actionable 的插件、不存在的插件都应拒绝。
func TestManagerActionRejections(t *testing.T) {
	m := NewManager(InitContext{}, "")
	disabled := &actionPlugin{fakePlugin: fakePlugin{name: "off"}}
	plain := &fakePlugin{name: "plain"}
	if err := m.Register(disabled, PluginConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(plain, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if err := m.StartAction(context.Background(), "off", "bind"); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("禁用插件应拒绝操作，得到 %v", err)
	}
	if err := m.StartAction(context.Background(), "plain", "bind"); err == nil || !strings.Contains(err.Error(), "没有操作入口") {
		t.Fatalf("未实现 Actionable 应拒绝，得到 %v", err)
	}
	if _, err := m.ActionState("ghost", "bind"); err == nil {
		t.Fatal("不存在的插件应报错")
	}

	// 禁用插件的 Actions 不该出现在列表里
	for _, st := range m.List() {
		if st.Name == "off" && len(st.Actions) > 0 {
			t.Fatal("禁用插件不应暴露操作入口")
		}
	}
}

// ---------- 操作的草稿配置值 ----------

func TestActionValuesRoundTrip(t *testing.T) {
	ctx := WithActionValues(context.Background(), map[string]any{"city": "杭州"})
	got := ActionValuesFrom(ctx)
	if got["city"] != "杭州" {
		t.Errorf("取回的草稿值 = %v", got)
	}
}

// 没带草稿值时返回 nil，不是空 map——调用方按「没有」处理即可。
func TestActionValuesAbsent(t *testing.T) {
	if got := ActionValuesFrom(context.Background()); got != nil {
		t.Errorf("未附带时应为 nil，实际 %v", got)
	}
	ctx := WithActionValues(context.Background(), nil)
	if got := ActionValuesFrom(ctx); got != nil {
		t.Errorf("空草稿不该占位，实际 %v", got)
	}
}

// 草稿里填了就用草稿的，没填（或填了空白）就回落到已保存的值。
func TestActionValueOr(t *testing.T) {
	saved := "已保存"
	cases := []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"没有草稿", nil, saved},
		{"草稿里没这一项", map[string]any{"other": "x"}, saved},
		{"草稿是空串", map[string]any{"city": ""}, saved},
		{"草稿只有空白", map[string]any{"city": "   "}, saved},
		{"草稿有值", map[string]any{"city": "苏州"}, "苏州"},
		{"草稿两侧有空白", map[string]any{"city": " 苏州 "}, "苏州"},
	}
	for _, c := range cases {
		if got := ActionValueOr(c.values, "city", saved); got != c.want {
			t.Errorf("%s: = %q，期望 %q", c.name, got, c.want)
		}
	}
}
