package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
	"wen/internal/session"
)

// writeSession 直接造一个会话文件：首行 meta，其后每行一条消息。
// 会话 id 的时间戳前缀与文件修改时间都可控，用来验证日期剪枝。
func writeSession(t *testing.T, dir, id, title string, msgs []session.StoredMessage, touched time.Time) {
	t.Helper()
	created, err := time.ParseInLocation(idTimeLayout, id[:len(idTimeLayout)], time.Local)
	if err != nil {
		t.Fatalf("会话 id %q 前缀不是时间戳: %v", id, err)
	}
	var b strings.Builder
	meta, _ := json.Marshal(session.Meta{Type: "meta", ID: id, Title: title, CreatedAt: created})
	b.Write(meta)
	b.WriteString("\n")
	for _, m := range msgs {
		line, _ := json.Marshal(m)
		b.Write(line)
		b.WriteString("\n")
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, touched, touched); err != nil {
		t.Fatal(err)
	}
}

func msg(role, content string, ts time.Time) session.StoredMessage {
	return session.StoredMessage{Message: llm.Message{Role: role, Content: content}, TS: ts}
}

func day(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func newTestPlugin(t *testing.T, cfg map[string]any) (*Plugin, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.Init(plugin.InitContext{SessionDir: dir}, cfg); err != nil {
		t.Fatal(err)
	}
	return p, dir
}

func run(t *testing.T, tool plugin.Tool, args string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", tool.Name(), args, err)
	}
	return out
}

func TestInitRequiresSessionDir(t *testing.T) {
	if err := New().Init(plugin.InitContext{Workdir: "/w"}, nil); err == nil {
		t.Fatal("没有会话目录时应拒绝启用")
	}
}

func TestSearchByKeyword(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	writeSession(t, dir, "20260501-090000-aaaaaa", "聊命名规范", []session.StoredMessage{
		msg("user", "接口路径用小写连字符还是驼峰？", day("2026-05-01 09:00")),
		msg("assistant", "统一用小写连字符。", day("2026-05-01 09:01")),
	}, day("2026-05-01 09:01"))
	writeSession(t, dir, "20260601-090000-bbbbbb", "聊部署", []session.StoredMessage{
		msg("user", "怎么部署到服务器？", day("2026-06-01 09:00")),
	}, day("2026-06-01 09:00"))

	tool := &searchTool{p: p}
	got := run(t, tool, `{"keyword":"连字符"}`)
	if !strings.Contains(got, "聊命名规范") || strings.Contains(got, "聊部署") {
		t.Errorf("关键词过滤失效:\n%s", got)
	}
	if !strings.Contains(got, "命中 2 条") {
		t.Errorf("应统计命中条数:\n%s", got)
	}
	// 摘录以命中位置为中心，且包含关键词本身
	if !strings.Contains(got, "连字符") {
		t.Errorf("摘录应包含关键词:\n%s", got)
	}
	// 大小写不敏感
	if out := run(t, tool, `{"keyword":"驼峰"}`); !strings.Contains(out, "聊命名规范") {
		t.Errorf("应命中:\n%s", out)
	}
	if out := run(t, tool, `{"keyword":"根本没有的词"}`); !strings.Contains(out, "没有符合条件") {
		t.Errorf("无命中应有明确说明:\n%s", out)
	}
}

