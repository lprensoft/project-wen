// Package weather 提供天气感知的系统插件：按用户填写的城市定时取现实天气，
// 把此刻的天气每轮注入上下文，作为角色对对方处境的感知。
//
// 它注入的是环境状态而不是一个可调用的工具——这是它依赖 roleplay 的原因。通用对话
// 里每轮塞一行天气只是噪声，真要问天气，模型自己抓网页就够了；只有在有人被扮演时，
// 「对方那边在下雨」才是一条持续起作用的背景。
//
// 天气在后台按固定间隔刷新，注入路径只读内存里的缓存：TurnPrompt 在每轮对话的同步
// 路径上，在那里发网络请求就是让每一轮都替一次超时买单。
package weather

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"wen/internal/plugin"
)

const (
	defaultRefreshMinutes = 30
	defaultStaleMinutes   = 60
	// minRefreshMinutes 是刷新间隔的下限。数据源是免费的公共接口，取得再勤也没有
	// 更新的数据，只是白白打对方。
	minRefreshMinutes = 10

	// requestTimeout 是单次取数（含地址解析）的总时限。
	requestTimeout = 20 * time.Second
)

// errNotReady 表示插件尚未初始化完成。
var errNotReady = fmt.Errorf("插件尚未就绪")

// Plugin 是 weather 系统插件。
type Plugin struct {
	mu       sync.RWMutex
	location string
	refresh  time.Duration
	stale    time.Duration
	client   *http.Client

	// 后台刷新循环。Init 可重入，每次先停旧的再起新的。
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 观测缓存。单独一把锁：它由后台循环写、由注入路径读，与配置的生命周期无关。
	dataMu   sync.RWMutex
	placeKey string // 已解析的地名原文，与 location 不符时重新解析
	place    Place
	placeOK  bool
	cur      Report
	curOK    bool
	lastErr  string

	// 设置页操作的状态。StartAction 与 ActionState 会被 HTTP 并发调用，自带一把锁。
	actMu    sync.Mutex
	actSeq   uint64
	actState plugin.ActionState
}

func New() *Plugin {
	return &Plugin{
		refresh: defaultRefreshMinutes * time.Minute,
		stale:   defaultStaleMinutes * time.Minute,
	}
}

func (p *Plugin) Name() string { return "weather" }

func (p *Plugin) Category() string { return plugin.CategoryPersona }

func (p *Plugin) Description() string {
	return "天气感知：按设定的城市定时获取现实天气，作为角色对对方处境的感知每轮注入"
}

// Requires 硬依赖 roleplay：没有角色，一行天气就只是上下文里的噪声。
func (p *Plugin) Requires() []string { return []string{"roleplay"} }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			// 标签用「我」：填的人就是你自己。注入给模型的那一段写「对方所在地」——
			// 提示词是模型以第一人称读的，那里的「我」指它自己。与 roleplay 的
			// 「我的信息」是同一处人称约定。
			Key: "location", Label: "我所在的城市", Type: plugin.FieldString,
			Description: "用于查询现实天气的地名，如「杭州」「上海」「Hangzhou」。" +
				"留空则不查询、也不注入任何天气——不要为了有东西可注入而随便填一个。",
			Default: "",
		},
		{
			Key: "refresh_minutes", Label: "刷新间隔（分钟）", Type: plugin.FieldInt,
			Description: "后台每隔多久取一次天气。取得再勤也没有更新的数据，只是白打接口。",
			Default:     defaultRefreshMinutes,
			Min:         plugin.IntPtr(minRefreshMinutes),
			Max:         plugin.IntPtr(1440),
		},
		{
			Key: "stale_minutes", Label: "过期不注入的时限（分钟）", Type: plugin.FieldInt,
			Description: "距上次成功取得超过这个时间就不再注入天气——取不到新的时，" +
				"宁可让角色不知道天气，也不要让它把几个小时前的天气当作此刻。" +
				"不得小于刷新间隔。",
			Default: defaultStaleMinutes,
			Min:     plugin.IntPtr(minRefreshMinutes),
			Max:     plugin.IntPtr(10080),
		},
	}
}

// Init 应用配置并（重）启动后台刷新循环。可重入：先停掉上一轮循环。
//
// 这里不发网络请求：启动时断网不该让插件启用失败，取不到就是没有天气可注入而已。
func (p *Plugin) Init(_ plugin.InitContext, cfg map[string]any) error {
	location := strings.TrimSpace(plugin.CfgString(cfg, "location", ""))
	refreshMin := plugin.CfgInt(cfg, "refresh_minutes", defaultRefreshMinutes)
	staleMin := plugin.CfgInt(cfg, "stale_minutes", defaultStaleMinutes)
	if staleMin < refreshMin {
		return fmt.Errorf("「过期不注入的时限」(%d 分钟) 不能小于「刷新间隔」(%d 分钟)："+
			"那样刚取到的天气也会立刻被判为过期，等于永远不注入", staleMin, refreshMin)
	}

	p.Stop() // 重入：先停上一轮循环，避免两套定时器并行

	// 只在城市变了的时候丢弃缓存。改个刷新间隔就把手上这份好数据扔掉，
	// 万一此刻正好取不到，就白白空窗一段。
	// 这一段刻意不与下面的 p.mu 嵌套：两把锁全程各用各的，就不存在取锁顺序问题。
	p.dataMu.Lock()
	if p.placeKey != location {
		p.placeKey, p.place, p.placeOK = "", Place{}, false
		p.cur, p.curOK, p.lastErr = Report{}, false, ""
	}
	p.dataMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.location = location
	p.refresh = time.Duration(refreshMin) * time.Minute
	p.stale = time.Duration(staleMin) * time.Minute
	p.client = &http.Client{Timeout: requestTimeout}
	if location == "" {
		return nil // 没填城市就不起循环，也不注入任何东西
	}

	// 循环起在锁内，与 cancel 的记录是同一步：分成两步的话，中间来一次 Stop
	// 会看到 cancel 还是 nil，于是这个刚起来的循环就没人停得掉了。
	var ctx context.Context
	ctx, p.cancel = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.loop(ctx, p.client, location, p.refresh)
	return nil
}

