// Package roleplay 提供角色扮演的系统插件：把用户配置的角色设定与用户自己的信息作为
// 最高优先级提示词注入，附一套让中文表达脱离机械感的规则、一套以【】做场景与动作
// 演绎的规则，以及时间一致性约束。
//
// 注意界面与提示词的人称是相反的：设置页上那一项叫「我的信息」（填的人是用户自己），
// 而注入给模型的段落写作「对方信息」——提示词是模型以第一人称读的，那里的「我」指它。
// 两处不要统一。
//
// 本插件不提供工具，也不落盘。它硬依赖 memory 与 session_search：一个有连续性的
// 角色需要记得住跨会话的事、查得到之前说过的原话，缺了这两样，角色每开一个新会话
// 就重新变成陌生人。
package roleplay

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"wen/internal/plugin"
)

const (
	defaultInteraction = true
	defaultHumanize    = true
	defaultTimeRules   = true
	defaultMemoryRules = true
	// defaultMaxTextBytes 限制角色设定与用户信息的合计长度。这些内容每轮随 system
	// 消息全额重发、且不参与预算裁剪，还计入自动压缩判据，必须有硬上限。
	defaultMaxTextBytes = 8 * 1024
)

// Plugin 是 roleplay 系统插件。
type Plugin struct {
	mu sync.RWMutex

	persona      string
	userProfile  string
	interaction  bool
	humanize     bool
	timeRules    bool
	memoryRules  bool
	maxTextBytes int
}

func New() *Plugin {
	return &Plugin{
		interaction:  defaultInteraction,
		humanize:     defaultHumanize,
		timeRules:    defaultTimeRules,
		memoryRules:  defaultMemoryRules,
		maxTextBytes: defaultMaxTextBytes,
	}
}

func (p *Plugin) Name() string { return "roleplay" }

func (p *Plugin) Category() string { return plugin.CategoryPersona }
func (p *Plugin) Description() string {
	return "按设定扮演角色：注入角色设定与我的信息，以【】演绎场景动作，约束表达方式与时间一致性"
}

// Requires 硬依赖记忆与会话检索：角色的连续性建立在「记得住」与「查得到」之上。
func (p *Plugin) Requires() []string { return []string{"memory", "session_search"} }

func (p *Plugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key: "persona", Label: "角色设定", Type: plugin.FieldText,
			Description: "扮演谁、什么性格、什么处境、怎么说话。作为最高优先级提示词每轮注入。留空则不注入设定，只保留表达规则。",
			Default:     "",
		},
		{
			// 标签用「我的信息」：填的人就是你自己。注入给模型的那一段仍写作
			// 「对方信息」——提示词是模型以第一人称读的，那里的「我」指它自己。
			Key: "user_profile", Label: "我的信息", Type: plugin.FieldText,
			Description: "你的基本情况，作为角色一开始就知道的信息。可以留空，改在对话中告诉它，由记忆插件记下来。",
			Default:     "",
		},
		{
			Key: "interaction", Label: "启用【】互动演绎", Type: plugin.FieldBool,
			Description: "每次回复先以【】写场景、动作与表情，再说话；一轮中可多次出现。",
			Default:     defaultInteraction,
		},
		{
			Key: "humanize", Label: "启用自然表达规则", Type: plugin.FieldBool,
			Description: "抑制三段式、否定对举、过渡套话、客套收尾等让中文显得机械的表达。",
			Default:     defaultHumanize,
		},
		{
			Key: "time_rules", Label: "启用时间一致性约束", Type: plugin.FieldBool,
			Description: "要求涉及时间的表述与本轮状态中的当前时间一致（而不是沿用历史里出现过的时刻），并把运行环境信息排除在角色自我认知之外。",
			Default:     defaultTimeRules,
		},
		{
			Key: "memory_rules", Label: "启用记忆与回想约束", Type: plugin.FieldBool,
			Description: "引导给生活类记忆标上会淡忘、结论被推翻时修订旧记忆，" +
				"并要求不去翻历史对话的原文复述细节——记不清就照记不清说。",
			Default: defaultMemoryRules,
		},
		{
			Key: "max_text_bytes", Label: "设定文本上限（字节）", Type: plugin.FieldInt,
			Description: "角色设定与我的信息的合计上限。它们每轮全额重发且不参与预算裁剪，超出部分会被截断。",
			Default:     defaultMaxTextBytes,
			Min:         plugin.IntPtr(512),
			Max:         plugin.IntPtr(64 * 1024),
		},
	}
}

