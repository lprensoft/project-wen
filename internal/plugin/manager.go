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
	Enabled     bool     `json:"enabled"`
	ToolNames   []string `json:"tool_names"`
	HasPrompt   bool     `json:"has_prompt"`
	// ConfigFields 为空表示该插件没有可配置项（界面不显示配置入口）。
	ConfigFields []ConfigField  `json:"config_fields,omitempty"`
	Config       map[string]any `json:"config,omitempty"` // 当前生效值（含默认值）
}

type entry struct {
	plugin  Plugin
	source  string
	baseCfg map[string]any // 配置文件 plugins.<name>.config
	userCfg map[string]any // 界面上保存过的配置（持久化到状态文件，覆盖 baseCfg）
	cfg     map[string]any // 生效配置 = baseCfg 叠加 userCfg
	enabled bool
	inited  bool
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
func (m *Manager) initCtxFor(name string) InitContext {
	ictx := m.ictx
	if m.statePath != "" {
		ictx.StateDir = filepath.Join(filepath.Dir(m.statePath), "plugins", name)
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
func (m *Manager) SetEnabled(name string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[name]
	if !ok {
		return fmt.Errorf("插件 %q 不存在", name)
	}
	if on && !e.inited {
		if err := e.plugin.Init(m.initCtxFor(name), e.cfg); err != nil {
			return fmt.Errorf("插件 %q 初始化失败: %w", name, err)
		}
		e.inited = true
	}
	e.enabled = on
	m.saveStateLocked()
	return nil
}

// SetConfig 保存插件配置并持久化。已初始化的插件会以新配置重新 Init，
// 失败时回滚到旧配置。配置中未声明的键被忽略，缺失的键取默认值。
func (m *Manager) SetConfig(name string, cfg map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[name]
	if !ok {
		return fmt.Errorf("插件 %q 不存在", name)
	}
	fields := ConfigFieldsOf(e.plugin)
	if len(fields) == 0 {
		return fmt.Errorf("插件 %q 没有可配置项", name)
	}
	next, err := NormalizeConfig(fields, cfg)
	if err != nil {
		return err
	}

	oldUser, oldCfg := e.userCfg, e.cfg
	e.userCfg = next
	e.applyCfg()
	if e.inited {
		if err := e.plugin.Init(m.initCtxFor(name), e.cfg); err != nil {
			e.userCfg, e.cfg = oldUser, oldCfg
			_ = e.plugin.Init(m.initCtxFor(name), oldCfg) // 尽力恢复旧配置下的状态
			return fmt.Errorf("插件 %q 应用配置失败: %w", name, err)
		}
	}
	m.saveStateLocked()
	return nil
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
			Enabled:     e.enabled,
			ToolNames:   names,
			HasPrompt:   e.plugin.SystemPrompt() != "",
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
		// 只持久化界面上改过的配置，未改动的插件仍跟随配置文件
		state[name] = persistedEntry{Enabled: e.enabled, Config: e.userCfg}
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	_ = os.MkdirAll(filepath.Dir(m.statePath), 0o755)
	if err := os.WriteFile(m.statePath, raw, 0o644); err != nil {
		log.Printf("保存插件状态失败: %v", err)
	}
}
