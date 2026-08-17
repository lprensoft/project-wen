package memory

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"wen/internal/plugin"
)

const (
	defaultMaxIndexEntries = 200
	defaultMaxIndexBytes   = 16 * 1024
	defaultMaxEntryBytes   = 8 * 1024
	defaultMaxArchives     = 20
)

// Plugin 是 memory 系统插件：注入记忆索引，并提供读写记忆的工具。
type Plugin struct {
	mu sync.RWMutex

	store           *Store
	archiveDir      string
	maxIndexEntries int
	maxIndexBytes   int
	maxEntryBytes   int
	maxArchives     int
}

func New() *Plugin {
	return &Plugin{
		maxIndexEntries: defaultMaxIndexEntries,
		maxIndexBytes:   defaultMaxIndexBytes,
		maxEntryBytes:   defaultMaxEntryBytes,
		maxArchives:     defaultMaxArchives,
	}
}

func (p *Plugin) Name() string { return "memory" }
func (p *Plugin) Description() string {
	return "跨会话的长期记忆，索引随对话注入，可按需读取全文"
}

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "max_index_entries",
			Label:       "索引最多列出条数",
			Type:        plugin.FieldInt,
			Description: "超出后按最近更新保留，其余可用 list_memories 查看。",
			Default:     defaultMaxIndexEntries,
			Min:         plugin.IntPtr(0),
			Max:         plugin.IntPtr(2000),
		},
		{
			Key:   "max_index_bytes",
			Label: "索引最大字节数",
			Type:  plugin.FieldInt,
			Description: "索引会随每一轮对话重复发送，因此需要上界。" +
				"超出时先省略各条的摘要只保留标题，仍超出才截断。",
			Default: defaultMaxIndexBytes,
			Min:     plugin.IntPtr(0),
			Max:     plugin.IntPtr(256 * 1024),
		},
		{
			Key:         "max_entry_bytes",
			Label:       "单条记忆最大返回字节数",
			Type:        plugin.FieldInt,
			Description: "读取记忆全文时超出部分截断。",
			Default:     defaultMaxEntryBytes,
			Min:         plugin.IntPtr(1024),
			Max:         plugin.IntPtr(1024 * 1024),
		},
		{
			Key:         "max_archives",
			Label:       "保留的压缩归档数",
			Type:        plugin.FieldInt,
			Description: "会话历史被压缩前会先原样归档，超出数量时删除最旧的；填 0 表示不归档。",
			Default:     defaultMaxArchives,
			Min:         plugin.IntPtr(0),
			Max:         plugin.IntPtr(500),
		},
	}
}

// Init 需要一个持久化目录；没有则拒绝启用，避免把记忆散落到进程当前目录。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	if ictx.StateDir == "" {
		return fmt.Errorf("没有可用的持久化目录，无法保存记忆")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.store = NewStore(filepath.Join(ictx.StateDir, "memories"))
	p.archiveDir = filepath.Join(ictx.StateDir, "archives")
	p.maxIndexEntries = plugin.CfgInt(cfg, "max_index_entries", defaultMaxIndexEntries)
	p.maxIndexBytes = plugin.CfgInt(cfg, "max_index_bytes", defaultMaxIndexBytes)
	p.maxEntryBytes = plugin.CfgInt(cfg, "max_entry_bytes", defaultMaxEntryBytes)
	p.maxArchives = plugin.CfgInt(cfg, "max_archives", defaultMaxArchives)
	return nil
}

func (p *Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		&saveTool{p: p},
		&recallTool{p: p},
		&listTool{p: p},
		&deleteTool{p: p},
	}
}

// settings 是一次调用期间使用的配置快照。
type settings struct {
	store           *Store
	archiveDir      string
	maxIndexEntries int
	maxIndexBytes   int
	maxEntryBytes   int
	maxArchives     int
}

// snapshot 取一份配置快照：SetConfig 会在运行时重新 Init，而工具可能正在执行。
func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{
		store:           p.store,
		archiveDir:      p.archiveDir,
		maxIndexEntries: p.maxIndexEntries,
		maxIndexBytes:   p.maxIndexBytes,
		maxEntryBytes:   p.maxEntryBytes,
		maxArchives:     p.maxArchives,
	}
}