// TestSearchByDateSpansSessionCreation 是本插件最容易写错的一处：
// 会话可能创建于很久以前却一直在用，按文件名/创建时间判断日期会漏掉后续的消息。
func TestSearchByDateSpansSessionCreation(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	// 会话创建于 5 月 1 日，但 6 月 20 日还在继续用
	writeSession(t, dir, "20260501-090000-aaaaaa", "长期会话", []session.StoredMessage{
		msg("user", "五月说的话", day("2026-05-01 09:00")),
		msg("user", "六月说的话", day("2026-06-20 15:00")),
	}, day("2026-06-20 15:00"))

	tool := &searchTool{p: p}
	got := run(t, tool, `{"from":"2026-06-01","to":"2026-06-30"}`)
	if !strings.Contains(got, "六月说的话") {
		t.Errorf("应命中创建于区间之前、但消息落在区间内的会话:\n%s", got)
	}
	if strings.Contains(got, "五月说的话") {
		t.Errorf("区间外的消息不应返回:\n%s", got)
	}
	if !strings.Contains(got, "命中 1 条") {
		t.Errorf("只应命中 1 条:\n%s", got)
	}

	// 只查五月，应只剩五月那条
	got = run(t, tool, `{"from":"2026-05-01","to":"2026-05-31"}`)
	if !strings.Contains(got, "五月说的话") || strings.Contains(got, "六月说的话") {
		t.Errorf("日期区间筛选错误:\n%s", got)
	}

	// 边界：起止同为消息当天应命中（to 取当天最后一刻）
	if out := run(t, tool, `{"from":"2026-06-20","to":"2026-06-20"}`); !strings.Contains(out, "六月说的话") {
		t.Errorf("单日查询应命中当天消息:\n%s", out)
	}
}

func TestCandidatesPruning(t *testing.T) {
	dir := t.TempDir()
	// 文件修改时间早于 from → 其中不可能有 from 之后的消息，安全剪掉
	writeSession(t, dir, "20260101-090000-old000", "很旧", nil, day("2026-01-02 10:00"))
	// 创建时间晚于 to → 消息不会早于创建时间，安全剪掉
	writeSession(t, dir, "20261201-090000-new000", "很新", nil, day("2026-12-01 09:00"))
	// 跨区间：创建早、最近还在用，不能被剪
	writeSession(t, dir, "20260101-100000-span00", "跨区间", nil, day("2026-06-20 10:00"))
	// 非会话文件不应进入候选
	os.WriteFile(filepath.Join(dir, "笔记.txt"), []byte("x"), 0o644)

	got, err := candidates(dir, day("2026-06-01 00:00"), day("2026-06-30 23:59"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].id != "20260101-100000-span00" {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.id)
		}
		t.Fatalf("剪枝结果 = %v，应只剩跨区间的那个", ids)
	}

	// 不限日期时全部保留，且按时间从新到旧
	all, err := candidates(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("不限日期应保留 3 个，实际 %d", len(all))
	}
	if all[0].id != "20261201-090000-new000" || all[2].id != "20260101-090000-old000" {
		t.Errorf("应按时间从新到旧排序: %s ... %s", all[0].id, all[2].id)
	}
}

func TestCandidatesMissingDirIsEmpty(t *testing.T) {
	got, err := candidates(filepath.Join(t.TempDir(), "不存在"), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("目录不存在不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d", len(got))
	}
}

func TestScanCapReportsTruncation(t *testing.T) {
	p, dir := newTestPlugin(t, map[string]any{"max_scan_sessions": 2})
	for i := range 5 {
		id := fmt.Sprintf("202606%02d-090000-%06d", i+1, i)
		writeSession(t, dir, id, fmt.Sprintf("会话%d", i), []session.StoredMessage{
			msg("user", "共同关键词 apple", day(fmt.Sprintf("2026-06-%02d 09:00", i+1))),
		}, day(fmt.Sprintf("2026-06-%02d 09:00", i+1)))
	}
	got := run(t, &searchTool{p: p}, `{"keyword":"apple"}`)
	if !strings.Contains(got, "仅扫描了最近 2 个会话") {
		t.Errorf("超出扫描上限时必须说明，不能静默漏结果:\n%s", got)
	}
	// 从新到旧扫描，命中的应是最近的两个
	if !strings.Contains(got, "会话4") || !strings.Contains(got, "会话3") || strings.Contains(got, "会话0") {
		t.Errorf("应扫描最近的会话:\n%s", got)
	}
}

