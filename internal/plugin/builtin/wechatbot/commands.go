package wechatbot

import (
	"context"
	"errors"
	"log"
	"strings"

	"wen/internal/plugin"
	"wen/internal/statustext"
)

const helpText = `支持的命令：
/new     新建一个会话（之后的消息进入新会话）
/status  查看当前会话状态
/compact 压缩当前会话历史
/help    显示本列表
/apply   允许等待中的危险操作
/deny    拒绝等待中的危险操作
其他内容将作为消息发给助手。`

// handleInbound 是入站消息的分发层：去重、白名单，然后把 /apply /deny 直接投递给
// 确认代理（不能排队：worker 正阻塞在等确认上，排队会自锁），其余消息交给该用户的
// 串行 worker。unsupported 表示消息里有暂不支持的媒体类型。
func (p *Plugin) handleInbound(ctx context.Context, msg inbound, dedupKey string, unsupported bool) {
	p.mu.Lock()
	dedup, broker := p.dedup, p.broker
	p.mu.Unlock()

	if dedup.isDuplicate(dedupKey) {
		return
	}
	if !p.allowed(msg.userID) {
		log.Printf("wechat_bot: 拒绝了白名单之外的用户 %s（如需放行请加入白名单）", msg.userID)
		return
	}
	// 记住该用户最近一次入站的 context_token：后台轮次（心跳等）的结果要靠它推送
	p.mu.Lock()
	tokens := p.tokens
	p.mu.Unlock()
	tokens.remember(msg.userID, msg.contextToken)
	if msg.text == "" {
		if unsupported {
			p.send(ctx, msg.userID, "暂不支持该消息类型，请发文字或语音。", msg.contextToken)
		}
		return
	}

	switch strings.TrimSpace(msg.text) {
	case "/apply", "/deny":
		approved := strings.TrimSpace(msg.text) == "/apply"
		ch, ok := broker.take(msg.userID)
		if !ok {
			p.send(ctx, msg.userID, "当前没有等待确认的操作。", msg.contextToken)
			return
		}
		// 先回执、再投答复：反过来的话，被解开的那一轮可能抢在回执前面把结果发出去
		if approved {
			p.send(ctx, msg.userID, "✅ 已允许，继续执行。", msg.contextToken)
		} else {
			p.send(ctx, msg.userID, "🚫 已拒绝该操作。", msg.contextToken)
		}
		ch <- approved
		return
	}

	p.enqueue(ctx, msg)
}

// allowed 判定用户是否放行：扫码绑定人始终放行，其余看配置白名单。
func (p *Plugin) allowed(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return userID != "" && (userID == p.creds.BinderUserID || p.whitelist[userID])
}

// enqueue 把消息放进该用户的串行队列，worker 不存在时惰性启动。队列满说明上一条
// 还在处理，直接告知稍候而不是无限堆积。
func (p *Plugin) enqueue(ctx context.Context, msg inbound) {
	p.mu.Lock()
	q, ok := p.workers[msg.userID]
	if !ok {
		q = make(chan inbound, queueSize)
		p.workers[msg.userID] = q
		p.wg.Add(1)
		go p.worker(p.ctx, q)
	}
	p.mu.Unlock()

	select {
	case q <- msg:
	default:
		p.send(ctx, msg.userID, "上一条消息还在处理中，请稍候再发。", msg.contextToken)
	}
}

// worker 串行处理单个用户的消息：命令直接答复，普通消息跑一轮对话并把结果发回。
func (p *Plugin) worker(ctx context.Context, q chan inbound) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-q:
			p.process(ctx, msg)
		}
	}
}

func (p *Plugin) process(ctx context.Context, msg inbound) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("wechat_bot: 处理消息时 panic（已隔离）: %v", r)
		}
	}()

	text := strings.TrimSpace(msg.text)
	switch {
	case text == "/help":
		p.send(ctx, msg.userID, helpText, msg.contextToken)
	case text == "/new":
		p.cmdNew(ctx, msg)
	case text == "/status":
		p.cmdStatus(ctx, msg)
	case text == "/compact":
		p.cmdCompact(ctx, msg)
	case strings.HasPrefix(text, "/"):
		p.send(ctx, msg.userID, "未知命令："+text+"\n"+helpText, msg.contextToken)
	default:
		p.chat(ctx, msg)
	}
}

