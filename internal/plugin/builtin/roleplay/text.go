package roleplay

import (
	"fmt"
	"strings"
	"time"

	"wen/internal/llm"
	"wen/internal/plugin"
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

// lastTS 返回历史中最后一条消息的落盘时间。
func lastTS(history []plugin.TaggedMessage) (time.Time, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if !history[i].TS.IsZero() {
			return history[i].TS, true
		}
	}
	return time.Time{}, false
}

// humanizeGap 把时长写成中文的粗略说法。精确到分秒没有意义，模型需要的是量级。
func humanizeGap(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%d 个月", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%d 年", int(d.Hours()/24/365))
	}
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
