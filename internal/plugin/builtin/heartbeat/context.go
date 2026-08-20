package heartbeat

import (
	"sort"
	"strings"
)

// 情境提示词：同一套心跳模板每次醒来都说一样的话，机械感正来自于此。
// 用户可按情境配多套（[睡前] [闲聊] [干活]…），模型在调整节奏时顺手声明当下是
// 什么情境，下次心跳就用那一套——判断交给模型，插件只管存与换。

// contextDefault 是切回默认提示词的保留名，不允许用作情境名。
const contextDefault = "默认"

// parseContexts 解析情境提示词配置。格式：以「[情境名]」单独成行开启一段，
// 之后的行是该情境的提示词，直到下一个段头。段头之前的内容忽略；
// 空名、保留名与空提示词的段丢弃。
func parseContexts(raw string) map[string]string {
	out := map[string]string{}
	name := ""
	var lines []string
	flush := func() {
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		if name != "" && name != contextDefault && body != "" {
			out[name] = body
		}
		lines = nil
	}
	for line := range strings.SplitSeq(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && len(trimmed) > 2 {
			flush()
			name = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return out
}

// contextNames 列出已配置的情境名（排序保证提示与 schema 稳定）。
func contextNames(contexts map[string]string) []string {
	names := make([]string, 0, len(contexts))
	for n := range contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// beatPromptLocked 返回本次心跳该用的提示词：当前情境有配置就用它，否则用默认。
// 调用方需持有 p.mu。
func (p *Plugin) beatPromptLocked() string {
	if tpl, ok := p.contexts[p.curContext]; ok {
		return tpl
	}
	return p.prompt
}

// cfgRawText 读一个多行文本配置项。键**缺失**时返回默认值（没配置过），键存在时
// 原样返回——包括空串（FieldText 的空串是合法取值，用户清空后不该长回默认值）。
func cfgRawText(cfg map[string]any, key, def string) string {
	if cfg == nil {
		return def
	}
	if s, ok := cfg[key].(string); ok {
		return s
	}
	return def
}
