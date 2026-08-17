// Package heartbeat 按可动态调整的间隔定时唤醒模型，在最近活跃的会话上跑一轮心跳对话。
//
// 心跳轮次带上系统提示词与全部启用插件的提示词（RunTurn 的天然组装），输出直接落进
// 目标会话。心跳自身不算「活跃」：落点判定只看真人交互的轮次，否则心跳会不断自我续命。
// 动态心跳开启时，每轮真人对话结束后由辅助模型判定聊天的热度来加快或放缓节奏；
// 无人聊天时由内置的衰减定时器每 15 分钟逐步放缓，直到最慢间隔。
package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wen/internal/plugin"
	"wen/internal/session"
)

// 配置默认值。
const (
	defBaseMinutes = 30
	defMinMinutes  = 5
	defMaxMinutes  = 120
)

// defaultPrompt 是心跳提示词的默认值，用户可在设置页改成任何内容。
const defaultPrompt = `【心跳】这是一次定时唤醒，当前没有新的用户消息。请回顾本会话的进展：
- 若有未完成的任务、值得跟进的事项或需要主动提醒用户的内容，直接说给用户听；
- 若确实无事可说，用一句简短的话表明你还在即可，不要编造进展。`

// beatTimeout 是单次心跳轮次的时长上限。
const beatTimeout = 10 * time.Minute

// decayEvery 是空闲衰减定时器的周期：无人聊天时每次触发都把间隔放缓一档。
const decayEvery = 15 * time.Minute

// Plugin 是心跳插件。有状态：Init 会被反复调用（SetConfig），所有字段由 mu 保护，
// 重入时先停掉上一轮循环再起新的。
type Plugin struct {
	mu sync.Mutex

	// 配置（Init 写入）
	base    time.Duration
	minIv   time.Duration
	maxIv   time.Duration
	prompt  string
	dynamic bool

	// 能力与路径（Init 写入）
	stateDir   string
	runTurn    plugin.RunTurnFunc
	newSession plugin.NewSessionFunc
	complete   plugin.CompleteFunc
	sessions   *session.Store // 只读：用于挑选最近活跃的会话

	// 运行状态
	cur        time.Duration // 当前心跳间隔（持久化到 state.json）
	lastActive time.Time     // 最近一次真人交互轮次的时间
	lastBeat   time.Time
	adjusting  bool // 有一次动态判定在途，避免堆积

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "heartbeat" }

func (p *Plugin) Category() string { return plugin.CategoryBackground }

func (p *Plugin) Description() string {
	return "定时唤醒模型在最近活跃的会话上执行心跳提示词，节奏可随聊天热度动态调整"
}

func (p *Plugin) SystemPrompt() string { return "" }

func (p *Plugin) Tools() []plugin.Tool { return nil }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "interval_minutes", Label: "基础心跳间隔（分钟）", Type: plugin.FieldInt,
			Default: defBaseMinutes, Min: plugin.IntPtr(1), Max: plugin.IntPtr(1440),
			Description: "启用或重新配置后的初始心跳间隔",
		},
		{
			Key: "prompt", Label: "心跳提示词", Type: plugin.FieldText, Default: defaultPrompt,
			Description: "每次心跳作为输入执行的内容，连同系统提示词与已启用插件的提示词一起发给模型",
		},
		{
			Key: "dynamic", Label: "动态心跳", Type: plugin.FieldBool, Default: true,
			Description: "根据聊天的激烈程度与回复间隔自动加快或放缓心跳；无人聊天时逐步放缓到最慢间隔",
		},
		{
			Key: "min_minutes", Label: "最快间隔（分钟）", Type: plugin.FieldInt,
			Default: defMinMinutes, Min: plugin.IntPtr(1), Max: plugin.IntPtr(1440),
			Description: "动态提频的下限",
		},
		{
			Key: "max_minutes", Label: "最慢间隔（分钟）", Type: plugin.FieldInt,
			Default: defMaxMinutes, Min: plugin.IntPtr(1), Max: plugin.IntPtr(2880),
			Description: "动态降频与空闲衰减的上限",
		},
	}
}

