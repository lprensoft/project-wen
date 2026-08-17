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
	defaultAutoExtract     = true
	defaultMaxExtract      = 5
	// maxExtractBytes 限制送去提炼的历史长度，避免一次压缩带来过高的调用成本。
	maxExtractBytes = 24 * 1024
)

// 记忆库的归属范围。
const (
	scopeGlobal  = "global"  // 全局单库，所有工作目录共享
	scopeProject = "project" // 按工作目录分库
	defaultScope = scopeGlobal
)

// memoriesDir 按 scope 决定记忆目录。
func memoriesDir(ictx plugin.InitContext, scope string) (string, error) {
	if scope == scopeProject {
		if ictx.Workdir == "" {
			return "", fmt.Errorf("按项目分库需要工作目录，当前未设置（可改用全局记忆库）")
		}
		return filepath.Join(ictx.Workdir, ".wen", "memories"), nil
	}
	if ictx.StateDir == "" {
		return "", fmt.Errorf("没有可用的持久化目录，无法保存记忆")
	}
	return filepath.Join(ictx.StateDir, "memories"), nil
}

// Plugin 是 memory 系统插件：注入记忆索引，并提供读写记忆的工具。
type Plugin struct {
	mu sync.RWMutex

	store           *Store
	scope           string
	complete        plugin.CompleteFunc
	maxIndexEntries int
	maxIndexBytes   int
	maxEntryBytes   int
	autoExtract     bool
	maxExtract      int
}

func New() *Plugin {
	return &Plugin{
		scope:           defaultScope,
		maxIndexEntries: defaultMaxIndexEntries,
		maxIndexBytes:   defaultMaxIndexBytes,
		maxEntryBytes:   defaultMaxEntryBytes,
		autoExtract:     defaultAutoExtract,
		maxExtract:      defaultMaxExtract,
	}
}

func (p *Plugin) Name() string { return "memory" }
func (p *Plugin) Description() string {
	return "跨会话的长期记忆，索引随对话注入，可按需读取全文"
}

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:   "scope",
			Label: "记忆库范围",
			Type:  plugin.FieldSelect,
			Description: "全局：所有工作目录共享一份，存于配置目录下；" +
				"按项目：存于工作目录的 .wen/memories/，不同项目互不可见（建议加入该项目的 .gitignore）。" +
				"切换后原位置的记忆不会自动迁移。",
			Default: defaultScope,
			Options: []plugin.ConfigOption{
				{Value: scopeGlobal, Label: "全局"},
				{Value: scopeProject, Label: "按项目（工作目录）"},
			},
		},
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
			Key:   "auto_extract",
			Label: "压缩时自动提炼记忆",
			Type:  plugin.FieldBool,
			Description: "会话历史被压缩摘要替换之前，用一次独立的模型调用从其中提炼值得长期保留的结论。" +
				"关闭后只能靠模型在对话中主动调用 save_memory。每次压缩会因此多一次模型调用。",
			Default: defaultAutoExtract,
		},
		{
			Key:         "max_extract",
			Label:       "单次压缩最多提炼条数",
			Type:        plugin.FieldInt,
			Description: "超出的部分丢弃，避免一次压缩灌入过多低价值记忆。",
			Default:     defaultMaxExtract,
			Min:         plugin.IntPtr(1),
			Max:         plugin.IntPtr(50),
		},
	}
}

// Init 需要一个可写目录；没有则拒绝启用，避免把记忆散落到进程当前目录。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	scope := plugin.CfgString(cfg, "scope", defaultScope)
	dir, err := memoriesDir(ictx, scope)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.scope = scope
	p.store = NewStore(dir)
	p.complete = ictx.Complete
	p.maxIndexEntries = plugin.CfgInt(cfg, "max_index_entries", defaultMaxIndexEntries)
	p.maxIndexBytes = plugin.CfgInt(cfg, "max_index_bytes", defaultMaxIndexBytes)
	p.maxEntryBytes = plugin.CfgInt(cfg, "max_entry_bytes", defaultMaxEntryBytes)
	p.autoExtract = plugin.CfgBool(cfg, "auto_extract", defaultAutoExtract)
	p.maxExtract = plugin.CfgInt(cfg, "max_extract", defaultMaxExtract)
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
	maxIndexEntries int
	maxIndexBytes   int
	maxEntryBytes   int
	autoExtract     bool
	maxExtract      int
	maxExtractBytes int
}

// snapshot 取一份配置快照：SetConfig 会在运行时重新 Init，而工具可能正在执行。
func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{
		store:           p.store,
		maxIndexEntries: p.maxIndexEntries,
		maxIndexBytes:   p.maxIndexBytes,
		maxEntryBytes:   p.maxEntryBytes,
		autoExtract:     p.autoExtract,
		maxExtract:      p.maxExtract,
		maxExtractBytes: maxExtractBytes,
	}
}

// completeFunc 返回当前的辅助调用入口（可能为 nil）。
func (p *Plugin) completeFunc() plugin.CompleteFunc {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.complete
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
//
// 记忆库为空时只注入判据、不注入索引：判据正是引导保存第一条记忆的东西，
// 一条记忆都没有的时候最需要它。
func (p *Plugin) SystemPrompt() string {
	s := p.snapshot()
	if s.store == nil {
		return ""
	}
	entries, err := s.store.List()
	if err != nil || len(entries) == 0 {
		return promptGuide
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
