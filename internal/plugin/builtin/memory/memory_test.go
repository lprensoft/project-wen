package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"wen/internal/plugin"
)

// newTestPlugin 返回一个已初始化到临时目录的插件。
func newTestPlugin(t *testing.T, cfg map[string]any) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: t.TempDir()}, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

// seed 写入 n 条记忆，标题按序号补零保证排序稳定。
func seed(t *testing.T, p *Plugin, n int) {
	t.Helper()
	s := p.snapshot()
	for i := range n {
		_, err := s.store.Save(Entry{
			Name:        fmt.Sprintf("条目%03d", i),
			Description: fmt.Sprintf("这是第 %d 条记忆的摘要说明文字", i),
			Type:        Types[i%len(Types)],
			Content:     "正文",
		}, false)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestInitRequiresStateDir(t *testing.T) {
	p := New()
	if err := p.Init(plugin.InitContext{Workdir: "/w"}, nil); err == nil {
		t.Fatal("没有持久化目录时应拒绝启用，而不是退化到进程当前目录")
	}
	if p.SystemPrompt() != "" {
		t.Error("未初始化时不应注入提示词")
	}
}

func TestSystemPromptEmptyWithoutMemories(t *testing.T) {
	p := newTestPlugin(t, nil)
	if got := p.SystemPrompt(); got != "" {
		t.Errorf("没有记忆时不应注入提示词，得到:\n%s", got)
	}
}

func TestSystemPromptListsAllEntriesWithinBudget(t *testing.T) {
	p := newTestPlugin(t, nil)
	seed(t, p, 100)

	got := p.SystemPrompt()
	if !strings.Contains(got, "[长期记忆]") || !strings.Contains(got, "save_memory") {
		t.Fatalf("提示词缺少索引头或保存判据:\n%s", got)
	}
	// 100 条是本方案的目标规模：必须全部带摘要出现，且不触发任何降级
	for _, i := range []int{0, 1, 42, 99} {
		name := fmt.Sprintf("条目%03d", i)
		if !strings.Contains(got, name) {
			t.Errorf("索引缺少 %s", name)
		}
		if !strings.Contains(got, fmt.Sprintf("这是第 %d 条记忆的摘要说明文字", i)) {
			t.Errorf("索引缺少 %s 的摘要", name)
		}
	}
	if strings.Contains(got, "未列出") {
		t.Error("100 条不应触发截断")
	}
	t.Logf("100 条索引共 %d 字节", len(got))
}

func TestIndexDegradesToTitlesOnly(t *testing.T) {
	p := newTestPlugin(t, nil)
	seed(t, p, 40)

	entries, _ := p.snapshot().store.List()
	full := renderIndex(entries, 200, 0) // 0 = 不限字节

	// 预算刚好装不下带摘要的版本：应先砍摘要，而不是砍条目
	idx := renderIndex(entries, 200, len(full)-1)
	if strings.Contains(idx, "摘要说明文字") {
		t.Error("超预算时应先省略摘要")
	}
	if strings.Contains(idx, "未列出") {
		t.Error("砍掉摘要后装得下，就不应再丢条目")
	}
	for i := range 40 {
		if !strings.Contains(idx, fmt.Sprintf("条目%03d", i)) {
			t.Fatalf("降级后仍应列出全部标题，缺少 条目%03d:\n%s", i, idx)
		}
	}
}

func TestIndexTruncatesAndReportsRemainder(t *testing.T) {
	p := newTestPlugin(t, nil)
	seed(t, p, 60)
	entries, _ := p.snapshot().store.List()

	// 条数上限：多出来的应被折成一行提示，而不是静默消失
	idx := renderIndex(entries, 10, 0)
	if !strings.Contains(idx, "另有 50 条未列出") {
		t.Errorf("应注明被略去的条数:\n%s", idx)
	}
	if n := strings.Count(idx, "\n- "); n != 9 {
		t.Errorf("应列出 10 条，实际 %d 条", n+1)
	}

	// 字节上限极小：仍要给出可继续查询的出口，不能返回空
	tiny := renderIndex(entries, 200, 120)
	if len(tiny) > 120 && !strings.Contains(tiny, "list_memories") {
		t.Errorf("预算极小时应退回一行说明:\n%s", tiny)
	}
	if tiny == "" {
		t.Error("不应返回空索引")
	}
}

func TestIndexOrderStableAcrossSaves(t *testing.T) {
	p := newTestPlugin(t, nil)
	seed(t, p, 20)
	before := p.SystemPrompt()

	// 更新一条已有记忆：展示顺序按（分类, 标题）固定，不应因时间变化而重排
	s := p.snapshot()
	if _, err := s.store.Save(Entry{
		Name: "条目005", Description: "这是第 5 条记忆的摘要说明文字", Type: Types[5%len(Types)], Content: "新正文",
	}, true); err != nil {
		t.Fatal(err)
	}
	if after := p.SystemPrompt(); after != before {
		t.Errorf("更新正文不应改变索引内容与顺序\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestListMemoriesFilters(t *testing.T) {
	p := newTestPlugin(t, nil)
	s := p.snapshot()
	s.store.Save(Entry{Name: "数据库连接串", Description: "PG 连接参数", Type: "事实", Content: "host=localhost"}, false)
	s.store.Save(Entry{Name: "提交信息用中文", Description: "说明做了什么", Type: "约定", Content: "正文"}, false)
	s.store.Save(Entry{Name: "回答要直接", Description: "不要寒暄", Type: "偏好", Content: "正文"}, false)

	tool := &listTool{p: p}
	run := func(args string) string {
		out, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	all := run(`{}`)
	if !strings.Contains(all, "共 3 条记忆") {
		t.Errorf("无过滤时应列出全部:\n%s", all)
	}

	// 关键词命中正文
	byContent := run(`{"keyword":"localhost"}`)
	if !strings.Contains(byContent, "数据库连接串") || strings.Contains(byContent, "回答要直接") {
		t.Errorf("关键词应匹配正文:\n%s", byContent)
	}
	// 关键词大小写不敏感
	if got := run(`{"keyword":"PG"}`); !strings.Contains(got, "数据库连接串") {
		t.Errorf("关键词应匹配摘要:\n%s", got)
	}
	if got := run(`{"keyword":"pg"}`); !strings.Contains(got, "数据库连接串") {
		t.Errorf("关键词匹配应不区分大小写:\n%s", got)
	}
	// 分类过滤
	byType := run(`{"type":"约定"}`)
	if !strings.Contains(byType, "提交信息用中文") || strings.Contains(byType, "数据库连接串") {
		t.Errorf("分类过滤失效:\n%s", byType)
	}
	// 无命中要明确说出来，而不是返回空串让模型以为没有记忆
	if got := run(`{"keyword":"根本不存在的词"}`); !strings.Contains(got, "没有符合条件") {
		t.Errorf("无命中应有明确说明:\n%s", got)
	}
}

func TestRecallMemory(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"max_entry_bytes": 1024})
	p.snapshot().store.Save(Entry{
		Name: "构建命令", Description: "怎么构建", Type: "事实", Content: "go build ./cmd/wen",
	}, false)

	tool := &recallTool{p: p}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"构建命令"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "go build ./cmd/wen") || !strings.Contains(out, "事实/构建命令") {
		t.Errorf("读取结果 = %q", out)
	}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"没有这条"}`)); err == nil {
		t.Error("读取不存在的记忆应报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"  "}`)); err == nil {
		t.Error("空标题应报错")
	}
}

