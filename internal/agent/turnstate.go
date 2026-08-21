package agent

import (
	"fmt"
	"strings"
	"time"
)

// 本轮状态块的包裹标签。用成对标签而不是 [标题] 是因为它拼在用户输入之前发出，
// 需要一个明确的收尾位置把「程序生成的信息」与「对方说的话」分开。
const (
	turnStateOpen  = "<本轮状态>"
	turnStateClose = "</本轮状态>"
)

// turnStateLead 是块内的第一句话。「以这里为准」这一句是必须的：模型判断「现在」
// 时会优先采信对话文本里出现过的时刻（那是叙事事实，且离生成位置更近），
// 不明确宣告权威性的话，一段深夜的对话会让它在第二天下午继续演深夜。
const turnStateLead = "以下由程序在每轮对话前重新生成，不是对方说的话。" +
	"其中的信息与历史对话里出现过的同类说法不一致时，一律以这里为准。"

// gapThreshold 以下的间隔不在历史里作标记。每条消息都带时间戳的话噪声太大，
// 而模型真正缺的信息只有一个：两条消息之间是不是隔了很久。
const gapThreshold = 30 * time.Minute

// nowGapThreshold 是本轮那一行的阈值，比历史里的低得多：它每轮只有一行，
// 「隔了五分钟」与「刚说完」对接话方式的影响是实打实的，不算噪声。
// 低于一分钟才不说——连续对话里报「刚刚」是废话。
const nowGapThreshold = time.Minute

// turnStateBlock 组装放在历史之后、本轮用户输入之前的易变段：当前时间、距上一条
// 消息的间隔，以及各插件的每轮提示词（心情、天气、记忆索引这类）。
//
// 放在这个位置有两个理由，且是同一处改动同时解决的：其一，它离生成位置最近，
// 模型不再需要跨几千 token 主动比对才想起「现在」是几点；其二，system 与历史因此
// 成为整轮之间字节一致的前缀，提示词缓存才可能命中（见 llm 层的缓存断点）。
func turnStateBlock(now time.Time, sinceLast time.Duration, parts []string) string {
	var b strings.Builder
	b.WriteString(turnStateOpen)
	b.WriteString("\n")
	b.WriteString(turnStateLead)
	b.WriteString("\n\n当前时间: ")
	b.WriteString(formatNow(now))
	if sinceLast >= nowGapThreshold {
		fmt.Fprintf(&b, "\n距上一条消息: %s", humanizeGap(sinceLast))
	}
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			b.WriteString("\n\n")
			b.WriteString(s)
		}
	}
	b.WriteString("\n")
	b.WriteString(turnStateClose)
	return b.String()
}

// gapNote 返回插在历史某条消息之前的时间间隔提示，不需要标记时返回空串。
//
// 历史里没有任何时间流逝的信号：昨夜 23:30 说的话与今天下午的提问在上下文里紧挨着，
// 中间那十几个小时不存在，模型只能从对话文本里找时刻，于是找到的永远是旧的那个。
// 消息的时间戳本来就落了盘（session.StoredMessage.TS），补上这一行即可。
func gapNote(prev, cur time.Time) string {
	if prev.IsZero() || cur.IsZero() {
		return ""
	}
	d := cur.Sub(prev)
	if d < gapThreshold {
		return ""
	}
	return fmt.Sprintf("（此处距上一条消息%s）\n", humanizeGap(d))
}

// humanizeGap 把间隔说成人话。只给到「约」的精度：这里要传达的是「隔了多久」，
// 精确到分钟反而会让模型拿它去做不必要的推算。
func humanizeGap(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("约 %d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("约 %d 小时", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("约 %d 天", int(d.Hours()/24))
	default:
		return fmt.Sprintf("约 %d 个月", int(d.Hours()/24/30))
	}
}

var weekdayCN = [...]string{"日", "一", "二", "三", "四", "五", "六"}

// dayMark 是历史里的日界线：日期翻篇处标一行绝对日期。
//
// gapNote 给的是增量（「距上一条消息约 12 小时」），要定位历史里某句「明天」指的是
// 哪一天，模型得把那条消息到末尾的间隔逐段累加——一条它做不可靠的算术链。日界线把
// 这件事变成一次查表：那句话写在哪一天下面，「明天」就是那天的次日。
//
// 日期格式与 formatNow 的日期部分同形（2006-01-02 星期X），使「历史里那天」与
// 「当前时间」可以直接比对，不必再做一次格式换算。
//
// 一天只有一行，且由已落盘的时间戳推出、生成后再不变化，因此对提示词缓存无害——
// 它与 <本轮状态> 那种每轮重算的内容不是一回事。
func dayMark(t time.Time) string {
	return fmt.Sprintf("（以下是 %s 星期%s 的对话）", t.Format("2006-01-02"), weekdayCN[int(t.Weekday())])
}

// formatNow 输出中文时间：星期用中文而不是 Mon 这类英文缩写，并带上时区偏移。
// 时间是模型唯一无法自行获知却又必须与之保持一致的事实，格式上不该再给它额外的
// 解析负担；不写时区的话，跨时区的相对时间推算会悄悄错开。
func formatNow(t time.Time) string {
	return fmt.Sprintf("%s 星期%s %s",
		t.Format("2006-01-02 15:04"), weekdayCN[int(t.Weekday())], t.Format("Z07:00"))
}
