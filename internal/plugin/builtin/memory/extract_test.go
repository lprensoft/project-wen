package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/plugin"
)

// fakeComplete 记录收到的提示词并返回预设结果。
type fakeComplete struct {
	replies []string
	calls   int
	prompts []string
	err     error
}

func (f *fakeComplete) fn(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "[]", nil
	}
	r := f.replies[0]
	if len(f.replies) > 1 {
		f.replies = f.replies[1:]
	}
	return r, nil
}

func newPluginWithComplete(t *testing.T, c *fakeComplete, cfg map[string]any) *Plugin {
	t.Helper()
	p := New()
	ictx := plugin.InitContext{StateDir: t.TempDir()}
	if c != nil {
		ictx.Complete = c.fn
	}
	if err := p.Init(ictx, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

func sampleHistory() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: "以后回答别写开场白"},
		{Role: llm.RoleAssistant, Content: "好的"},
	}
}

func TestOnCompactExtractsAndSaves(t *testing.T) {
	c := &fakeComplete{replies: []string{`[
		{"name":"回答不要开场白","description":"直接给结论","type":"偏好","content":"回答时不写开场白。"},
		{"name":"构建命令","description":"go build ./cmd/wen","type":"事实","content":"构建：go build ./cmd/wen"}
	]`}}
	p := newPluginWithComplete(t, c, nil)

	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{
		SessionID: "s1", Summary: "摘要", History: sampleHistory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Errorf("应恰好发起一次提炼调用，实际 %d 次", c.calls)
	}

	// 提炼结果必须真的落盘，而不是只提示模型稍后自己保存
	entries, err := p.snapshot().store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("应保存 2 条记忆，实际 %d 条", len(entries))
	}
	got, err := p.snapshot().store.Get("构建命令")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "事实" || got.Content != "构建：go build ./cmd/wen" {
		t.Errorf("落盘内容不对: %+v", got)
	}

	// 注记落进该会话的摘要，让用户看得见发生了什么
	if !strings.Contains(note, "提炼并保存 2 条") || !strings.Contains(note, "偏好/回答不要开场白") {
		t.Errorf("注记 = %q", note)
	}
	// 历史内容与已有标题都要进提示词
	if !strings.Contains(c.prompts[0], "以后回答别写开场白") {
		t.Error("提示词应包含待提炼的历史")
	}
}

func TestOnCompactSkipsExistingTitles(t *testing.T) {
	c := &fakeComplete{replies: []string{
		`[{"name":"构建命令","description":"新的说法","type":"事实","content":"被改坏的版本"}]`,
	}}
	p := newPluginWithComplete(t, c, nil)
	p.snapshot().store.Save(Entry{Name: "构建命令", Description: "原说法", Type: "事实", Content: "原文"}, false)

	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()})
	if err != nil {
		t.Fatal(err)
	}
	// 压缩摘要里的内容可能已经是残缺副本，覆盖会让原文退化
	got, _ := p.snapshot().store.Get("构建命令")
	if got.Content != "原文" {
		t.Errorf("已有同名记忆不应被覆盖，当前内容 = %q", got.Content)
	}
	if note != "" {
		t.Errorf("没有新增时不应返回注记: %q", note)
	}
	// 已有标题要写进提示词，让模型自己先避开重复
	if !strings.Contains(c.prompts[0], "事实/构建命令") {
		t.Errorf("提示词应列出已有记忆标题:\n%s", c.prompts[0])
	}
}

func TestOnCompactNoCandidates(t *testing.T) {
	c := &fakeComplete{replies: []string{"[]"}}
	p := newPluginWithComplete(t, c, nil)

	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()})
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Errorf("空结果不应返回注记: %q", note)
	}
	if all, _ := p.snapshot().store.List(); len(all) != 0 {
		t.Errorf("不应凑数保存: %d 条", len(all))
	}
}

func TestOnCompactRespectsMaxExtract(t *testing.T) {
	var items []string
	for i := range 10 {
		items = append(items, fmt.Sprintf(
			`{"name":"条目%d","description":"d","type":"事实","content":"c"}`, i))
	}
	c := &fakeComplete{replies: []string{"[" + strings.Join(items, ",") + "]"}}
	p := newPluginWithComplete(t, c, map[string]any{"max_extract": 3})

	if _, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()}); err != nil {
		t.Fatal(err)
	}
	if all, _ := p.snapshot().store.List(); len(all) != 3 {
		t.Errorf("应只保存 3 条，实际 %d 条", len(all))
	}
}

