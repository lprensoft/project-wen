package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// 插件来源。
const (
	SourceBuiltin  = "builtin"  // 随程序内置
	SourceExternal = "external" // 来自外部来源
)

// SourceLabel 返回来源的中文显示名。
func SourceLabel(source string) string {
	switch source {
	case SourceBuiltin:
		return "内置"
	case SourceExternal:
		return "外源"
	}
	return source
}

// Status 是插件在 /api/plugins 中展示的状态。
type Status struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Category    string   `json:"category"` // 功能分组（设置页分节展示用）
	Enabled     bool     `json:"enabled"`
	ToolNames   []string `json:"tool_names"`
	HasPrompt   bool     `json:"has_prompt"`
	// ConfigFields 为空表示该插件没有可配置项（界面不显示配置入口）。
	ConfigFields []ConfigField  `json:"config_fields,omitempty"`
	Config       map[string]any `json:"config,omitempty"` // 当前生效值（含默认值）
	// 依赖与冲突。Unmet / Conflicting 由 Manager 算好，界面直接用，不必自己推。
	Requires    []string `json:"requires,omitempty"`    // 声明的依赖
	Unmet       []string `json:"unmet,omitempty"`       // 其中未满足的（未注册或未启用）
	Conflicts   []string `json:"conflicts,omitempty"`   // 声明的冲突项
	Conflicting []string `json:"conflicting,omitempty"` // 其中当前已启用的
	// Actions 是插件声明的操作入口，仅在启用且初始化成功时暴露。
	Actions []ActionDef `json:"actions,omitempty"`
}

type entry struct {
	plugin  Plugin
	source  string
	baseCfg map[string]any // 配置文件 plugins.<name>.config
	userCfg map[string]any // 界面上保存过的配置（持久化到状态文件，覆盖 baseCfg）
	cfg     map[string]any // 生效配置 = baseCfg 叠加 userCfg
	enabled bool
	inited  bool
	// forcedOff 表示「用户想开，但依赖没满足所以被强制关掉」。持久化时按想开来写，
	// 使依赖恢复后插件能自动回来，见 Resolve 的说明。
	forcedOff bool
}

// applyCfg 重算生效配置。
func (e *entry) applyCfg() {
	if len(e.userCfg) == 0 {
		e.cfg = e.baseCfg
		return
	}
	merged := make(map[string]any, len(e.baseCfg)+len(e.userCfg))
	maps.Copy(merged, e.baseCfg)
	maps.Copy(merged, e.userCfg)
	e.cfg = merged
}

// Manager 管理系统插件：注册、开关、状态持久化、工具与提示词聚合。
type Manager struct {
	mu        sync.RWMutex
	ictx      InitContext
	statePath string // 运行时开关状态文件（覆盖配置的初始值）
	entries   map[string]*entry
	order     []string // 注册顺序（提示词按此序拼接）
}

// PluginConfig 来自配置文件 plugins.<name> 段。
type PluginConfig struct {
	Enabled bool           `yaml:"enabled"`
	Config  map[string]any `yaml:"config"`
}

func NewManager(ictx InitContext, statePath string) *Manager {
	return &Manager{ictx: ictx, statePath: statePath, entries: map[string]*entry{}}
}

// validName 限定插件名的取值，因为它会被用来拼持久化目录。
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// initCtxFor 返回该插件专属的运行环境：在公共 InitContext 上补一个按插件名隔离的持久化目录。
// 未配置状态文件时 StateDir 留空，由插件自行决定是否拒绝启用。
// RunTurn 与 Notice 被包一层自动注入发起方标记——发起方由 Manager 裁定，
// 插件无法伪装成前台。
func (m *Manager) initCtxFor(name string) InitContext {
	ictx := m.ictx
	if m.statePath != "" {
		ictx.StateDir = filepath.Join(filepath.Dir(m.statePath), "plugins", name)
	}
	if base := m.ictx.RunTurn; base != nil {
		ictx.RunTurn = func(ctx context.Context, sessionID, input string) (string, error) {
			return base(WithTurnOrigin(ctx, name), sessionID, input)
		}
	}
	if base := m.ictx.Notice; base != nil {
		ictx.Notice = func(ctx context.Context, sessionID, text string) error {
			return base(WithTurnOrigin(ctx, name), sessionID, text)
		}
	}
	return ictx
}

// Register 注册内置插件，其余行为见 register。
func (m *Manager) Register(p Plugin, cfg PluginConfig) error {
	return m.register(p, cfg, SourceBuiltin)
}

// RegisterExternal 注册来自外部来源的插件（界面上标记为「外源」）。
func (m *Manager) RegisterExternal(p Plugin, cfg PluginConfig) error {
	return m.register(p, cfg, SourceExternal)
}

