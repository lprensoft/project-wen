package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/plugin"
	"wen/internal/session"
)

// erroringProvider 先流出一段半截文本，然后以错误事件收场。
type erroringProvider struct {
	partial string
	err     error
}

func (p *erroringProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, r := range p.partial {
			ch <- llm.StreamEvent{Type: llm.EventContentDelta, Content: string(r)}
		}
		ch <- llm.StreamEvent{Type: llm.EventError, Err: p.err}
	}()
	return ch, nil
}

// translatorPlugin 是实现了失败转译的测试插件。
type translatorPlugin struct {
	text   string
	ok     bool
	gotErr error
}

func (p *translatorPlugin) Name() string                                  { return "fake_translator" }
func (p *translatorPlugin) Description() string                           { return "测试转译" }
func (p *translatorPlugin) Init(plugin.InitContext, map[string]any) error { return nil }
func (p *translatorPlugin) SystemPrompt() string                          { return "" }
func (p *translatorPlugin) Tools() []plugin.Tool                          { return nil }
func (p *translatorPlugin) TranslateFailure(_ context.Context, ev plugin.TurnFailure) (string, bool) {
	p.gotErr = ev.Err
	return p.text, p.ok
}

func TestFailureTranslatedOnInteractiveTurn(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	cause := errors.New("boom")
	provider := &erroringProvider{partial: "半截", err: cause}
	tr := &translatorPlugin{text: "（愣了一下）……刚才说到哪了？", ok: true}

	ag := New(provider, newTestManager(t, tr), store, Options{Model: "test", MaxTurns: 3})

	var deltas string
	var sawError, sawDone bool
	ag.Run(plugin.WithInteractive(context.Background()), meta.ID, "hi", func(ev Event) {
		switch ev.Type {
		case EventDelta:
			deltas += ev.Content
		case EventError:
			sawError = true
		case EventDone:
			sawDone = true
		}
	})

	if sawError || !sawDone {
		t.Fatalf("sawError=%v sawDone=%v，转译成功应按正常轮次收尾", sawError, sawDone)
	}
	if tr.gotErr != cause {
		t.Errorf("translator got err %v, want %v", tr.gotErr, cause)
	}
	// 半截文本已经上屏，台词另起一段接在后面
	if !strings.HasSuffix(deltas, "\n\n（愣了一下）……刚才说到哪了？") {
		t.Errorf("deltas = %q", deltas)
	}

	_, msgs, _ := store.Get(meta.ID)
	// user + assistant（台词）+ notice 共 3 条；半截文本不落盘
	if len(msgs) != 3 {
		t.Fatalf("stored %d messages, want 3: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != llm.RoleAssistant || msgs[1].Content != "（愣了一下）……刚才说到哪了？" {
		t.Errorf("assistant message = %+v", msgs[1])
	}
	if msgs[2].Kind != session.KindNotice || !strings.Contains(msgs[2].Content, "boom") {
		t.Errorf("notice message = %+v，应含原始错误", msgs[2])
	}
}

func TestFailureNotTranslatedWithoutInteractive(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &erroringProvider{err: errors.New("boom")}
	tr := &translatorPlugin{text: "不该出现", ok: true}

	ag := New(provider, newTestManager(t, tr), store, Options{Model: "test", MaxTurns: 3})

	var sawError bool
	ag.Run(context.Background(), meta.ID, "hi", func(ev Event) {
		if ev.Type == EventError {
			sawError = true
		}
	})
	if !sawError {
		t.Fatal("后台轮次不该转译，应照常报错")
	}
	_, msgs, _ := store.Get(meta.ID)
	// 只有 user 一条：没有台词、没有注记
	if len(msgs) != 1 {
		t.Fatalf("stored %d messages, want 1: %+v", len(msgs), msgs)
	}
}

func TestFailureFallsThroughWhenTranslatorDeclines(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &erroringProvider{err: errors.New("boom")}
	tr := &translatorPlugin{ok: false}

	ag := New(provider, newTestManager(t, tr), store, Options{Model: "test", MaxTurns: 3})

	var errText string
	ag.Run(plugin.WithInteractive(context.Background()), meta.ID, "hi", func(ev Event) {
		if ev.Type == EventError {
			errText = ev.Error
		}
	})
	if !strings.Contains(errText, "boom") {
		t.Errorf("error = %q，不接手时应报原始错误", errText)
	}
}