func (p *Plugin) Init(_ plugin.InitContext, cfg map[string]any) error {
	persona := strings.TrimSpace(plugin.CfgString(cfg, "persona", ""))
	profile := strings.TrimSpace(plugin.CfgString(cfg, "user_profile", ""))
	limit := plugin.CfgInt(cfg, "max_text_bytes", defaultMaxTextBytes)

	p.mu.Lock()
	defer p.mu.Unlock()
	// 两段共享一个预算，角色设定优先：它是这个插件存在的理由
	p.persona, p.userProfile = clipPair(persona, profile, limit)
	p.interaction = plugin.CfgBool(cfg, "interaction", defaultInteraction)
	p.humanize = plugin.CfgBool(cfg, "humanize", defaultHumanize)
	p.timeRules = plugin.CfgBool(cfg, "time_rules", defaultTimeRules)
	p.memoryRules = plugin.CfgBool(cfg, "memory_rules", defaultMemoryRules)
	p.maxTextBytes = limit
	return nil
}

func (p *Plugin) Tools() []plugin.Tool { return nil }

// settings 是一次调用期间使用的配置快照。
type settings struct {
	persona     string
	userProfile string
	interaction bool
	humanize    bool
	timeRules   bool
	memoryRules bool
}

// snapshot 取一份配置快照：SetConfig 会在运行时重新 Init，而提示词可能正在生成。
func (p *Plugin) snapshot() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return settings{
		persona:     p.persona,
		userProfile: p.userProfile,
		interaction: p.interaction,
		humanize:    p.humanize,
		timeRules:   p.timeRules,
		memoryRules: p.memoryRules,
	}
}

// SystemPrompt 拼装各段设定与规则。全部内容都是静态的——设定来自配置、规则是常量、
// 当前时间由核心的本轮状态块每轮提供——因此不需要按轮生成。
func (p *Plugin) SystemPrompt() string {
	s := p.snapshot()
	var parts []string
	if s.persona != "" {
		parts = append(parts, personaHeader+"\n\n"+s.persona)
	}
	if s.userProfile != "" {
		parts = append(parts, userHeader+"\n\n"+s.userProfile)
	}
	if s.interaction {
		parts = append(parts, interactionRules)
		// 身高规则依附于动作描写，不做演绎时它没有作用对象
		if s.persona != "" && s.userProfile != "" {
			parts = append(parts, heightRules)
		}
	}
	if s.humanize {
		parts = append(parts, humanizeRules)
	}
	if s.timeRules {
		parts = append(parts, timeRules)
	}
	if s.memoryRules {
		parts = append(parts, memoryRules)
	}
	return strings.Join(parts, "\n\n")
}

// OnCompact 在历史被物理删除前抽出最后一处【】原文，作为注记留在摘要末尾。
//
// 压缩会把整段历史换成一份摘要，而摘要是对内容的概述，场景与姿态这类细节正是它会
// 丢掉的部分——压缩一次场景就断了。这里只做文本提取，不发起模型调用。
func (p *Plugin) OnCompact(_ context.Context, ev plugin.CompactEvent) (string, error) {
	if !p.snapshot().interaction {
		return "", nil
	}
	scene := lastScene(ev.History)
	if scene == "" {
		return "", nil
	}
	return fmt.Sprintf("（压缩前的最后一处场景演绎，后续对话应从此处继续：%s）", scene), nil
}
