// Package dualpersona 提供表里两套人格的系统插件。
//
// 表人格的设定由 roleplay 提供，本插件只管三件事：里人格自己的设定、两个方向的
// 切换触发词，以及每轮的可见域裁决——里人格的消息带标签落盘，表人格读不到它们，
// 里人格读得到全部。
//
// 它不引用 roleplay 的任何类型。里人格激活时 roleplay 注入的表人格设定仍留在
// 上下文里，这是对的：里人格「看得到全部」，本就该知道表面那层长什么样；而注册顺序
// 让本插件的片段排在其后，天然形成追加与覆盖的语义。Requires 只是启用门禁。
//
// 需要清楚的一点：可见域是上下文隔离，不是沙箱。提供通用文件与命令通道的插件
// （read_file / exec_command）能直接读到会话文件、记忆目录与本插件的状态文件，
// 绕过这里的一切过滤，因此在 Conflicts 里声明出来告警。
package dualpersona

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"wen/internal/plugin"
)

// 两个人格对应的可见域标签。会被拼进持久化目录，取值与插件名同规。
const (
	personaOuter = "outer"
	personaInner = "inner"
)

// 触发词的匹配方式。
const (
	matchContains = "contains" // 消息里出现即命中
	matchEquals   = "equals"   // 整条消息就是这个词才命中
	defaultMatch  = matchContains
)

func valid(p string) bool { return p == personaOuter || p == personaInner }

// Plugin 是 dual_persona 系统插件。
type Plugin struct {
	mu sync.RWMutex

	innerPersona string
	toInner      []string
	toOuter      []string
	matchMode    string
	store        *store
}

func New() *Plugin { return &Plugin{matchMode: defaultMatch} }

func (p *Plugin) Name() string { return "dual_persona" }

func (p *Plugin) Description() string {
	return "表里两套人格：里人格的对话与记忆对表人格不可见，由触发词在两者之间切换"
}

// Requires 依赖 roleplay：表人格的设定由它提供，没有它这个插件只剩半边。
func (p *Plugin) Requires() []string { return []string{"roleplay"} }

// Conflicts 声明的插件提供通用的文件与命令通道，能绕过可见域直接读到会话文件、
// 记忆目录与本插件的状态文件。冲突只告警不阻止：用户可能确实要一个能干活的角色，
// 代价由他自己权衡。
func (p *Plugin) Conflicts() []string { return []string{"exec_command", "read_file"} }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "inner_persona", Label: "里人格设定", Type: plugin.FieldText,
			Description: "只在里人格激活时注入，优先于表人格的角色设定。表人格模式下不注入任何与它相关的内容。",
			Default:     "",
		},
		{
			Key: "to_inner", Label: "切到里人格的触发词", Type: plugin.FieldText,
			Description: "一行一个，命中任一即切换。命中的那条消息照常发给模型，由新人格作答，不做任何提示。",
			Default:     "",
		},
		{
			Key: "to_outer", Label: "切回表人格的触发词", Type: plugin.FieldText,
			Description: "一行一个。切换对当轮即时生效，因此这个词会留在表人格能读到的历史里，建议选一个在表人格语境下也说得通的说法。",
			Default:     "",
		},
		{
			Key: "match_mode", Label: "触发词匹配方式", Type: plugin.FieldSelect,
			Description: "包含：消息里出现该词即命中；整句相等：整条消息就是该词才命中，更不容易误触发。",
			Default:     defaultMatch,
			Options: []plugin.ConfigOption{
				{Value: matchContains, Label: "包含"},
				{Value: matchEquals, Label: "整句相等"},
			},
		},
	}
}

// Init 需要一个可写目录来记住人格状态；没有则拒绝启用。
// 状态一丢，用户下次进来就会莫名回到表人格，那比不启用更糟。
func (p *Plugin) Init(ictx plugin.InitContext, cfg map[string]any) error {
	st, err := newStore(ictx.StateDir)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.innerPersona = strings.TrimSpace(plugin.CfgString(cfg, "inner_persona", ""))
	p.toInner = parseKeywords(plugin.CfgString(cfg, "to_inner", ""))
	p.toOuter = parseKeywords(plugin.CfgString(cfg, "to_outer", ""))
	p.matchMode = plugin.CfgString(cfg, "match_mode", defaultMatch)
	p.store = st
	return nil
}