// Stop 停止刷新循环。只做取消与有界等待，符合 Stoppable 契约。
func (p *Plugin) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.wg.Wait() // 循环里的阻塞点都挂在 ctx 上，取消后即刻返回
	}
}

func (p *Plugin) Tools() []plugin.Tool { return nil }

// settings 是一次调用期间使用的配置快照。
type settings struct {
	location string
	refresh  time.Duration
	stale    time.Duration
	client   *http.Client
}

// snapshot 取一份配置快照：SetConfig 会在运行时重新 Init，而注入可能正在进行。
func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{location: p.location, refresh: p.refresh, stale: p.stale, client: p.client}
}

// SystemPrompt 注入天气的使用规则，是静态内容。没填城市时整段不注入：
// 那时候永远不会有 [当前天气]，一段解释怎么用它的规则只是白占上下文。
func (p *Plugin) SystemPrompt() string {
	if p.snapshot().location == "" {
		return ""
	}
	return guidePrompt
}

// TurnPrompt 注入此刻的天气。取不到或已过期时不注入：提示词里已经写了
// 「没有给出天气时就当作不知道」，让它不知道，好过让它把旧天气当成现在的。
func (p *Plugin) TurnPrompt(_ context.Context, _ plugin.TurnEvent) (string, error) {
	rep, ok := p.fresh(time.Now())
	if !ok {
		return "", nil
	}
	return stateHeader + "\n" + renderReport(rep), nil
}

// ---------- 后台刷新 ----------

func (p *Plugin) loop(ctx context.Context, client *http.Client, location string, refresh time.Duration) {
	defer p.wg.Done()

	p.refreshOnce(ctx, client, location)

	t := time.NewTicker(refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.refreshOnce(ctx, client, location)
		}
	}
}

// refreshOnce 取一次天气。失败时保留上一份观测，由过期时限决定它还能不能用。
func (p *Plugin) refreshOnce(ctx context.Context, client *http.Client, location string) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	rep, err := p.observe(ctx, client, location)
	if err != nil {
		if ctx.Err() == nil {
			p.noteError(err)
		}
		return
	}
	p.dataMu.Lock()
	p.cur, p.curOK, p.lastErr = rep, true, ""
	p.dataMu.Unlock()
}

// observe 取一次观测，地名解析的结果按城市缓存——它几乎不变，不必每次重解析。
func (p *Plugin) observe(ctx context.Context, client *http.Client, location string) (Report, error) {
	p.dataMu.RLock()
	place, ok := p.place, p.placeOK && p.placeKey == location
	p.dataMu.RUnlock()

	if !ok {
		resolved, err := geocode(ctx, client, location)
		if err != nil {
			return Report{}, err
		}
		p.dataMu.Lock()
		p.placeKey, p.place, p.placeOK = location, resolved, true
		p.dataMu.Unlock()
		place = resolved
	}
	return fetchCurrent(ctx, client, place)
}

// noteError 记下失败原因供状态行展示。同一个原因只写一次日志：
// 断网期间每半小时刷屏一条同样的错误没有意义。
func (p *Plugin) noteError(err error) {
	msg := err.Error()
	p.dataMu.Lock()
	repeated := p.lastErr == msg
	p.lastErr = msg
	p.dataMu.Unlock()
	if !repeated {
		log.Printf("weather: %s", msg)
	}
}

// fresh 返回仍在有效期内的观测。
func (p *Plugin) fresh(now time.Time) (Report, bool) {
	stale := p.snapshot().stale
	p.dataMu.RLock()
	defer p.dataMu.RUnlock()
	if !p.curOK {
		return Report{}, false
	}
	if stale > 0 && now.Sub(p.cur.Fetched) > stale {
		return Report{}, false
	}
	return p.cur, true
}

// ---------- 注入渲染 ----------

// renderReport 渲染一行当前天气。体量固定，不需要预算与降级。
//
// 不注入「几分钟前采集」：当前时间由核心的环境块每轮给出，再加一个逐轮变化的
// 采集间隔只是噪声；数据够不够新已经由过期时限判过了。
func renderReport(r Report) string {
	var b strings.Builder
	if r.Place != "" {
		b.WriteString(r.Place)
		b.WriteString("：")
	}
	fmt.Fprintf(&b, "%s，%.0f℃", r.Condition, r.TempC)
	if diff := r.FeelsC - r.TempC; diff >= 1 || diff <= -1 {
		fmt.Fprintf(&b, "（体感 %.0f℃）", r.FeelsC)
	}
	if r.Humidity > 0 {
		fmt.Fprintf(&b, "，湿度 %d%%", r.Humidity)
	}
	fmt.Fprintf(&b, "，风速 %.0f km/h。", r.WindKmh)
	return b.String()
}