// chat 把消息作为一轮对话执行，回复助手的最终文本。期间向微信发「正在输入」状态。
func (p *Plugin) chat(ctx context.Context, msg inbound) {
	p.mu.Lock()
	runTurn := p.runTurn
	showThinking, showTools := p.showThinking, p.showTools
	p.mu.Unlock()

	sid, err := p.sessionFor(msg.userID)
	if err != nil {
		p.send(ctx, msg.userID, "会话初始化失败："+err.Error(), msg.contextToken)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	// 微信对面是真人：标记交互（更新会话活跃时间），并接入微信侧的确认通道
	tctx = plugin.WithInteractive(tctx)
	tctx = plugin.WithConfirmer(tctx, p.confirmerFor(msg.userID, msg.contextToken))
	// 按配置转发过程通知：思考链、工具调用（仅名字）。同步发送保证与最终回复的顺序
	if showThinking || showTools {
		tctx = plugin.WithTurnNotes(tctx, func(n plugin.TurnNote) {
			switch {
			case n.Kind == plugin.NoteThinking && showThinking:
				p.send(ctx, msg.userID, thinkingLine(n.Text), msg.contextToken)
			case n.Kind == plugin.NoteToolCalls && showTools:
				p.send(ctx, msg.userID, toolsLine(n.Tools), msg.contextToken)
			}
		})
	}

	p.setTyping(ctx, msg.userID, msg.contextToken, true)
	final, err := runTurn(tctx, sid, msg.text)
	p.setTyping(ctx, msg.userID, msg.contextToken, false)
	if err != nil {
		if errors.Is(err, plugin.ErrSessionBusy) {
			p.send(ctx, msg.userID, "当前会话正忙（可能有其他对话或后台任务在进行），请稍后再试。", msg.contextToken)
			return
		}
		p.send(ctx, msg.userID, "处理失败："+err.Error(), msg.contextToken)
		return
	}
	if strings.TrimSpace(final) == "" {
		final = "（本轮没有文本回复）"
	}
	p.send(ctx, msg.userID, final, msg.contextToken)
}

func (p *Plugin) cmdNew(ctx context.Context, msg inbound) {
	p.mu.Lock()
	binding, newSession := p.binding, p.newSession
	p.mu.Unlock()

	sid, err := newSession()
	if err != nil {
		p.send(ctx, msg.userID, "新建会话失败："+err.Error(), msg.contextToken)
		return
	}
	if err := binding.set(msg.userID, sid); err != nil {
		p.send(ctx, msg.userID, "保存会话映射失败："+err.Error(), msg.contextToken)
		return
	}
	p.send(ctx, msg.userID, "✨ 已新建会话（"+sid+"），接下来的消息将进入新会话。", msg.contextToken)
}

// cmdStatus 的输出与 Web UI 的 /status 命令保持一致（同源数据，同样的措辞与格式）。
func (p *Plugin) cmdStatus(ctx context.Context, msg inbound) {
	p.mu.Lock()
	status := p.status
	p.mu.Unlock()

	if status == nil {
		p.send(ctx, msg.userID, "当前环境不支持状态查询。", msg.contextToken)
		return
	}
	sid, err := p.sessionFor(msg.userID)
	if err != nil {
		p.send(ctx, msg.userID, "会话初始化失败："+err.Error(), msg.contextToken)
		return
	}
	info, err := status(sid)
	if err != nil {
		p.send(ctx, msg.userID, "获取状态失败："+err.Error(), msg.contextToken)
		return
	}

	// 措辞三处输出共用一份实现，见 internal/statustext
	p.send(ctx, msg.userID, statustext.Render(info, sid), msg.contextToken)
}

func (p *Plugin) cmdCompact(ctx context.Context, msg inbound) {
	p.mu.Lock()
	compact := p.compact
	p.mu.Unlock()

	if compact == nil {
		p.send(ctx, msg.userID, "当前环境不支持压缩。", msg.contextToken)
		return
	}
	sid, err := p.sessionFor(msg.userID)
	if err != nil {
		p.send(ctx, msg.userID, "会话初始化失败："+err.Error(), msg.contextToken)
		return
	}
	p.send(ctx, msg.userID, "开始压缩会话历史，请稍候…", msg.contextToken)
	tctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	if err := compact(tctx, sid); err != nil {
		p.send(ctx, msg.userID, "压缩失败："+err.Error(), msg.contextToken)
		return
	}
	p.send(ctx, msg.userID, "📦 会话历史已压缩完成。", msg.contextToken)
}

// thinkingLine / toolsLine 的措辞与 Web UI 的过程展示保持一致（🧠 思考过程 / 🔧 调用工具）。
// 工具名带行内代码标记：markdown 模式由微信渲染成代码样式，纯文本模式转换为「工具名」。
func thinkingLine(text string) string {
	return "🧠 思考过程\n" + strings.TrimSpace(text)
}

func toolsLine(tools []string) string {
	quoted := make([]string, 0, len(tools))
	for _, t := range tools {
		quoted = append(quoted, "`"+t+"`")
	}
	return "🔧 调用工具 " + strings.Join(quoted, "、")
}