func (p *Plugin) Tools() []plugin.Tool { return nil }

// SystemPrompt 恒为空：设定与人格有关，只能按轮注入。
func (p *Plugin) SystemPrompt() string { return "" }

// settings 是一次调用期间使用的配置快照（SetConfig 会在运行时重新 Init）。
type settings struct {
	innerPersona string
	toInner      []string
	toOuter      []string
	matchMode    string
	store        *store
}

func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{
		innerPersona: p.innerPersona,
		toInner:      p.toInner,
		toOuter:      p.toOuter,
		matchMode:    p.matchMode,
		store:        p.store,
	}
}

// DecideScope 决定本轮的人格与可见域。
//
// 切换立即对本轮生效，命中的那条消息本身就带新标签落盘。对表→里这正是想要的
// （暗号不留在表人格的历史里）；对里→表则意味着暗号会进入表人格可读的历史——这是
// 「标签按整轮分配」换来的代价：把同一轮的用户消息与助手回复拆到两个标签会打断
// user/assistant 的配对，那样的历史送不进 API。
func (p *Plugin) DecideScope(_ context.Context, ev plugin.TurnEvent) (plugin.Scope, error) {
	s := p.snapshot()
	if s.store == nil {
		return plugin.Scope{}, fmt.Errorf("人格状态尚未就绪")
	}

	cur := s.store.personaFor(ev.SessionID)
	next := switchTo(cur, ev.UserInput, s)
	if next != cur {
		log.Printf("人格切换：%s → %s（会话 %s）", cur, next, ev.SessionID)
	}
	// 每轮都写：状态文件是权威来源，历史推导不可靠（压缩会重排标签顺序）
	if err := s.store.remember(ev.SessionID, next); err != nil {
		log.Printf("保存人格状态失败，重启后可能回到表人格: %v", err)
	}
	return scopeFor(next), nil
}

// switchTo 按触发词决定本轮的人格。
// 两个方向同时命中时回表人格优先：宁可多切回来一次，也不要卡在里人格出不去。
func switchTo(cur, input string, s settings) string {
	if hit(input, s.toOuter, s.matchMode) {
		return personaOuter
	}
	if hit(input, s.toInner, s.matchMode) {
		return personaInner
	}
	return cur
}

// scopeFor 把人格翻译成可见域。里人格显式列出两个标签而不是留 nil：
// 意图是「读表里两侧」，而不是「读将来可能出现的任何域」。
func scopeFor(persona string) plugin.Scope {
	if persona == personaInner {
		return plugin.Scope{Write: personaInner, Read: []string{personaOuter, personaInner}}
	}
	return plugin.Scope{Write: personaOuter, Read: []string{personaOuter}}
}

const innerHeader = `[里人格设定 · 优先于上文的角色设定]
以下设定优先于上文的角色设定。冲突之处以这里为准，未提到的部分沿用上文的角色。
不说明自己换了人格，不提及另一面的存在，不解释这段设定。`

// TurnPrompt 只在里人格激活时注入里人格设定。
//
// 表人格激活时返回空串——注入任何提及里人格的内容，哪怕是「你不知道还有另一面」，
// 都等于让表人格知道了它的存在。
func (p *Plugin) TurnPrompt(_ context.Context, ev plugin.TurnEvent) (string, error) {
	if ev.Scope.Write != personaInner {
		return "", nil
	}
	s := p.snapshot()
	if s.innerPersona == "" {
		return "", nil
	}
	return innerHeader + "\n\n" + s.innerPersona, nil
}

// parseKeywords 按行切分触发词：去空白、丢空行、统一小写（匹配不区分大小写）。
func parseKeywords(raw string) []string {
	var out []string
	for line := range strings.SplitSeq(raw, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

// hit 判断输入是否命中任一触发词。
func hit(input string, keywords []string, mode string) bool {
	text := strings.ToLower(strings.TrimSpace(input))
	if text == "" {
		return false
	}
	for _, kw := range keywords {
		if mode == matchEquals {
			if text == kw {
				return true
			}
			continue
		}
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}
