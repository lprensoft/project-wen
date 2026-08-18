package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"wen/internal/plugin"
)

const (
	defaultMaxIndexEntries = 200
	defaultMaxIndexBytes   = 16 * 1024
	defaultMaxEntryBytes   = 8 * 1024
	defaultAutoExtract     = true
	defaultMaxExtract      = 5
	defaultTurnExtract     = true
	defaultTurnEvery       = 10
	defaultDecay           = false
	defaultBlurDays        = 30
	defaultForgetDays      = 90
	// maxExtractBytes 限制送去提炼的历史长度，避免一次压缩带来过高的调用成本。
	maxExtractBytes = 24 * 1024
	// turnExtractBytes 是定期提炼的历史上限。比压缩那次小一档：一个窗口本来
	// 就只有十来轮对话，装不满也没必要为此付更多 token。
	turnExtractBytes = 12 * 1024
	// minWindowBytes 以下的窗口不值得为它调一次模型（十轮「嗯」「好的」也会到点）。
	// 不清空缓冲，并进下一个窗口继续攒。
	minWindowBytes = 240
	// idleFlushGap：与上一轮相隔这么久再开口，视作上一段话题已经结束，
	// 先把攒着的窗口提炼掉再开新窗口。比「轮数够了」更贴近记忆该落盘的时机，
	// 而且不需要任何定时器——间隔从两轮的时间戳就能算出来。
	idleFlushGap = 30 * time.Minute
	// stopWait 是 Stop 等待在途提炼收尾的上限，超时就不再等（ctx 已经取消）。
	stopWait = 5 * time.Second
)

// 记忆库的存放范围。配置键沿用 scope，但代码里叫 library——「可见域」这个核心概念
// 也叫 scope，同名会让这段逻辑难以阅读。
const (
	libraryGlobal  = "global"  // 全局单库，所有工作目录共享
	libraryProject = "project" // 按工作目录分库
	defaultLibrary = libraryGlobal
)

