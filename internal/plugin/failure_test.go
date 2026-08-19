package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTranslator 是实现了 FailureTranslator 的假插件。
type fakeTranslator struct {
	fakePlugin
	text   string
	ok     bool
	called bool
}

func (p *fakeTranslator) TranslateFailure(context.Context, TurnFailure) (string, bool) {
	p.called = true
	return p.text, p.ok
}

func TestTranslateFailureSingleOwner(t *testing.T) {
	m := NewManager(InitContext{}, "")
	first := &fakeTranslator{fakePlugin: fakePlugin{name: "first"}, text: "第一句", ok: true}
	second := &fakeTranslator{fakePlugin: fakePlugin{name: "second"}, text: "第二句", ok: true}
	for _, p := range []*fakeTranslator{first, second} {
		if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	// 逆注册序征询：靠后注册的转译者（叠加语义里的最上层）先拿到机会
	text, ok := m.TranslateFailure(context.Background(), TurnFailure{SessionID: "s", Err: errors.New("boom")})
	if !ok || text != "第二句" {
		t.Fatalf("TranslateFailure = %q, %v，应由后注册者胜出", text, ok)
	}
	// 单所有者但不 break：其余转译者仍被调用（以便记日志），只是结果被忽略
	if !first.called {
		t.Error("first translator should still be consulted")
	}
}

func TestTranslateFailureSkipsDecliners(t *testing.T) {
	m := NewManager(InitContext{}, "")
	// accept 注册在最前：逆序征询下它最后被问到，前两个都不接手时才轮到它
	accept := &fakeTranslator{fakePlugin: fakePlugin{name: "accept"}, text: " 接住了 ", ok: true}
	emptyText := &fakeTranslator{fakePlugin: fakePlugin{name: "empty_text"}, text: "  ", ok: true}
	decline := &fakeTranslator{fakePlugin: fakePlugin{name: "decline"}, ok: false}
	for _, p := range []*fakeTranslator{accept, emptyText, decline} {
		if err := m.Register(p, PluginConfig{Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	text, ok := m.TranslateFailure(context.Background(), TurnFailure{Err: errors.New("boom")})
	if !ok || text != "接住了" {
		t.Fatalf("TranslateFailure = %q, %v（应跳过不接手与空文本者，并去除首尾空白）", text, ok)
	}
}

func TestTranslateFailureIgnoresDisabled(t *testing.T) {
	m := NewManager(InitContext{}, "")
	off := &fakeTranslator{fakePlugin: fakePlugin{name: "off"}, text: "不该出现", ok: true}
	if err := m.Register(off, PluginConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}

	if text, ok := m.TranslateFailure(context.Background(), TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatalf("disabled translator should not be consulted, got %q", text)
	}
	if off.called {
		t.Error("disabled translator was called")
	}
}

func TestTranslateFailureCapsLength(t *testing.T) {
	m := NewManager(InitContext{}, "")
	long := &fakeTranslator{fakePlugin: fakePlugin{name: "long"}, text: strings.Repeat("啰", 600), ok: true}
	if err := m.Register(long, PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	text, ok := m.TranslateFailure(context.Background(), TurnFailure{Err: errors.New("boom")})
	if !ok {
		t.Fatal("expected translation")
	}
	if got := len([]rune(text)); got != failureTextMaxRunes {
		t.Errorf("len = %d, want %d", got, failureTextMaxRunes)
	}
}
