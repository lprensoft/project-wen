// Package wechatbot 通过微信官方 ClawBot 插件通道（iLink Bot API）提供个人微信私聊远程会话。
//
// iLink 是腾讯 2026 年正式开放的个人微信 Bot 协议：HTTP/JSON，扫码绑定后以
// bot_token 鉴权，getupdates 长轮询收消息、sendmessage 回消息（必须回带入站
// 消息的 context_token）。凭证不是可粘贴的 Key，而是动态二维码扫码确认后由
// 服务端下发，因此绑定流程通过「插件操作入口」（plugin.Actionable）做在设置页。
//
// 每个微信用户（xxx@im.wechat）映射到一个当前会话，远程会话就是普通会话，
// Web UI 刷新即可看到。命令集与 qq_bot 一致：/new /status /compact /help，
// 以及危险操作确认的 /apply /deny。仅响应白名单内的用户；扫码绑定人自动放行，
// 白名单为空时其他人一律拒绝（陌生用户记录到日志，方便抄进白名单）。仅私聊：
// 带 group_id 的消息忽略（群聊能力官方未充分验证）。
package wechatbot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"wen/internal/plugin"
	"wen/internal/session"
)

const (
	defAPIBase        = "https://ilinkai.weixin.qq.com"
	defConfirmTimeout = 300 // 秒

	turnTimeout = 10 * time.Minute
	queueSize   = 8 // 每用户待处理消息队列长度，超出直接回复稍候

	// chunkLimit 是单条消息的 rune 上限。官方未公开限制，保守取值，实测后可调。
	chunkLimit = 2000

	// expiredPause 是服务端返回会话过期（errcode -14）后的暂停时长，与官方参考实现一致。
	expiredPause = time.Hour
)

// Plugin 是微信 ClawBot 插件。有状态：Init 可重入（先停旧循环），字段由 mu 保护。
type Plugin struct {
	mu sync.Mutex

	// 配置
	apiBase        string // 扫码入口用的公共基址；绑定后改用服务端下发的专属 baseurl
	whitelist      map[string]bool
	confirmTimeout time.Duration

	// 凭证（扫码绑定后持久化到 StateDir/credentials.json）
	creds    credentials
	stateDir string

	// 能力
	runTurn    plugin.RunTurnFunc
	newSession plugin.NewSessionFunc
	compact    plugin.CompactFunc
	status     plugin.StatusFunc
	sessions   *session.Store // 只读：校验绑定的会话是否仍存在

	// 运行组件
	dedup   *deduper
	binding *sessionBinding // 微信用户 ID → 会话 ID
	broker  *confirmBroker
	workers map[string]chan inbound
	typing  map[string]string // 微信用户 ID → typing_ticket 缓存

	// 长轮询循环有独立的子取消：绑定成功后可只重启轮询而不动整个插件
	ctx        context.Context
	cancel     context.CancelFunc
	pollCancel context.CancelFunc
	wg         sync.WaitGroup

	// 绑定流程状态（插件操作入口）
	bind bindState

	// 测试注入：过期暂停时长（默认 expiredPause）
	pauseOnExpired time.Duration
}

// inbound 是一条收到的私聊消息。
type inbound struct {
	userID       string // from_user_id（xxx@im.wechat）
	contextToken string // 回复时必须回带
	text         string
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "wechat_bot" }

func (p *Plugin) Description() string {
	return "接入微信官方 ClawBot 插件实现个人微信私聊远程会话（设置页扫码绑定），支持 /new /status /compact /help 命令与危险操作远程确认"
}

func (p *Plugin) SystemPrompt() string { return "" }

func (p *Plugin) Tools() []plugin.Tool { return nil }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "whitelist", Label: "用户白名单", Type: plugin.FieldText, Default: "",
			Description: "每行一个微信用户 ID（形如 xxx@im.wechat）。扫码绑定人始终放行；名单外用户一律拒绝，其 ID 会记录到运行日志方便抄入",
		},
		{
			Key: "api_base", Label: "API 地址", Type: plugin.FieldString, Default: defAPIBase,
			Description: "扫码绑定的入口地址，一般不需要改；绑定后的请求走服务端下发的专属地址",
		},
		{
			Key: "confirm_timeout_sec", Label: "确认等待超时（秒）", Type: plugin.FieldInt,
			Default: defConfirmTimeout, Min: plugin.IntPtr(10), Max: plugin.IntPtr(3600),
			Description: "危险操作发出确认请求后等待 /apply 或 /deny 的时长，超时按拒绝处理",
		},
	}
}

// Init 应用配置并（重）启动。可重入：先停旧循环与所有 worker。
// 未绑定时 Init 也成功——插件进入待绑定状态，等设置页扫码；绑定过则直接起轮询。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	if ictx.StateDir == "" {
		return fmt.Errorf("没有可用的持久化目录")
	}
	if ictx.RunTurn == nil || ictx.NewSession == nil {
		return fmt.Errorf("当前环境不支持插件发起对话轮次")
	}
	sessions, err := session.NewStore(ictx.SessionDir)
	if err != nil {
		return fmt.Errorf("打开会话目录失败: %w", err)
	}
	binding, err := loadBinding(ictx.StateDir)
	if err != nil {
		return fmt.Errorf("加载会话映射失败: %w", err)
	}
	creds, err := loadCredentials(ictx.StateDir)
	if err != nil {
		return fmt.Errorf("加载绑定凭证失败: %w", err)
	}

	whitelist := map[string]bool{}
	for _, line := range strings.Split(plugin.CfgString(cfg, "whitelist", ""), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			whitelist[s] = true
		}
	}

	p.Stop()

	p.mu.Lock()
	p.apiBase = strings.TrimRight(plugin.CfgString(cfg, "api_base", defAPIBase), "/")
	p.whitelist = whitelist
	p.confirmTimeout = time.Duration(plugin.CfgInt(cfg, "confirm_timeout_sec", defConfirmTimeout)) * time.Second
	p.creds = creds
	p.stateDir = ictx.StateDir
	p.runTurn = ictx.RunTurn
	p.newSession = ictx.NewSession
	p.compact = ictx.Compact
	p.status = ictx.Status
	p.sessions = sessions
	p.dedup = newDeduper()
	p.binding = binding
	p.broker = newConfirmBroker()
	p.workers = map[string]chan inbound{}
	p.typing = map[string]string{}
	if p.pauseOnExpired == 0 {
		p.pauseOnExpired = expiredPause
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	bound := creds.valid()
	p.mu.Unlock()

	if bound {
		p.startPolling()
	} else {
		log.Printf("wechat_bot: 尚未绑定微信，请到设置页的插件卡片上点击「扫码绑定微信」")
	}
	return nil
}

// startPolling （重）启动长轮询循环：先停掉旧循环，再以当前凭证起新的。
func (p *Plugin) startPolling() {
	p.mu.Lock()
	if p.pollCancel != nil {
		p.pollCancel()
		p.pollCancel = nil
	}
	if p.ctx == nil {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.pollCancel = cancel
	p.wg.Add(1)
	p.mu.Unlock()
	go p.pollLoop(ctx)
}

// Stop 停掉轮询循环、绑定流程与全部 worker。
func (p *Plugin) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.pollCancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.wg.Wait()
	}
}
