package qqbot

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
// 确认代理（不能排队，见 confirmBroker 注释），其余消息交给该用户的串行 worker。
func (p *Plugin) handleInbound(ctx context.Context, msg inbound) {
	p.mu.Lock()
	dedup, whitelist, broker := p.dedup, p.whitelist, p.broker
	p.mu.Unlock()

	if dedup.isDuplicate(msg.msgID) {
		return
	}
	if !whitelist[msg.openid] {
		log.Printf("qq_bot: 拒绝了白名单之外的用户，openid=%s（如需放行请加入白名单）", msg.openid)
		return
	}

	switch strings.TrimSpace(msg.content) {
	case "/apply", "/deny":
		approved := strings.TrimSpace(msg.content) == "/apply"
		ch, ok := broker.take(msg.openid)
		if !ok {
			p.send(ctx, msg.openid, "当前没有等待确认的操作。", msg.msgID)
			return
		}
		// 先回执、再投答复：反过来的话，被解开的那一轮可能抢在回执前面把结果发出去
		if approved {
			p.send(ctx, msg.openid, "✅ 已允许，继续执行。", msg.msgID)
		} else {
			p.send(ctx, msg.openid, "🚫 已拒绝该操作。", msg.msgID)
		}
		ch <- approved
		return
	}

	p.enqueue(ctx, msg)
}

// enqueue 把消息放进该用户的串行队列，worker 不存在时惰性启动。队列满说明上一条
// 还在处理，直接告知稍候而不是无限堆积。
func (p *Plugin) enqueue(ctx context.Context, msg inbound) {
	p.mu.Lock()
	q, ok := p.workers[msg.openid]
	if !ok {
		q = make(chan inbound, queueSize)
		p.workers[msg.openid] = q
		p.wg.Add(1)
		go p.worker(p.ctx, q)
	}
	p.mu.Unlock()

	select {
	case q <- msg:
	default:
		p.send(ctx, msg.openid, "上一条消息还在处理中，请稍候再发。", msg.msgID)
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
			log.Printf("qq_bot: 处理消息时 panic（已隔离）: %v", r)
		}
	}()

	text := strings.TrimSpace(msg.content)
	switch {
	case text == "/help":
		p.send(ctx, msg.openid, helpText, msg.msgID)
	case text == "/new":
		p.cmdNew(ctx, msg)
	case text == "/status":
		p.cmdStatus(ctx, msg)
	case text == "/compact":
		p.cmdCompact(ctx, msg)
	case strings.HasPrefix(text, "/"):
		p.send(ctx, msg.openid, "未知命令："+text+"\n"+helpText, msg.msgID)
	default:
		p.chat(ctx, msg)
	}
}

// chat 把消息作为一轮对话执行，回复助手的最终文本。
func (p *Plugin) chat(ctx context.Context, msg inbound) {
	p.mu.Lock()
	runTurn := p.runTurn
	showThinking, showTools := p.showThinking, p.showTools
	p.mu.Unlock()

	sid, err := p.sessionFor(msg.openid)
	if err != nil {
		p.send(ctx, msg.openid, "会话初始化失败："+err.Error(), msg.msgID)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	// QQ 对面是真人：标记交互（更新会话活跃时间），并接入 QQ 侧的确认通道
	tctx = plugin.WithInteractive(tctx)
	tctx = plugin.WithConfirmer(tctx, p.confirmerFor(msg.openid, msg.msgID))
	// 按配置转发过程通知：思考链、工具调用（仅名字）。同步发送保证与最终回复的顺序
	if showThinking || showTools {
		tctx = plugin.WithTurnNotes(tctx, func(n plugin.TurnNote) {
			switch {
			case n.Kind == plugin.NoteThinking && showThinking:
				p.send(ctx, msg.openid, thinkingLine(n.Text), msg.msgID)
			case n.Kind == plugin.NoteToolCalls && showTools:
				p.send(ctx, msg.openid, toolsLine(n.Tools), msg.msgID)
			}
		})
	}

	final, err := runTurn(tctx, sid, msg.content)
	if err != nil {
		if errors.Is(err, plugin.ErrSessionBusy) {
			p.send(ctx, msg.openid, "当前会话正忙（可能有其他对话或后台任务在进行），请稍后再试。", msg.msgID)
			return
		}
		p.send(ctx, msg.openid, "处理失败："+err.Error(), msg.msgID)
		return
	}
	if strings.TrimSpace(final) == "" {
		final = "（本轮没有文本回复）"
	}
	p.send(ctx, msg.openid, final, msg.msgID)
}

func (p *Plugin) cmdNew(ctx context.Context, msg inbound) {
	p.mu.Lock()
	binding, newSession := p.binding, p.newSession
	p.mu.Unlock()

	sid, err := newSession()
	if err != nil {
		p.send(ctx, msg.openid, "新建会话失败："+err.Error(), msg.msgID)
		return
	}
	if err := binding.set(msg.openid, sid); err != nil {
		p.send(ctx, msg.openid, "保存会话映射失败："+err.Error(), msg.msgID)
		return
	}
	p.send(ctx, msg.openid, "✨ 已新建会话（"+sid+"），接下来的消息将进入新会话。", msg.msgID)
}

// cmdStatus 的输出与 Web UI 的 /status 命令保持一致（同源数据，同样的措辞与格式）。
func (p *Plugin) cmdStatus(ctx context.Context, msg inbound) {
	p.mu.Lock()
	status := p.status
	p.mu.Unlock()

	if status == nil {
		p.send(ctx, msg.openid, "当前环境不支持状态查询。", msg.msgID)
		return
	}
	sid, err := p.sessionFor(msg.openid)
	if err != nil {
		p.send(ctx, msg.openid, "会话初始化失败："+err.Error(), msg.msgID)
		return
	}
	info, err := status(sid)
	if err != nil {
		p.send(ctx, msg.openid, "获取状态失败："+err.Error(), msg.msgID)
		return
	}

	// 措辞三处输出共用一份实现，见 internal/statustext
	p.send(ctx, msg.openid, statustext.Render(info, sid), msg.msgID)
}

// thinkingLine / toolsLine 的措辞与 Web UI 的过程展示保持一致（🧠 思考过程 / 🔧 调用工具）。
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

func (p *Plugin) cmdCompact(ctx context.Context, msg inbound) {
	p.mu.Lock()
	compact := p.compact
	p.mu.Unlock()

	if compact == nil {
		p.send(ctx, msg.openid, "当前环境不支持压缩。", msg.msgID)
		return
	}
	sid, err := p.sessionFor(msg.openid)
	if err != nil {
		p.send(ctx, msg.openid, "会话初始化失败："+err.Error(), msg.msgID)
		return
	}
	p.send(ctx, msg.openid, "开始压缩会话历史，请稍候…", msg.msgID)
	tctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	if err := compact(tctx, sid); err != nil {
		p.send(ctx, msg.openid, "压缩失败："+err.Error(), msg.msgID)
		return
	}
	p.send(ctx, msg.openid, "📦 会话历史已压缩完成。", msg.msgID)
}