// register 注册插件并按配置与持久化状态决定是否启用。
// 工具名与已注册插件冲突时拒绝注册。
func (m *Manager) register(p Plugin, cfg PluginConfig, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := p.Name()
	if !validName.MatchString(name) {
		return fmt.Errorf("插件名 %q 非法（只允许小写字母开头的小写字母、数字与下划线）", name)
	}
	if _, exists := m.entries[name]; exists {
		return fmt.Errorf("插件 %q 重复注册", name)
	}
	for _, t := range p.Tools() {
		for otherName, other := range m.entries {
			for _, ot := range other.plugin.Tools() {
				if ot.Name() == t.Name() {
					return fmt.Errorf("插件 %q 的工具 %q 与插件 %q 冲突", name, t.Name(), otherName)
				}
			}
		}
	}

	e := &entry{plugin: p, source: source, baseCfg: cfg.Config, cfg: cfg.Config, enabled: cfg.Enabled}
	m.entries[name] = e
	m.order = append(m.order, name)

	// 持久化状态覆盖配置文件的初始值（界面上的改动优先）
	if saved, ok := m.loadState()[name]; ok {
		e.enabled = saved.Enabled
		e.userCfg = saved.Config
		// JSON 会把整数读成 float64，按字段声明还原类型；非法值退回配置文件
		if fields := ConfigFieldsOf(p); len(fields) > 0 && len(e.userCfg) > 0 {
			values, err := NormalizeConfig(fields, e.userCfg)
			if err != nil {
				log.Printf("插件 %q 的持久化配置无效，改用配置文件的值: %v", name, err)
				values = nil
			}
			e.userCfg = values
		}
		e.applyCfg()
	}
	if e.enabled {
		if err := p.Init(m.initCtxFor(name), e.cfg); err != nil {
			e.enabled = false
			log.Printf("插件 %q 初始化失败，保持禁用: %v", name, err)
		} else {
			e.inited = true
		}
	}
	return nil
}

// SetEnabled 运行时启用/禁用插件并持久化状态。启用时执行 Init，失败保持禁用。
// 依赖校验在 Init 之前完成：依赖不满足时不该产生任何副作用。
// 禁用有后台活动的插件时调用其 Stop——必须在锁外：插件的后台 goroutine 可能正阻塞在
// Manager 的读锁上（如 SystemPrompts），锁内等待会死锁。
func (m *Manager) SetEnabled(name string, on bool) error {
	m.mu.Lock()
	var toStop Stoppable

	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("插件 %q 不存在", name)
	}
	if on {
		if err := m.checkEnableLocked(name); err != nil {
			m.mu.Unlock()
			return err
		}
		if !e.inited {
			if err := e.plugin.Init(m.initCtxFor(name), e.cfg); err != nil {
				m.mu.Unlock()
				return fmt.Errorf("插件 %q 初始化失败: %w", name, err)
			}
			e.inited = true
		}
	} else {
		if err := m.checkDisableLocked(name); err != nil {
			m.mu.Unlock()
			return err
		}
		if s, ok := e.plugin.(Stoppable); ok && e.inited {
			// 停止后插件的内部状态已拆除，重新启用时必须重新 Init
			toStop = s
			e.inited = false
		}
	}
	e.enabled = on
	e.forcedOff = false // 用户显式操作过，此后以他的意图为准
	m.saveStateLocked()
	m.mu.Unlock()

	if toStop != nil {
		toStop.Stop()
	}
	return nil
}

// SetConfig 保存插件配置并持久化。已初始化的插件会以新配置重新 Init，
// 失败时回滚到旧配置。配置中未声明的键被忽略，缺失的键取默认值。
//
// 有后台活动的插件在重新 Init 前先在锁外 Stop（锁内调用可能与插件 goroutine 持有的
// 读锁死锁）。解锁窗口内其它请求可能穿插进来，代价是短暂读到「已停止但未重配」的状态，
// 可容忍；有状态插件本就要求 Init 可重入、自行加锁。
func (m *Manager) SetConfig(name string, cfg map[string]any) error {
	m.mu.Lock()

	e, ok := m.entries[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("插件 %q 不存在", name)
	}
	fields := ConfigFieldsOf(e.plugin)
	if len(fields) == 0 {
		m.mu.Unlock()
		return fmt.Errorf("插件 %q 没有可配置项", name)
	}
	next, err := NormalizeConfig(fields, cfg)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	if s, ok := e.plugin.(Stoppable); ok && e.inited {
		m.mu.Unlock()
		s.Stop()
		m.mu.Lock()
	}

	oldUser, oldCfg := e.userCfg, e.cfg
	e.userCfg = next
	e.applyCfg()
	if e.inited {
		if err := e.plugin.Init(m.initCtxFor(name), e.cfg); err != nil {
			e.userCfg, e.cfg = oldUser, oldCfg
			_ = e.plugin.Init(m.initCtxFor(name), oldCfg) // 尽力恢复旧配置下的状态
			m.mu.Unlock()
			return fmt.Errorf("插件 %q 应用配置失败: %w", name, err)
		}
	}
	m.saveStateLocked()
	m.mu.Unlock()
	return nil
}