// memoriesDir 按存放范围决定基准记忆目录。按可见域分出的库是它的同级目录，
// 见 plugin.DomainDir。
func memoriesDir(ictx plugin.InitContext, library string) (string, error) {
	if library == libraryProject {
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

	libBase         string // 基准记忆库目录（= 共享域）
	store           *Store // 基准库，等价于 storeFor("")
	library         string
	complete        plugin.CompleteFunc
	notice          plugin.NoticeFunc
	maxIndexEntries int
	maxIndexBytes   int
	maxEntryBytes   int
	autoExtract     bool
	maxExtract      int
	turnExtract     bool
	turnEvery       int
	decay           bool
	blurDays        int
	forgetDays      int

	// 后台提炼与清扫的运行环境。ctx 由 Init 建、Stop 取消——广播进来的 ctx
	// 在轮次结束时就被取消了，拿它去跑后台调用必然半路夭折。
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 按可见域分出的库，惰性创建。单独一把锁：Store 自带目录指纹缓存，
	// 每次重建会白丢缓存，而这张表的生命周期与配置无关。
	storesMu sync.Mutex
	stores   map[string]*Store

	// 定期提炼的状态。单独一把锁，且不随 Init 重置：攒着的是对话内容，
	// 不是配置，改一次配置就把它清空等于白丢一个窗口。
	turnMu     sync.Mutex
	windows    map[windowKey]*window
	extracting map[windowKey]bool
	lastSweep  time.Time
	sweeping   bool
	// stopped 让 wg.Add 与 wg.Wait 有明确的先后：两者都在 turnMu 内决定，
	// 停止之后不再有新的 goroutine 被登记，Wait 也就不会漏等一个刚起来的。
	stopped bool
}

func New() *Plugin {
	return &Plugin{
		library:         defaultLibrary,
		maxIndexEntries: defaultMaxIndexEntries,
		maxIndexBytes:   defaultMaxIndexBytes,
		maxEntryBytes:   defaultMaxEntryBytes,
		autoExtract:     defaultAutoExtract,
		maxExtract:      defaultMaxExtract,
		turnExtract:     defaultTurnExtract,
		turnEvery:       defaultTurnEvery,
		decay:           defaultDecay,
		blurDays:        defaultBlurDays,
		forgetDays:      defaultForgetDays,
		windows:         map[windowKey]*window{},
		extracting:      map[windowKey]bool{},
	}
}

func (p *Plugin) Name() string { return "memory" }

func (p *Plugin) Category() string { return plugin.CategoryMemory }
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
			Default: defaultLibrary,
			Options: []plugin.ConfigOption{
				{Value: libraryGlobal, Label: "全局"},
				{Value: libraryProject, Label: "按项目（工作目录）"},
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
			Label:       "单次最多提炼条数",
			Type:        plugin.FieldInt,
			Description: "超出的部分丢弃，避免一次提炼灌入过多低价值记忆。",
			Default:     defaultMaxExtract,
			Min:         plugin.IntPtr(1),
			Max:         plugin.IntPtr(50),
		},
		{
			Key:   "turn_extract",
			Label: "对话中定期提炼记忆",
			Type:  plugin.FieldBool,
			Description: "每隔若干轮对话，用一次独立的模型调用从这段对话中提炼值得长期保留的结论，" +
				"顺带修订与之矛盾的旧记忆。不必等到压缩，大部分对话本来也走不到那一步。" +
				"每个提炼窗口会因此多一次模型调用。",
			Default: defaultTurnExtract,
		},
		{
			Key:   "turn_extract_every",
			Label: "提炼间隔（轮）",
			Type:  plugin.FieldInt,
			Description: "每多少轮真人对话提炼一次。机器自发的轮次（心跳、定时任务）不计入。" +
				"距上次对话超过半小时再开口时，会先把攒着的部分提炼掉。",
			Default: defaultTurnEvery,
			Min:     plugin.IntPtr(5),
			Max:     plugin.IntPtr(100),
		},
		{
			Key:   "decay",
			Label: "记忆逐步淡忘",
			Type:  plugin.FieldBool,
			Description: "让标记为会淡忘的记忆随时间失效：久未提及先塌缩成一句要点，" +
				"更久之后移出记忆库（移入库内的 forgotten 目录，不会真正删除）。" +
				"是否会淡忘由保存时逐条决定，未标记的记忆永久保留，不受此开关影响。",
			Default: defaultDecay,
		},
		{
			Key:         "decay_blur_days",
			Label:       "细节淡忘天数",
			Type:        plugin.FieldInt,
			Description: "一条会淡忘的记忆多少天没有被提及后，正文塌缩成摘要只留要点。",
			Default:     defaultBlurDays,
			Min:         plugin.IntPtr(1),
			Max:         plugin.IntPtr(3650),
		},
		{
			Key:         "decay_forget_days",
			Label:       "移出记忆库天数",
			Type:        plugin.FieldInt,
			Description: "一条会淡忘的记忆多少天没有被提及后移出记忆库。需大于细节淡忘天数。",
			Default:     defaultForgetDays,
			Min:         plugin.IntPtr(1),
			Max:         plugin.IntPtr(3650),
		},
	}
}

// Init 需要一个可写目录；没有则拒绝启用，避免把记忆散落到进程当前目录。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	library := plugin.CfgString(cfg, "scope", defaultLibrary)
	dir, err := memoriesDir(ictx, library)
	if err != nil {
		return err
	}
	blurDays := plugin.CfgInt(cfg, "decay_blur_days", defaultBlurDays)
	forgetDays := plugin.CfgInt(cfg, "decay_forget_days", defaultForgetDays)
	// 跨字段的约束 NormalizeConfig 表达不了（它只认单字段的 Min/Max），在这里挡一次。
	// 先于任何副作用检查：Init 失败等于拒绝这次配置。
	if forgetDays <= blurDays {
		return fmt.Errorf("「移出记忆库天数」（%d）需大于「细节淡忘天数」（%d）", forgetDays, blurDays)
	}

	// 先停掉上一份配置起的后台活动：Init 会被 SetConfig 反复调用
	p.stopWorkers()

	p.storesMu.Lock()
	p.stores = map[string]*Store{}
	p.storesMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	p.library = library
	p.libBase = dir
	p.store = NewStore(dir)
	p.complete = ictx.Complete
	p.notice = ictx.Notice
	p.maxIndexEntries = plugin.CfgInt(cfg, "max_index_entries", defaultMaxIndexEntries)
	p.maxIndexBytes = plugin.CfgInt(cfg, "max_index_bytes", defaultMaxIndexBytes)
	p.maxEntryBytes = plugin.CfgInt(cfg, "max_entry_bytes", defaultMaxEntryBytes)
	p.autoExtract = plugin.CfgBool(cfg, "auto_extract", defaultAutoExtract)
	p.maxExtract = plugin.CfgInt(cfg, "max_extract", defaultMaxExtract)
	p.turnExtract = plugin.CfgBool(cfg, "turn_extract", defaultTurnExtract)
	p.turnEvery = plugin.CfgInt(cfg, "turn_extract_every", defaultTurnEvery)
	p.decay = plugin.CfgBool(cfg, "decay", defaultDecay)
	p.blurDays = blurDays
	p.forgetDays = forgetDays
	p.ctx, p.cancel = context.WithCancel(context.Background())

	p.turnMu.Lock()
	p.stopped = false
	p.turnMu.Unlock()
	return nil
}

