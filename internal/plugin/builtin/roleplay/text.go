package roleplay

import (
	"strings"

	"wen/internal/llm"
	"wen/internal/textclip"
)

// maxSceneRunes 限制回写进摘要的场景长度：注记会长期留在会话历史里。
const maxSceneRunes = 300

// clipTriple 让三段文本共用一个字节预算，截断顺序为 设定 > 我的信息 > 样例。
// 样例排最末，因为它最可降级——没有它角色仍然成立，只是味道淡。
func clipTriple(first, second, samples string, limit int) (string, string, string) {
	first, second = textclip.ClipPair(first, second, limit)
	return first, second, textclip.ClipSegments(samples, limit-len(first)-len(second))
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
