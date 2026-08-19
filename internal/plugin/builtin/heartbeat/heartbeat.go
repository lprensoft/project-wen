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
			Description: "每次心跳作为输入执行的内容，连同系统提示词与已启用插件的提示词一起发给模型。" +
				"末尾会自动附上「距上次对话」的时长，提示词里不必再写「很久没聊」这类模糊措辞",
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

	// 间隔与倒计时起点都接着上次：只存间隔是不够的，「每 60 分钟一次」还得知道
	// 「上次是几点」才推得出下一次
	st := p.loadStateLocked()
	p.cur = st.interval(p.base, p.clamp)
	p.lastActive = p.probeLastActiveLocked()
	p.lastBeat = p.resumeLastBeat(st.LastBeat, p.lastActive, time.Now())

	// 起点当场落盘，别等第一次心跳才写：否则「启用后还没跳过一次就重启」这一段
	// 时间里状态文件仍是空的，倒计时又会从头开始。persistState 不碰 p.mu，
	// 在锁内直接调是安全的，且写完即返回，不会脱离 Init 的生命周期。
	persistState(p.snapshotStateLocked())

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

// ---------- 状态持久化（state.json） ----------

// state 是重启后要接着用的东西：当前间隔，以及上一次心跳发生在什么时刻。
//
// 此前只存了间隔，Init 把倒计时清零重算，于是重启比心跳间隔更频繁时，心跳一次都
// 不会触发。定时类状态一律记「上一次发生的时刻」而不是「还剩多久」——进程内的
// 定时器只是执行手段，不是状态载体。
type state struct {
	IntervalSeconds int       `json:"interval_seconds"`
	LastBeat        time.Time `json:"last_beat,omitempty"`
}

// interval 返回可用的间隔：持久化值经限幅，缺失或非法时退回基础间隔。
func (st state) interval(base time.Duration, clamp func(time.Duration) time.Duration) time.Duration {
	if st.IntervalSeconds <= 0 {
		return base
	}
	return clamp(time.Duration(st.IntervalSeconds) * time.Second)
}

// startupGrace 是重启后补心跳的宽限期。
//
// 关机时长超过心跳间隔时，恢复后「早就该心跳了」，循环会立刻补上一次。补是对的
// ——心跳的语义是隔一阵子主动开口，提示词里还会带上真实间隔——但不该在服务刚起来
// 的那一秒就开口：那时人多半正打算自己说话，调试期间反复重启尤其明显。
const startupGrace = 2 * time.Minute

// resumeLastBeat 定出重启后的倒计时起点。
//
// 取「上次心跳」与「上次真人对话」中较晚的一个：真人聊完会重置心跳时钟
// （见 OnTurnEnd），而那个时刻记在会话元数据里，因此不必在轮次收尾的同步路径上
// 写盘——那条路径要求快速返回，每轮多一次写盘不值得。
func (p *Plugin) resumeLastBeat(lastBeat, lastActive, now time.Time) time.Time {
	last := lastBeat
	if lastActive.After(last) {
		last = lastActive
	}
	if last.IsZero() {
		return now // 没有先前状态（首次启用）：等满一个完整间隔，而不是立刻心跳
	}
	if earliest := now.Add(startupGrace - p.cur); last.Before(earliest) {
		return earliest // 已经过期，补一次，但至少等到宽限期之后
	}
	return last
}

func (p *Plugin) statePath() string { return filepath.Join(p.stateDir, "state.json") }

// loadStateLocked 读上次的状态；文件缺失或损坏时返回零值，由调用方回退。
func (p *Plugin) loadStateLocked() state {
	raw, err := os.ReadFile(p.statePath())
	if err != nil {
		return state{}
	}
	var st state
	if json.Unmarshal(raw, &st) != nil {
		return state{}
	}
	return st
}

// snapshotStateLocked 取出该落盘的内容。调用方需持有 p.mu，写盘在锁外做——
// 持锁写盘会让调用方只能另起 goroutine 绕开自锁，而那种写会脱离插件的生命周期，
// 在插件停掉之后才落地（测试里表现为临时目录清理时「目录非空」）。
func (p *Plugin) snapshotStateLocked() (string, state) {
	return p.stateDir, state{
		IntervalSeconds: int(p.cur / time.Second),
		LastBeat:        p.lastBeat,
	}
}

// persistState 写状态文件。失败只影响下次启动的初值，不值得让调用方失败。
func persistState(dir string, st state) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	raw, _ := json.Marshal(st)
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
