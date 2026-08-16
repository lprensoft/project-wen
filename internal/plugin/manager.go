package plugin

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Status 是插件在 /api/plugins 中展示的状态。
type Status struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	ToolNames   []string `json:"tool_names"`
	HasPrompt   bool     `json:"has_prompt"`
}

type entry struct {
	plugin  Plugin
	cfg     map[string]any
	enabled bool
	inited  bool
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

// Register 注册插件并按配置与持久化状态决定是否启用。
// 工具名与已注册插件冲突时拒绝注册。
func (m *Manager) Register(p Plugin, cfg PluginConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := p.Name()
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

	e := &entry{plugin: p, cfg: cfg.Config, enabled: cfg.Enabled}
	m.entries[name] = e
	m.order = append(m.order, name)

	// 持久化状态覆盖配置初始值
	if saved, ok := m.loadState()[name]; ok {
		e.enabled = saved
	}
	if e.enabled {
		if err := p.Init(m.ictx, e.cfg); err != nil {
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
		if err := e.plugin.Init(m.ictx, e.cfg); err != nil {
			return fmt.Errorf("插件 %q 初始化失败: %w", name, err)
		}
		e.inited = true
	}
	e.enabled = on
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
		out = append(out, Status{
			Name:        name,
			Description: e.plugin.Description(),
			Enabled:     e.enabled,
			ToolNames:   names,
			HasPrompt:   e.plugin.SystemPrompt() != "",
		})
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

// ---------- 状态持久化（plugins.state.json） ----------

func (m *Manager) loadState() map[string]bool {
	state := map[string]bool{}
	if m.statePath == "" {
		return state
	}
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	return state
}

func (m *Manager) saveStateLocked() {
	if m.statePath == "" {
		return
	}
	state := map[string]bool{}
	for name, e := range m.entries {
		state[name] = e.enabled
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	_ = os.MkdirAll(filepath.Dir(m.statePath), 0o755)
	if err := os.WriteFile(m.statePath, raw, 0o644); err != nil {
		log.Printf("保存插件状态失败: %v", err)
	}
}