// actionableFor 解析出可执行操作的插件：必须存在、启用、已初始化且实现 Actionable。
// 返回后在锁外调用其方法——操作可能有网络请求，不能占着 Manager 的锁。
func (m *Manager) actionableFor(name string) (Actionable, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return nil, fmt.Errorf("插件 %q 不存在", name)
	}
	if !e.enabled || !e.inited {
		return nil, fmt.Errorf("插件 %q 未启用", name)
	}
	a, ok := e.plugin.(Actionable)
	if !ok {
		return nil, fmt.Errorf("插件 %q 没有操作入口", name)
	}
	return a, nil
}

// StartAction 触发插件的一个操作。
func (m *Manager) StartAction(ctx context.Context, name, key string) error {
	a, err := m.actionableFor(name)
	if err != nil {
		return err
	}
	return a.StartAction(ctx, key)
}

// ActionState 查询插件操作的当前状态。
func (m *Manager) ActionState(name, key string) (ActionState, error) {
	a, err := m.actionableFor(name)
	if err != nil {
		return ActionState{}, err
	}
	return a.ActionState(key)
}

// StopAll 停止所有有后台活动的已初始化插件，供进程退出时调用。
// 与 SetEnabled 相同的理由，Stop 在锁外进行。
func (m *Manager) StopAll() {
	m.mu.RLock()
	var stops []Stoppable
	for _, name := range m.order {
		e := m.entries[name]
		if !e.inited {
			continue
		}
		if s, ok := e.plugin.(Stoppable); ok {
			stops = append(stops, s)
		}
	}
	m.mu.RUnlock()
	for _, s := range stops {
		s.Stop()
	}
}

// List 返回全部插件状态（按注册顺序）。
func (m *Manager) List() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		e := m.entries[name]
		names := []string{}
		for _, t := range e.plugin.Tools() {
			names = append(names, t.Name())
		}
		st := Status{
			Name:        name,
			Description: e.plugin.Description(),
			Source:      e.source,
			Category:    CategoryOf(e.plugin),
			Enabled:     e.enabled,
			ToolNames:   names,
			HasPrompt:   e.plugin.SystemPrompt() != "",
			Requires:    RequiresOf(e.plugin),
			Unmet:       m.unmetLocked(name),
			Conflicts:   ConflictsOf(e.plugin),
			Conflicting: m.conflictingLocked(name),
		}
		if a, ok := e.plugin.(Actionable); ok && e.enabled && e.inited {
			st.Actions = a.Actions()
		}
		if fields := ConfigFieldsOf(e.plugin); len(fields) > 0 {
			st.ConfigFields = fields
			// 配置非法时（如手改配置文件）退回默认值展示，不影响列表可用
			values, err := NormalizeConfig(fields, e.cfg)
			if err != nil {
				values, _ = NormalizeConfig(fields, nil)
			}
			st.Config = values
		}
		out = append(out, st)
	}
	return out
}

// EnabledTools 返回所有启用插件的工具（按注册顺序）。
func (m *Manager) EnabledTools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Tool
	for _, name := range m.order {
		if e := m.entries[name]; e.enabled {
			out = append(out, e.plugin.Tools()...)
		}
	}
	return out
}

// FindTool 在启用插件中按名查找工具。
func (m *Manager) FindTool(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, pname := range m.order {
		e := m.entries[pname]
		if !e.enabled {
			continue
		}
		for _, t := range e.plugin.Tools() {
			if t.Name() == name {
				return t, true
			}
		}
	}
	return nil, false
}

