package dualpersona

import (
	"context"
	"errors"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/plugin"
)

// newTranslatingPlugin 带辅助模型调用初始化插件。
func newTranslatingPlugin(t *testing.T, cfg map[string]any, complete plugin.CompleteFunc) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: t.TempDir(), Complete: complete}, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

// innerCtx 构造一个已裁决为里人格的轮次 ctx。
func innerCtx() context.Context {
	return plugin.WithScope(context.Background(), plugin.Scope{
		Write: personaInner, Read: []string{personaOuter, personaInner},
	})
}

func TestTranslateFailureUsesInnerPersona(t *testing.T) {
	var gotPrompt string
	complete := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "「刚才……想什么呢我。过来。」", nil
	}
	p := newTranslatingPlugin(t, defaultCfg(), complete)

	text, ok := p.TranslateFailure(innerCtx(), plugin.TurnFailure{Err: errors.New("boom")})
	if !ok {
		t.Fatal("里人格轮次应接手转译")
	}
	if text != "刚才……想什么呢我。过来。" {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(gotPrompt, "你另有一面") {
		t.Error("生成台词的提示词应包含里人格设定")
	}
}

func TestTranslateFailureDeclinesOuterTurn(t *testing.T) {
	complete := func(context.Context, string) (string, error) {
		t.Fatal("表人格轮次不该发起生成")
		return "", nil
	}
	p := newTranslatingPlugin(t, defaultCfg(), complete)

	outerCtx := plugin.WithScope(context.Background(), plugin.Scope{Write: personaOuter, Read: []string{personaOuter}})
	if _, ok := p.TranslateFailure(outerCtx, plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("表人格轮次应落回 roleplay")
	}
	// 没有可见域的轮次（未裁决/单人格场景）同样不接手
	if _, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("无标签轮次应落回 roleplay")
	}
}

func TestTranslateFailureDeclinesConfigError(t *testing.T) {
	p := newTranslatingPlugin(t, defaultCfg(), func(context.Context, string) (string, error) {
		t.Fatal("配置类错误不该发起生成")
		return "", nil
	})
	cfgErr := &llm.APIError{Status: 401, Body: "bad key"}
	if _, ok := p.TranslateFailure(innerCtx(), plugin.TurnFailure{Err: cfgErr}); ok {
		t.Fatal("配置类错误必须原样报出")
	}
}

func TestTranslateFailureDeclinesOnGenerateFailure(t *testing.T) {
	complete := func(context.Context, string) (string, error) {
		return "", errors.New("provider down")
	}
	p := newTranslatingPlugin(t, defaultCfg(), complete)
	if _, ok := p.TranslateFailure(innerCtx(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("生成失败应返回不接手，由 roleplay 的兜底续上")
	}
}

func TestTranslateFailureDeclinesWithoutPersonaOrComplete(t *testing.T) {
	// 没配里人格设定
	noPersona := newTranslatingPlugin(t, map[string]any{"to_inner": "暗号"}, func(context.Context, string) (string, error) {
		return "x", nil
	})
	if _, ok := noPersona.TranslateFailure(innerCtx(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("没有里人格设定不该接手")
	}
	// 没有辅助调用能力
	noComplete := newTranslatingPlugin(t, defaultCfg(), nil)
	if _, ok := noComplete.TranslateFailure(innerCtx(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("Complete 不可用时不该接手")
	}
}
