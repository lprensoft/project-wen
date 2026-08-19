package roleplay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/plugin"
)

// initWithComplete 以给定的辅助模型调用初始化插件。
func initWithComplete(t *testing.T, cfg map[string]any, complete plugin.CompleteFunc) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(plugin.InitContext{Complete: complete}, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranslateFailureNeedsPersona(t *testing.T) {
	p := newTestPlugin(t, nil) // 没配角色设定
	if _, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("没有角色设定不该接手转译")
	}
}

func TestTranslateFailureSkipsConfigErrors(t *testing.T) {
	p := initWithComplete(t, map[string]any{"persona": "某个角色"}, nil)
	cfgErr := &llm.APIError{Status: 401, Body: "bad key"}
	if _, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: cfgErr}); ok {
		t.Fatal("配置类错误必须原样报出，不该转译")
	}
}

func TestTranslateFailureUsesComplete(t *testing.T) {
	var gotPrompt string
	complete := func(_ context.Context, prompt string) (string, error) {
		gotPrompt = prompt
		return "「嗯……我刚才是不是走神了？」", nil
	}
	p := initWithComplete(t, map[string]any{"persona": "林绘，说话轻但不软"}, complete)

	text, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: &llm.APIError{Status: 400, Body: "risk"}})
	if !ok {
		t.Fatal("expected translation")
	}
	// 包裹引号被剥掉
	if text != "嗯……我刚才是不是走神了？" {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(gotPrompt, "林绘") {
		t.Error("生成台词的提示词应包含角色设定")
	}
	// 失败原文刻意不进提示词：拦截类失败回传原文只会再被拦一次
	if strings.Contains(gotPrompt, "risk") {
		t.Error("失败原因不该出现在生成提示词里")
	}
}

func TestTranslateFailureFallsBackToStaticLine(t *testing.T) {
	complete := func(context.Context, string) (string, error) {
		return "", errors.New("provider down")
	}
	p := initWithComplete(t, map[string]any{"persona": "某个角色"}, complete)

	text, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: errors.New("boom")})
	if !ok || text != defaultFailureLine {
		t.Errorf("text = %q, ok = %v，生成失败应落到兜底台词", text, ok)
	}
}

func TestTranslateFailureNilCompleteUsesFallback(t *testing.T) {
	p := initWithComplete(t, map[string]any{"persona": "某个角色"}, nil)
	text, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: errors.New("boom")})
	if !ok || text != defaultFailureLine {
		t.Errorf("text = %q, ok = %v", text, ok)
	}
}

func TestTranslateFailureCanBeDisabled(t *testing.T) {
	p := initWithComplete(t, map[string]any{"persona": "某个角色", "translate_failures": false}, nil)
	if _, ok := p.TranslateFailure(context.Background(), plugin.TurnFailure{Err: errors.New("boom")}); ok {
		t.Fatal("开关关闭时不该接手")
	}
}

func TestClipLine(t *testing.T) {
	if got := clipLine("  “你来啦。”  "); got != "你来啦。" {
		t.Errorf("clipLine = %q", got)
	}
	long := strings.Repeat("话", failureLineMaxRunes+50)
	if got := clipLine(long); len([]rune(got)) != failureLineMaxRunes {
		t.Errorf("len = %d", len([]rune(got)))
	}
}