// SystemPrompts 返回所有启用插件的非空提示词片段（按注册顺序）。
func (m *Manager) SystemPrompts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []string
	for _, name := range m.order {
		if e := m.entries[name]; e.enabled {
			if p := e.plugin.SystemPrompt(); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// StatusLines 返回所有启用插件贡献的状态行（按注册顺序）。
// 只问启用的插件：被禁用的插件没有运行状况可言，报一行「已停」只是噪声。
func (m *Manager) StatusLines() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []string
	for _, name := range m.order {
		e := m.entries[name]
		if !e.enabled {
			continue
		}
		r, ok := e.plugin.(StatusReporter)
		if !ok {
			continue
		}
		for _, line := range r.StatusLines() {
			if line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

// DecideScope 裁决本轮对话的可见域。
//
// 单所有者：按注册顺序第一个返回非零 Scope 的插件胜出，其余被忽略并记日志。
// Write 会被插件用来拼持久化目录，因此按插件名的字符集校验，非法则整条裁决作废
// 降级为零值——降级成「不限制」比让一个 "../x" 之类的标签流进文件路径安全得多。
func (m *Manager) DecideScope(ctx context.Context, ev TurnEvent) Scope {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out Scope
	owner := ""
	for _, name := range m.order {
		e := m.entries[name]
		if !e.enabled {
			continue
		}
		d, ok := e.plugin.(ScopeDecider)
		if !ok {
			continue
		}
		sc, err := d.DecideScope(ctx, ev)
		if err != nil {
			log.Printf("插件 %q 裁决可见域失败，按不限制处理: %v", name, err)
			continue
		}
		if sc.IsZero() {
			continue
		}
		if sc.Write != "" && !validName.MatchString(sc.Write) {
			log.Printf("插件 %q 返回了非法的可见域标签 %q，已忽略", name, sc.Write)
			continue
		}
		if owner != "" {
			log.Printf("插件 %q 的可见域裁决被忽略：本轮已由插件 %q 决定", name, owner)
			continue
		}
		out, owner = sc, name
	}
	return out
}

// TurnPrompts 在可见域裁决完成后收集各插件的一次性提示词片段（按注册顺序，已滤空）。
// 插件返回的错误只记日志：少一段提示词应当降级，不该让整轮对话失败。
func (m *Manager) TurnPrompts(ctx context.Context, ev TurnEvent) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []string
	for _, name := range m.order {
		e := m.entries[name]
		if !e.enabled {
			continue
		}
		tp, ok := e.plugin.(TurnPrompter)
		if !ok {
			continue
		}
		s, err := tp.TurnPrompt(ctx, ev)
		if err != nil {
			log.Printf("插件 %q 生成本轮提示词失败: %v", name, err)
			continue
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// NotifyCompact 在会话历史被替换前广播压缩事件，返回各插件的注记（按注册顺序，已滤掉空串）。
// 插件返回的错误只记录日志，不阻断压缩——压缩是上下文溢出时的保底手段，不能被插件卡住。
func (m *Manager) NotifyCompact(ctx context.Context, ev CompactEvent) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var notes []string
	for _, name := range m.order {
		e := m.entries[name]
		if !e.enabled {
			continue
		}
		lc, ok := e.plugin.(Lifecycle)
		if !ok {
			continue
		}
		note, err := lc.OnCompact(ctx, ev)
		if err != nil {
			log.Printf("插件 %q 处理压缩事件失败: %v", name, err)
			continue
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

// NotifyTurnEnd 在一轮对话成功结束后广播给所有实现 TurnObserver 的启用插件。
// 广播发生在轮次收尾的同步路径上：逐个 recover，单个插件的 panic 不能连累整轮对话。
func (m *Manager) NotifyTurnEnd(ctx context.Context, ev TurnEndEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, name := range m.order {
		e := m.entries[name]
		if !e.enabled {
			continue
		}
		ob, ok := e.plugin.(TurnObserver)
		if !ok {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("插件 %q 处理轮次结束事件时 panic: %v", name, r)
				}
			}()
			ob.OnTurnEnd(ctx, ev)
		}()
	}
}

// ---------- 状态持久化（plugins.state.json） ----------

// persistedEntry 是单个插件的持久化状态（开关 + 界面上改过的配置）。
type persistedEntry struct {
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config,omitempty"`
}

// loadState 读取状态文件，兼容旧格式（name -> bool）。
func (m *Manager) loadState() map[string]persistedEntry {
	state := map[string]persistedEntry{}
	if m.statePath == "" {
		return state
	}
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return state
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return state
	}
	for name, item := range items {
		var enabled bool
		if err := json.Unmarshal(item, &enabled); err == nil {
			state[name] = persistedEntry{Enabled: enabled}
			continue
		}
		var pe persistedEntry
		if err := json.Unmarshal(item, &pe); err == nil {
			state[name] = pe
		}
	}
	return state
}

func (m *Manager) saveStateLocked() {
	if m.statePath == "" {
		return
	}
	state := map[string]persistedEntry{}
	for name, e := range m.entries {
		// 只持久化界面上改过的配置，未改动的插件仍跟随配置文件；
		// 被依赖强制关掉的插件按「用户想开」持久化，依赖恢复后能自动回来
		state[name] = persistedEntry{Enabled: e.enabled || e.forcedOff, Config: e.userCfg}
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	_ = os.MkdirAll(filepath.Dir(m.statePath), 0o755)
	// 0600：插件配置里可能有用户不愿外泄的内容（提示词设定、触发词等），与 models.json 一致
	if err := os.WriteFile(m.statePath, raw, 0o600); err != nil {
		log.Printf("保存插件状态失败: %v", err)
	}
}
