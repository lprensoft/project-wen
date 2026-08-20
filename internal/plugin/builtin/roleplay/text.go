package roleplay

import (
	"strings"

	"wen/internal/llm"
)

// maxSceneRunes 限制回写进摘要的场景长度：注记会长期留在会话历史里。
const maxSceneRunes = 300

// clipPair 让两段文本共用一个字节预算，第一段优先。
// 超出时截断并注明，而不是整段丢弃——设定被砍掉一半仍然能扮演，整段没了就不能。
func clipPair(first, second string, limit int) (string, string) {
	if limit <= 0 || len(first)+len(second) <= limit {
		return first, second
	}
	first = clip(first, limit)
	return first, clip(second, limit-len(first))
}

// clipTriple 在 clipPair 之上再分给台词样例：截断顺序为 设定 > 我的信息 > 样例。
// 样例排最末，因为它最可降级——没有它角色仍然成立，只是味道淡。
func clipTriple(first, second, samples string, limit int) (string, string, string) {
	first, second = clipPair(first, second, limit)
	return first, second, clipSegments(samples, limit-len(first)-len(second))
}

// clipSegments 按空行分段、整段丢弃地截断——样例被拦腰砍断本身就是一份坏范本，
// 教出来的就是断句。段落都塞不下时宁可整个不要，不做字节级截断。
func clipSegments(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	const note = "\n\n（其余样例过长，已略去）"
	budget := limit - len(note)
	if budget <= 0 {
		return ""
	}
	// 在预算内找最后一个空行边界。按字节切 s[:budget] 是安全的：
	// "\n" 不会是多字节字符的组成部分，LastIndex 只认完整的空行。
	cut := strings.LastIndex(s[:budget], "\n\n")
	if cut <= 0 {
		return ""
	}
	return strings.TrimRight(s[:cut], "\n") + note
}

// clip 按字节上限截断，不切断 UTF-8 字符。
func clip(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	const note = "…（设定过长已截断）"
	if limit <= len(note) {
		return ""
	}
	cut := limit - len(note)
	for cut > 0 && !isBoundary(s, cut) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + note
}

// isBoundary 判断字节下标是否落在 UTF-8 字符边界上。
func isBoundary(s string, i int) bool {
	return i >= len(s) || s[i]&0xC0 != 0x80
}

// lastScene 从历史中取出最后一处【】里的内容（不含括号本身）。
// 只看助手消息：用户也可能用【】，但场景的连续性是由扮演方维持的。
func lastScene(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != llm.RoleAssistant {
			continue
		}
		if s := lastBracketed(history[i].Content); s != "" {
			return truncateRunes(s, maxSceneRunes)
		}
	}
	return ""
}

// lastBracketed 返回文本中最后一对【】之间的内容。
func lastBracketed(s string) string {
	end := strings.LastIndex(s, "】")
	if end < 0 {
		return ""
	}
	start := strings.LastIndex(s[:end], "【")
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(s[start+len("【") : end])
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