func TestRecallTruncatesLongEntry(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"max_entry_bytes": 1024})
	p.snapshot().store.Save(Entry{
		Name: "长文", Type: "事实", Content: strings.Repeat("很长的正文内容。", 500),
	}, false)

	out, err := (&recallTool{p: p}).Execute(context.Background(), json.RawMessage(`{"name":"长文"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已截断") {
		t.Error("超长记忆应被截断并说明")
	}
	if len(out) > 1024+128 {
		t.Errorf("截断后仍有 %d 字节", len(out))
	}
}

func TestSetConfigTakesEffect(t *testing.T) {
	// 走 Manager 的完整链路：保存配置会重新 Init，不能丢掉记忆目录
	base := t.TempDir()
	m := plugin.NewManager(plugin.InitContext{}, filepath.Join(base, "plugins.state.json"))
	p := New()
	if err := m.Register(p, plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p.snapshot().store.Save(Entry{Name: "一条", Description: "钩子", Type: "事实", Content: "正文"}, false)
	if !strings.Contains(p.SystemPrompt(), "一条") {
		t.Fatal("初始状态应能注入索引")
	}

	if err := m.SetConfig("memory", map[string]any{"max_index_entries": 0}); err != nil {
		t.Fatal(err)
	}
	if s := p.snapshot(); s.maxIndexEntries != 0 {
		t.Errorf("配置未生效: %+v", s)
	}
	if s := p.snapshot(); s.store == nil {
		t.Fatal("重新 Init 后记忆目录不应丢失")
	}
	if !strings.Contains(p.SystemPrompt(), "一条") {
		t.Error("改配置后仍应能读到原有记忆")
	}
}

func TestToolsRegisterWithoutConflict(t *testing.T) {
	m := plugin.NewManager(plugin.InitContext{}, filepath.Join(t.TempDir(), "s.json"))
	if err := m.Register(New(), plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range m.EnabledTools() {
		names = append(names, tl.Name())
	}
	want := []string{"list_memories", "recall_memory"}
	if len(names) != len(want) {
		t.Fatalf("工具列表 = %v", names)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("工具 %d = %q, want %q", i, names[i], n)
		}
	}
}