// sortByUpdatedDesc 按更新时间倒序，仅用于超预算时挑选保留哪些条目。
func sortByUpdatedDesc(entries []Entry) {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		return b.Updated.Compare(a.Updated)
	})
}

const promptHeader = `[长期记忆]
以下是已保存的记忆条目，跨会话长期有效。需要某条的细节时用 recall_memory 按标题读取全文。`

const promptGuide = `对话中出现下列内容时，用 save_memory 保存，使其在之后的会话中依然可用：
- 偏好：对方明确表达的、长期有效的工作方式或表达方式要求
- 约定：项目或协作上确定下来的规则
- 事实：环境、配置、结构等不易从当前上下文重新得知的信息
- 踩坑：已经验证过的失败原因与正确做法
只保存长期有效的结论，不保存一次性的任务细节、临时中间结果与可以随时重新读取的内容。
对话历史被压缩后，摘要中若含上述内容，需要检查是否已经保存。`

// SystemPrompt 返回记忆索引与保存判据。未初始化时不注入（列表接口会对
// 禁用的插件也调用本方法，此时不应产生任何磁盘访问）。
func (p *Plugin) SystemPrompt() string {
	s := p.snapshot()
	if s.store == nil {
		return ""
	}
	entries, err := s.store.List()
	if err != nil || len(entries) == 0 {
		return ""
	}
	return promptHeader + "\n" +
		renderIndex(entries, s.maxIndexEntries, s.maxIndexBytes) + "\n\n" + promptGuide
}

// renderIndex 按预算渲染索引，分三级降级：
// 全列（日期 + 分类/标题 + 摘要）→ 省略摘要只留标题 → 按最近更新截断并注明剩余条数。
// 降级优先砍摘要而不是砍条目：标题在，模型才知道这条记忆存在，还能按名读取。
func renderIndex(entries []Entry, maxEntries, maxBytes int) string {
	kept, dropped := limitEntries(entries, maxEntries)

	if out, ok := renderLines(kept, dropped, maxBytes, true); ok {
		return out
	}
	if out, ok := renderLines(kept, dropped, maxBytes, false); ok {
		return out
	}
	// 仍然超预算：按最近更新丢弃更多条目，直到装得下
	for n := len(kept) - 1; n > 0; n-- {
		trimmed, more := limitEntries(entries, n)
		if out, ok := renderLines(trimmed, more, maxBytes, false); ok {
			return out
		}
	}
	return fmt.Sprintf("（共 %d 条记忆，索引预算不足以列出，可用 list_memories 查看）", len(entries))
}

// limitEntries 在超出条数上限时按最近更新保留，返回保留的条目（仍是原顺序）与被略去的条数。
func limitEntries(entries []Entry, max int) ([]Entry, int) {
	if max <= 0 || len(entries) <= max {
		return entries, 0
	}
	recent := make([]Entry, len(entries))
	copy(recent, entries)
	// 只用更新时间挑选保留哪些，不用它决定展示顺序：
	// 按时间排序会让每次保存都重排整个提示词前缀。
	sortByUpdatedDesc(recent)
	keep := map[string]bool{}
	for _, e := range recent[:max] {
		keep[e.Slug] = true
	}
	out := make([]Entry, 0, max)
	for _, e := range entries {
		if keep[e.Slug] {
			out = append(out, e)
		}
	}
	return out, len(entries) - len(out)
}

func renderLines(entries []Entry, dropped, maxBytes int, withDesc bool) (string, bool) {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e.Created.Format("2006-01-02"))
		b.WriteString(" ")
		b.WriteString(e.Type)
		b.WriteString("/")
		b.WriteString(e.Name)
		if withDesc && e.Description != "" {
			b.WriteString(" — ")
			b.WriteString(e.Description)
		}
		b.WriteString("\n")
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "（另有 %d 条未列出，可用 list_memories 查看）\n", dropped)
	}
	out := strings.TrimRight(b.String(), "\n")
	if maxBytes > 0 && len(out) > maxBytes {
		return "", false
	}
	return out, true
}
