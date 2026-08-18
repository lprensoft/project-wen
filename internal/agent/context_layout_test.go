package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
	"wen/internal/session"
)

// volatilePlugin 每轮给出不同的片段，模拟心情、天气、记忆索引这类会变的注入。
type volatilePlugin struct{ n int }

func (p *volatilePlugin) Name() string                                  { return "volatile" }
func (p *volatilePlugin) Description() string                           { return "测试用：每轮片段都不同" }
func (p *volatilePlugin) Init(plugin.InitContext, map[string]any) error { return nil }
func (p *volatilePlugin) Tools() []plugin.Tool                          { return nil }
func (p *volatilePlugin) SystemPrompt() string                          { return "[角色设定]\n固定不变的设定" }

func (p *volatilePlugin) TurnPrompt(context.Context, plugin.TurnEvent) (string, error) {
	p.n++
	return "[当前心情]\n第 " + string(rune('0'+p.n)) + " 轮", nil
}

// 提示词缓存的前提是 system 逐轮字节一致。每轮都变的片段一旦混进去，
// 前缀立刻作废——这条约束只靠约定维持，所以要有测试盯着。
func TestSystemPrefixIsIdenticalAcrossTurns(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	vp := &volatilePlugin{}
	provider := &mockProvider{turns: []mockTurn{{content: "答一"}, {content: "答二"}}}
	ag := New(provider, newTestManager(t, vp), store, Options{Model: "test", SystemPrompt: "用户配置"})

	ag.Run(context.Background(), meta.ID, "问一", func(Event) {})
	ag.Run(context.Background(), meta.ID, "问二", func(Event) {})

	first, second := provider.reqs[0].Messages[0], provider.reqs[1].Messages[0]
	if first.Role != llm.RoleSystem || second.Role != llm.RoleSystem {
		t.Fatal("首条应当是 system")
	}
	if first.Content != second.Content {
		t.Errorf("system 两轮之间必须逐字节一致，否则缓存前缀每轮作废：\n第一轮:\n%s\n第二轮:\n%s",
			first.Content, second.Content)
	}
	for _, unwanted := range []string{"当前时间", "[当前心情]"} {
		if strings.Contains(first.Content, unwanted) {
			t.Errorf("易变内容不该在 system 里: %q", unwanted)
		}
	}
	// 固定的插件片段与用户配置提示词仍在 system 里
	for _, want := range []string{"[系统环境]", "[角色设定]", "用户配置"} {
		if !strings.Contains(first.Content, want) {
			t.Errorf("system 缺少 %q:\n%s", want, first.Content)
		}
	}
}

// 当前时间必须在上下文末尾：放在 system 开头时，模型会更采信历史里出现过的旧时刻。
func TestCurrentTimeRidesInLastMessage(t *testing.T) {
	store, _ := session.NewStore(t.TempDir())
	meta, _ := store.Create()
	provider := &mockProvider{turns: []mockTurn{{content: "答"}}}
	ag := New(provider, newTestManager(t, &volatilePlugin{}), store, Options{Model: "test"})

	ag.Run(context.Background(), meta.ID, "现在几点", func(Event) {})

	msgs := provider.reqs[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("最后一条应当是本轮用户消息，实际 %s", last.Role)
	}
	for _, want := range []string{"当前时间: ", "[当前心情]", "现在几点"} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("末条缺少 %q:\n%s", want, last.Content)
		}
	}
	// 落盘的那条保持原样：本轮状态是发出去的副本上的东西，不属于对话内容
	_, stored, _ := store.Get(meta.ID)
	if stored[0].Content != "现在几点" {
		t.Errorf("落盘的用户消息被污染了: %q", stored[0].Content)
	}
}

// 隔了很久再开口时，历史里要有这段时间流逝的痕迹——否则上下文里那十几个小时不存在。
func TestLongGapIsMarkedInHistory(t *testing.T) {
	dir := t.TempDir()
	store, _ := session.NewStore(dir)
	meta, _ := store.Create()
	old := time.Now().Add(-14 * time.Hour)
	store.Append(meta.ID, session.StoredMessage{
		Message: llm.Message{Role: llm.RoleUser, Content: "昨晚的问"}, TS: old})
	store.Append(meta.ID, session.StoredMessage{
		Message: llm.Message{Role: llm.RoleAssistant, Content: "昨晚的答"}, TS: old.Add(time.Minute)})
	store.Append(meta.ID, session.StoredMessage{
		Message: llm.Message{Role: llm.RoleUser, Content: "今天的问"}, TS: time.Now()})

	provider := &mockProvider{turns: []mockTurn{{content: "答"}}}
	ag := New(provider, newTestManager(t), store, Options{Model: "test"})
	ag.Run(context.Background(), meta.ID, "接着说", func(Event) {})

	var marked string
	for _, m := range provider.reqs[0].Messages {
		if strings.Contains(m.Content, "此处距上一条消息") {
			marked = m.Content
		}
	}
	if !strings.Contains(marked, "今天的问") {
		t.Errorf("间隔标记应当落在隔了很久之后的那条用户消息上，实际 %q", marked)
	}
	if !strings.Contains(marked, "约 13 小时") && !strings.Contains(marked, "约 14 小时") {
		t.Errorf("间隔时长不对: %q", marked)
	}
	// 本轮与上一条之间几乎没有间隔，末条不该出现间隔行
	msgs := provider.reqs[0].Messages
	if strings.Contains(msgs[len(msgs)-1].Content, "距上一条消息: ") {
		t.Errorf("本轮不该标间隔: %q", msgs[len(msgs)-1].Content)
	}
	// 原文不受影响：标记只加在发出去的副本上
	_, stored, _ := store.Get(meta.ID)
	for _, m := range stored {
		if strings.Contains(m.Content, "此处距上一条消息") {
			t.Errorf("间隔标记不该落盘: %q", m.Content)
		}
	}
}