func TestOnCompactDisabled(t *testing.T) {
	c := &fakeComplete{replies: []string{`[{"name":"x","description":"d","type":"事实","content":"c"}]`}}
	p := newPluginWithComplete(t, c, map[string]any{"auto_extract": false})

	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()})
	if err != nil || note != "" {
		t.Errorf("关闭自动提炼后应直接跳过: note=%q err=%v", note, err)
	}
	if c.calls != 0 {
		t.Error("关闭后不应产生模型调用")
	}
}

func TestOnCompactWithoutModel(t *testing.T) {
	p := newPluginWithComplete(t, nil, nil) // 没有 Complete
	_, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()})
	if err == nil {
		t.Fatal("没有可用模型时应报错，以便记录日志")
	}
	if !strings.Contains(err.Error(), "模型") {
		t.Errorf("错误信息应说明原因: %v", err)
	}
}

func TestOnCompactModelErrorDoesNotPanic(t *testing.T) {
	c := &fakeComplete{err: fmt.Errorf("上游 429")}
	p := newPluginWithComplete(t, c, nil)
	if _, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()}); err == nil {
		t.Error("模型调用失败应回报错误（由 Manager 记录日志，不阻断压缩）")
	}
}

func TestOnCompactEmptyHistory(t *testing.T) {
	c := &fakeComplete{}
	p := newPluginWithComplete(t, c, nil)
	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", Summary: "空"})
	if err != nil || note != "" {
		t.Errorf("空历史应跳过: note=%q err=%v", note, err)
	}
	if c.calls != 0 {
		t.Error("空历史不应产生模型调用")
	}
}

func TestParseExtracted(t *testing.T) {
	cases := []struct {
		name, in string
		want     int
	}{
		{"裸数组", `[{"name":"a","description":"d","type":"事实","content":"c"}]`, 1},
		{"围栏包裹", "```json\n[{\"name\":\"a\",\"description\":\"d\",\"type\":\"事实\",\"content\":\"c\"}]\n```", 1},
		{"无语言标记的围栏", "```\n[]\n```", 0},
		{"前后带说明文字", "分析如下：\n[{\"name\":\"a\",\"description\":\"d\",\"type\":\"事实\",\"content\":\"c\"}]\n以上。", 1},
		{"空数组", `[]`, 0},
		{"空串", ``, 0},
		{"仅空白", "  \n ", 0},
	}
	for _, c := range cases {
		got, err := parseExtracted(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("%s: 解析出 %d 条，want %d", c.name, len(got), c.want)
		}
	}

	if _, err := parseExtracted("这不是 JSON"); err == nil {
		t.Error("无法解析时应报错")
	}
}

func TestExtractSkipsInvalidItems(t *testing.T) {
	c := &fakeComplete{replies: []string{`[
		{"name":"","description":"d","type":"事实","content":"c"},
		{"name":"没内容","description":"d","type":"事实","content":"   "},
		{"name":"分类不对","description":"d","type":"胡编","content":"c"},
		{"name":"正常条目","description":"d","type":"约定","content":"c"}
	]`}}
	p := newPluginWithComplete(t, c, nil)

	if _, err := p.OnCompact(context.Background(), plugin.CompactEvent{SessionID: "s1", History: sampleHistory()}); err != nil {
		t.Fatal(err)
	}
	all, _ := p.snapshot().store.List()
	if len(all) != 1 || all[0].Name != "正常条目" {
		var names []string
		for _, e := range all {
			names = append(names, e.Name)
		}
		t.Errorf("非法条目应被跳过而不影响其余，实际保存 %v", names)
	}
}

func TestSerializeHistoryTruncates(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("很长的提问内容。", 3000)},
	}
	got := serializeHistory(msgs, 4096)
	if len(got) > 4096+64 {
		t.Errorf("应截断到上限附近，实际 %d 字节", len(got))
	}
	if !strings.Contains(got, "已截断") {
		t.Error("截断后应说明")
	}
}

func TestPluginImplementsLifecycle(t *testing.T) {
	var _ plugin.Lifecycle = New()

	// 走 Manager 的广播路径（这是核心真正调用的入口）
	c := &fakeComplete{replies: []string{`[{"name":"经由广播","description":"d","type":"事实","content":"c"}]`}}
	base := t.TempDir()
	m := plugin.NewManager(
		plugin.InitContext{Complete: c.fn},
		filepath.Join(base, "plugins.state.json"),
	)
	p := New()
	if err := m.Register(p, plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	notes := m.NotifyCompact(context.Background(), plugin.CompactEvent{
		SessionID: "s1", History: sampleHistory(),
	})
	if len(notes) != 1 || !strings.Contains(notes[0], "经由广播") {
		t.Errorf("Manager 广播未拿到提炼注记: %v", notes)
	}
}