// Init 应用配置并（重）启动心跳循环。可重入：先停掉上一轮循环。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	base := time.Duration(plugin.CfgInt(cfg, "interval_minutes", defBaseMinutes)) * time.Minute
	minIv := time.Duration(plugin.CfgInt(cfg, "min_minutes", defMinMinutes)) * time.Minute
	maxIv := time.Duration(plugin.CfgInt(cfg, "max_minutes", defMaxMinutes)) * time.Minute
	if minIv > base || base > maxIv {
		return fmt.Errorf("心跳间隔需满足 最快(%v) ≤ 基础(%v) ≤ 最慢(%v)", minIv, base, maxIv)
	}
	if ictx.StateDir == "" {
		return fmt.Errorf("没有可用的持久化目录，无法记录心跳状态")
	}
	if ictx.RunTurn == nil {
		return fmt.Errorf("当前环境不支持插件发起对话轮次")
	}
	sessions, err := session.NewStore(ictx.SessionDir)
	if err != nil {
		return fmt.Errorf("打开会话目录失败: %w", err)
	}

	p.Stop() // 重入：先停上一轮循环，避免两套定时器并行

	p.mu.Lock()
	defer p.mu.Unlock()
	p.base, p.minIv, p.maxIv = base, minIv, maxIv
	p.prompt = plugin.CfgString(cfg, "prompt", defaultPrompt)
	p.dynamic = plugin.CfgBool(cfg, "dynamic", true)
	p.stateDir = ictx.StateDir
	p.runTurn = ictx.RunTurn
	p.newSession = ictx.NewSession
	p.complete = ictx.Complete
	p.sessions = sessions

	// 当前间隔：优先用上次持久化的值（限幅到新配置范围），否则用基础间隔
	p.cur = p.loadIntervalLocked()
	p.lastBeat = time.Now()
	p.lastActive = p.probeLastActiveLocked()

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.wake = make(chan struct{}, 1)
	p.wg.Add(1)
	go p.loop(p.ctx)
	return nil
}

// Stop 停止心跳循环。只做取消与有界等待，符合 Stoppable 契约。
func (p *Plugin) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.wg.Wait() // 循环体内所有阻塞点都挂在 ctx 上，取消后即刻返回
	}
}

// ---------- 状态持久化（state.json：只存当前间隔） ----------

type state struct {
	IntervalSeconds int `json:"interval_seconds"`
}

func (p *Plugin) statePath() string { return filepath.Join(p.stateDir, "state.json") }

// loadIntervalLocked 读上次的间隔并限幅到当前配置范围；无状态时用基础间隔。
func (p *Plugin) loadIntervalLocked() time.Duration {
	raw, err := os.ReadFile(p.statePath())
	if err != nil {
		return p.base
	}
	var st state
	if json.Unmarshal(raw, &st) != nil || st.IntervalSeconds <= 0 {
		return p.base
	}
	return p.clamp(time.Duration(st.IntervalSeconds) * time.Second)
}

// saveInterval 持久化当前间隔。失败只影响下次启动的初值，不值得让调用方失败。
func (p *Plugin) saveInterval(iv time.Duration) {
	p.mu.Lock()
	dir := p.stateDir
	p.mu.Unlock()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	raw, _ := json.Marshal(state{IntervalSeconds: int(iv / time.Second)})
	_ = os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o644)
}

func (p *Plugin) clamp(iv time.Duration) time.Duration {
	if iv < p.minIv {
		return p.minIv
	}
	if iv > p.maxIv {
		return p.maxIv
	}
	return iv
}

// probeLastActiveLocked 从会话元数据推最近一次真人交互时间，进程重启后衰减判定不失忆。
func (p *Plugin) probeLastActiveLocked() time.Time {
	var last time.Time
	metas, err := p.sessions.List()
	if err != nil {
		return last
	}
	for _, m := range metas {
		if m.LastActiveAt != nil && m.LastActiveAt.After(last) {
			last = *m.LastActiveAt
		}
	}
	return last
}
