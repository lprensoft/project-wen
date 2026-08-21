package plugin

import "testing"

// dayPlugin 是只会答某一天的测试插件。
type dayPlugin struct {
	name  string
	lines []string
	date  string // 记下被问的是哪一天
}

func (p *dayPlugin) Name() string                           { return p.name }
func (p *dayPlugin) Description() string                    { return "测试用" }
func (p *dayPlugin) Init(InitContext, map[string]any) error { return nil }
func (p *dayPlugin) SystemPrompt() string                   { return "" }
func (p *dayPlugin) Tools() []Tool                          { return nil }
func (p *dayPlugin) DayFacts(date string) []string          { p.date = date; return p.lines }

// panicDay 用来确认一个插件炸了不会连累整份汇总。
type panicDay struct{ dayPlugin }

func (panicDay) DayFacts(string) []string { panic("测试用的 panic") }

func TestDayFactsAggregates(t *testing.T) {
	a := &dayPlugin{name: "aaa", lines: []string{"天气：小雨 24~31℃", ""}}
	b := &dayPlugin{name: "bbb", lines: []string{"心情：低落"}}
	m := NewManager(InitContext{}, "")
	for _, p := range []Plugin{a, b} {
		if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	got := m.DayFacts("2026-08-21")
	if len(got) != 2 || got[0] != "天气：小雨 24~31℃" || got[1] != "心情：低落" {
		t.Fatalf("汇总不符（空行该被丢掉、顺序按注册序）: %v", got)
	}
	if a.date != "2026-08-21" || b.date != "2026-08-21" {
		t.Fatalf("被问的日期没传对: %q %q", a.date, b.date)
	}
}

func TestDayFactsSkipsDisabledAndPanicking(t *testing.T) {
	off := &dayPlugin{name: "off_one", lines: []string{"不该出现"}}
	boom := &panicDay{dayPlugin{name: "boom"}}
	ok := &dayPlugin{name: "ok_one", lines: []string{"照常"}}
	m := NewManager(InitContext{}, "")
	if err := m.Register(off, PluginConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Plugin{boom, ok} {
		if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	got := m.DayFacts("2026-08-21")
	if len(got) != 1 || got[0] != "照常" {
		t.Fatalf("禁用的插件不该被问，炸掉的不该连累其余: %v", got)
	}
}