func TestSnippetCapReportsRemainder(t *testing.T) {
	p, dir := newTestPlugin(t, map[string]any{"max_snippets": 2})
	var msgs []session.StoredMessage
	for i := range 5 {
		msgs = append(msgs, msg("user", fmt.Sprintf("第 %d 次提到 banana", i), day("2026-06-01 09:00")))
	}
	writeSession(t, dir, "20260601-090000-aaaaaa", "多次命中", msgs, day("2026-06-01 09:00"))

	got := run(t, &searchTool{p: p}, `{"keyword":"banana"}`)
	if !strings.Contains(got, "命中 5 条") {
		t.Errorf("应统计全部命中数:\n%s", got)
	}
	if !strings.Contains(got, "另有 3 条命中未列出") {
		t.Errorf("片段被截断时应说明剩余:\n%s", got)
	}
}

func TestReadSession(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	msgs := []session.StoredMessage{
		msg("user", "第一个问题", day("2026-06-01 09:00")),
		msg("assistant", "第一个回答", day("2026-06-01 09:01")),
		{Message: llm.Message{Role: "user", Content: "以下是之前对话内容的压缩摘要：要点若干"},
			Kind: "summary", TS: day("2026-06-02 10:00")},
	}
	writeSession(t, dir, "20260601-090000-aaaaaa", "被读的会话", msgs, day("2026-06-02 10:00"))

	tool := &readTool{p: p}
	got := run(t, tool, `{"id":"20260601-090000-aaaaaa"}`)
	for _, want := range []string{"被读的会话", "第一个问题", "第一个回答", "压缩摘要"} {
		if !strings.Contains(got, want) {
			t.Errorf("读取结果缺少 %q:\n%s", want, got)
		}
	}

	// 关键词与日期收窄
	if out := run(t, tool, `{"id":"20260601-090000-aaaaaa","keyword":"回答"}`); strings.Contains(out, "第一个问题") {
		t.Errorf("关键词收窄失效:\n%s", out)
	}
	if out := run(t, tool, `{"id":"20260601-090000-aaaaaa","from":"2026-06-02"}`); strings.Contains(out, "第一个问题") {
		t.Errorf("日期收窄失效:\n%s", out)
	}
	if out := run(t, tool, `{"id":"20260601-090000-aaaaaa","keyword":"绝不存在"}`); !strings.Contains(out, "没有符合条件") {
		t.Errorf("无命中应有说明:\n%s", out)
	}

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"不存在的会话"}`)); err == nil {
		t.Error("读取不存在的会话应报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"  "}`)); err == nil {
		t.Error("空 id 应报错")
	}
}

func TestBadDateAndRange(t *testing.T) {
	p, _ := newTestPlugin(t, nil)
	tool := &searchTool{p: p}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"from":"2026/06/01"}`)); err == nil {
		t.Error("非法日期格式应报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"from":"2026-06-30","to":"2026-06-01"}`)); err == nil {
		t.Error("结束日期早于起始日期应报错")
	}
}

func TestOutputClipped(t *testing.T) {
	p, dir := newTestPlugin(t, map[string]any{"max_bytes": 1024, "max_snippets": 20})
	var msgs []session.StoredMessage
	for i := range 30 {
		msgs = append(msgs, msg("user", strings.Repeat("很长的内容 cherry ", 20)+fmt.Sprint(i), day("2026-06-01 09:00")))
	}
	writeSession(t, dir, "20260601-090000-aaaaaa", "超长会话", msgs, day("2026-06-01 09:00"))

	got := run(t, &searchTool{p: p}, `{"keyword":"cherry"}`)
	if !strings.Contains(got, "已截断") {
		t.Errorf("超出字节上限应截断并说明:\n%s", got)
	}
	if len(got) > 1024+160 {
		t.Errorf("截断后仍有 %d 字节", len(got))
	}
}

func TestToolsRegisterWithoutConflict(t *testing.T) {
	m := plugin.NewManager(
		plugin.InitContext{SessionDir: t.TempDir()},
		filepath.Join(t.TempDir(), "s.json"),
	)
	if err := m.Register(New(), plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range m.EnabledTools() {
		names = append(names, tl.Name())
	}
	want := []string{"search_sessions", "read_session", "read_archive"}
	if len(names) != len(want) {
		t.Fatalf("工具列表 = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("工具 %d = %q, want %q", i, names[i], n)
		}
	}
}
