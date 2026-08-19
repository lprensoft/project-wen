package imbot

import (
	"context"
	"errors"
	"log"
	"strings"

	"wen/internal/plugin"
	"wen/internal/statustext"
)

// HelpText 是各通道共用的命令说明。命令集统一，换一条通道不用重新学。
const HelpText = `支持的命令：
/new     新建一个会话（之后的消息进入新会话）
/status  查看当前会话状态
/compact 压缩当前会话历史
/help    显示本列表
/apply   允许等待中的危险操作
/deny    拒绝等待中的危险操作
其他内容将作为消息发给助手。`

// process 处理一条已排到队首的消息：命令直接答复，其余作为一轮对话执行。
func (c *Core) process(ctx context.Context, msg Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s: 处理消息时 panic（已隔离）: %v", c.cfg.PluginName, r)
		}
	}()

	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "/help":
		c.send(ctx, msg, HelpText)
	case text == "/new":
		c.cmdNew(ctx, msg)
	case text == "/status":
		c.cmdStatus(ctx, msg)
	case text == "/compact":
		c.cmdCompact(ctx, msg)
	case strings.HasPrefix(text, "/"):
		c.send(ctx, msg, "未知命令："+text+"\n"+HelpText)
	default:
		c.chat(ctx, msg)
	}
}

// chat 把消息作为一轮对话执行，回复助手的最终文本。
func (c *Core) chat(ctx context.Context, msg Message) {
	sid, err := c.SessionFor(msg.UserID)
	if err != nil {
		c.send(ctx, msg, "会话初始化失败："+err.Error())
		return
	}

	tctx, cancel := context.WithTimeout(ctx, c.cfg.TurnTimeout)
	defer cancel()
	// 对面是真人：标记交互（更新会话活跃时间），并接入本通道的确认通道
	tctx = plugin.WithInteractive(tctx)
	tctx = plugin.WithConfirmer(tctx, c.confirmerFor(msg))
	// 按配置转发过程通知：思考链、工具调用（仅名字）。同步发送保证与最终回复的顺序
	if c.cfg.ShowThinking || c.cfg.ShowTools {
		tctx = plugin.WithTurnNotes(tctx, func(n plugin.TurnNote) {
			switch {
			case n.Kind == plugin.NoteThinking && c.cfg.ShowThinking:
				c.send(ctx, msg, ThinkingLine(n.Text))
			case n.Kind == plugin.NoteToolCalls && c.cfg.ShowTools:
				c.send(ctx, msg, ToolsLine(n.Tools))
			}
		})
	}

	c.typing(ctx, msg, true)
	final, err := c.cfg.RunTurn(tctx, sid, msg.Text)
	c.typing(ctx, msg, false)
	if err != nil {
		if errors.Is(err, plugin.ErrSessionBusy) {
			c.send(ctx, msg, "当前会话正忙（可能有其他对话或后台任务在进行），请稍后再试。")
			return
		}
		c.send(ctx, msg, "处理失败："+err.Error())
		return
	}
	if strings.TrimSpace(final) == "" {
		final = "（本轮没有文本回复）"
	}
	c.send(ctx, msg, final)
}

// typing 发「正在输入」状态，通道没实现就什么也不做。
func (c *Core) typing(ctx context.Context, msg Message, active bool) {
	if c.cfg.Typing != nil {
		c.cfg.Typing(ctx, msg, active)
	}
}

func (c *Core) cmdNew(ctx context.Context, msg Message) {
	sid, err := c.cfg.NewSession()
	if err != nil {
		c.send(ctx, msg, "新建会话失败："+err.Error())
		return
	}
	if err := c.binding.Set(msg.UserID, sid); err != nil {
		c.send(ctx, msg, "保存会话映射失败："+err.Error())
		return
	}
	c.send(ctx, msg, "✨ 已新建会话（"+sid+"），接下来的消息将进入新会话。")
}

// cmdStatus 的输出与 Web UI 的 /status 命令保持一致（同源数据，同样的措辞与格式）。
func (c *Core) cmdStatus(ctx context.Context, msg Message) {
	if c.cfg.Status == nil {
		c.send(ctx, msg, "当前环境不支持状态查询。")
		return
	}
	sid, err := c.SessionFor(msg.UserID)
	if err != nil {
		c.send(ctx, msg, "会话初始化失败："+err.Error())
		return
	}
	info, err := c.cfg.Status(sid)
	if err != nil {
		c.send(ctx, msg, "获取状态失败："+err.Error())
		return
	}
	// 措辞三处输出共用一份实现，见 internal/statustext
	c.send(ctx, msg, statustext.Render(info, sid))
}

func (c *Core) cmdCompact(ctx context.Context, msg Message) {
	if c.cfg.Compact == nil {
		c.send(ctx, msg, "当前环境不支持压缩。")
		return
	}
	sid, err := c.SessionFor(msg.UserID)
	if err != nil {
		c.send(ctx, msg, "会话初始化失败："+err.Error())
		return
	}
	c.send(ctx, msg, "开始压缩会话历史，请稍候…")
	tctx, cancel := context.WithTimeout(ctx, c.cfg.TurnTimeout)
	defer cancel()
	if err := c.cfg.Compact(tctx, sid); err != nil {
		c.send(ctx, msg, "压缩失败："+err.Error())
		return
	}
	c.send(ctx, msg, "📦 会话历史已压缩完成。")
}

// ThinkingLine / ToolsLine 的措辞与 Web UI 的过程展示保持一致（🧠 思考过程 / 🔧 调用工具）。
// 工具名带行内代码标记：markdown 模式由客户端渲染成代码样式，纯文本模式转换为「工具名」。
func ThinkingLine(text string) string {
	return "🧠 思考过程\n" + strings.TrimSpace(text)
}

func ToolsLine(tools []string) string {
	quoted := make([]string, 0, len(tools))
	for _, t := range tools {
		quoted = append(quoted, "`"+t+"`")
	}
	return "🔧 调用工具 " + strings.Join(quoted, "、")
}
