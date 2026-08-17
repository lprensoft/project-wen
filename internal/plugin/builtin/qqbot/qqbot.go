// Package qqbot 通过 QQ 官方开放平台（API v2 + WebSocket 事件网关）提供 C2C 私聊远程会话。
//
// 每个 QQ 用户（openid）映射到一个当前会话，远程会话就是普通会话（同一个会话存储），
// Web UI 刷新即可看到。支持命令：/new /status /compact /help，以及危险操作确认的
// /apply /deny。协议层参考官方 qqbot-nodejs SDK：被动回复带 msg_id（同一条消息
// 60 分钟内最多回 4 条，超限降级为主动消息）、入站消息按 id 去重。
//
// 仅响应白名单内的 openid；白名单为空时拒绝所有人（陌生 openid 记录到日志，
// 方便抄进白名单）。
package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"wen/internal/plugin"
	"wen/internal/session"
)

const (
	defAPIBase        = "https://api.sgroup.qq.com"
	defTokenURL       = "https://bots.qq.com/app/getAppAccessToken"
	defConfirmTimeout = 300 // 秒

	turnTimeout = 10 * time.Minute
	queueSize   = 8 // 每用户待处理消息队列长度，超出直接回复稍候
)

// Plugin 是 QQ 机器人插件。有状态：Init 可重入（先停旧网关连接），字段由 mu 保护。
type Plugin struct {
	mu sync.Mutex

	// 配置
	appID          string
	appSecret      string
	apiBase        string
	whitelist      map[string]bool
	confirmTimeout time.Duration

	// 能力
	runTurn    plugin.RunTurnFunc
	newSession plugin.NewSessionFunc
	compact    plugin.CompactFunc
	status     plugin.StatusFunc
	sessions   *session.Store // 只读：校验绑定的会话是否仍存在

	// 运行组件
	tokens  *tokenSource
	limiter *replyLimiter
	dedup   *deduper
	binding *sessionBinding // openid → 会话 ID
	broker  *confirmBroker
	workers map[string]chan inbound

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// tokenURLOverride 供测试指向本地假鉴权服务，生产环境留空。
	tokenURLOverride string
}

// inbound 是一条收到的 C2C 消息。
type inbound struct {
	openid  string
	msgID   string
	content string
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string { return "qq_bot" }

func (p *Plugin) Description() string {
	return "接入 QQ 官方机器人实现私聊远程会话，支持 /new /status /compact /help 命令与危险操作远程确认"
}

func (p *Plugin) SystemPrompt() string { return "" }

func (p *Plugin) Tools() []plugin.Tool { return nil }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "app_id", Label: "AppID", Type: plugin.FieldString, Default: "",
			Description: "QQ 开放平台机器人的 AppID（q.qq.com 开发设置页）",
		},
		{
			Key: "app_secret", Label: "AppSecret", Type: plugin.FieldString, Default: "",
			Description: "机器人密钥，仅存于本机的插件状态文件（0600 权限），不入库",
		},
		{
			Key: "whitelist", Label: "openid 白名单", Type: plugin.FieldText, Default: "",
			Description: "每行一个 user_openid，只响应名单内用户；留空拒绝所有人。陌生用户首次发消息时其 openid 会记录到运行日志",
		},
		{
			Key: "api_base", Label: "API 地址", Type: plugin.FieldString, Default: defAPIBase,
			Description: "一般不需要改。注意官方沙箱环境不支持单聊，私聊需正式发布后可用",
		},
		{
			Key: "confirm_timeout_sec", Label: "确认等待超时（秒）", Type: plugin.FieldInt,
			Default: defConfirmTimeout, Min: plugin.IntPtr(10), Max: plugin.IntPtr(3600),
			Description: "危险操作发出确认请求后等待 /apply 或 /deny 的时长，超时按拒绝处理",
		},
	}
}

// Init 应用配置并（重）启动网关连接。可重入：先停旧连接与所有 worker。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	appID := strings.TrimSpace(plugin.CfgString(cfg, "app_id", ""))
	appSecret := strings.TrimSpace(plugin.CfgString(cfg, "app_secret", ""))
	if appID == "" || appSecret == "" {
		return fmt.Errorf("请先在配置中填写 AppID 与 AppSecret")
	}
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

	whitelist := map[string]bool{}
	for _, line := range strings.Split(plugin.CfgString(cfg, "whitelist", ""), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			whitelist[s] = true
		}
	}

	p.Stop()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.appID, p.appSecret = appID, appSecret
	p.apiBase = strings.TrimRight(plugin.CfgString(cfg, "api_base", defAPIBase), "/")
	p.whitelist = whitelist
	p.confirmTimeout = time.Duration(plugin.CfgInt(cfg, "confirm_timeout_sec", defConfirmTimeout)) * time.Second
	p.runTurn = ictx.RunTurn
	p.newSession = ictx.NewSession
	p.compact = ictx.Compact
	p.status = ictx.Status
	p.sessions = sessions
	p.tokens = newTokenSource(appID, appSecret)
	if p.tokenURLOverride != "" {
		p.tokens.tokenURL = p.tokenURLOverride
	}
	p.limiter = newReplyLimiter()
	p.dedup = newDeduper()
	p.binding = binding
	p.broker = newConfirmBroker()
	p.workers = map[string]chan inbound{}

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.gatewayLoop(p.ctx)
	return nil
}

// Stop 断开网关连接并停掉全部 worker。
func (p *Plugin) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		p.wg.Wait()
	}
}