// Stop 取消在途的提炼与清扫并有界等待。禁用插件、以新配置重新 Init、进程退出时被调用。
func (p *Plugin) Stop() { p.stopWorkers() }

// stopWorkers 取消后台 ctx 并等待已起的 goroutine 收尾，超过 stopWait 就不再等
// ——ctx 已经取消，在途的模型调用会自己断开。
func (p *Plugin) stopWorkers() {
	p.turnMu.Lock()
	p.stopped = true
	p.turnMu.Unlock()

	p.mu.Lock()
	cancel := p.cancel
	p.cancel, p.ctx = nil, nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopWait):
	}
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
	turnExtract     bool
	turnEvery       int
	decay           bool
	blurDays        int
	forgetDays      int
	ctx             context.Context
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
		turnExtract:     p.turnExtract,
		turnEvery:       p.turnEvery,
		decay:           p.decay,
		blurDays:        p.blurDays,
		forgetDays:      p.forgetDays,
		ctx:             p.ctx,
	}
}

// noticeFunc 返回当前的会话注记入口（可能为 nil，此时降级为只记日志）。
func (p *Plugin) noticeFunc() plugin.NoticeFunc {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.notice
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
已有记忆的结论被推翻时，用同一个标题修订它（save_memory 的 mode 设为 replace），
并在正文里交代被推翻的旧结论；不要换个说法另存一条，那会让两条互相打架的记忆同时留着。
对话历史被压缩后，摘要中若含上述内容，需要检查是否已经保存。`

// decayPrompt 只在开启淡忘时追加。
const decayPrompt = `
保存时还要判断这条记忆会不会随时间失去意义：近况、心情、当下的处境与安排属于会失去
意义的，把 decay 设为 true，它们久未提及会逐步淡忘直至移出记忆库；稳定的偏好、定下的
约定、不易重新得知的事实、验证过的踩坑不要设。
读到只剩一句要点的记忆时，说明它的细节已经淡忘了，就照这个程度说，不要凭空补细节。`

// SystemPrompt 只返回静态的保存判据。
//
// 记忆索引改由 TurnPrompt 注入：索引的内容取决于本轮的可见域，而 SystemPrompt 拿不到
// 可见域，一律全列会把不可读域的记忆标题漏出去。顺带的好处是本方法不再有任何磁盘
// 访问——列表接口会对禁用的插件也调用它。
func (p *Plugin) SystemPrompt() string {
	if p.snapshot().decay {
		return promptGuide + "\n" + decayPrompt
	}
	return promptGuide
}

// TurnPrompt 注入本轮可读的记忆索引。
// 记忆库为空时不注入：判据已在 SystemPrompt 里，那是引导保存第一条记忆的东西。
func (p *Plugin) TurnPrompt(ctx context.Context, _ plugin.TurnEvent) (string, error) {
	s := p.snapshot()
	if s.store == nil {
		return "", nil
	}
	entries, err := p.visibleEntries(ctx)
	if err != nil || len(entries) == 0 {
		return "", err
	}
	return promptHeader + "\n" + renderIndex(entries, s.maxIndexEntries, s.maxIndexBytes), nil
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
		// 用最后更新时间而不是创建时间：一条被反复修订的记忆，模型该看到的是
		// 这个结论最后一次被确认是什么时候，而不是这件事第一次被提起是什么时候。
		b.WriteString(e.Updated.Format("2006-01-02"))
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
