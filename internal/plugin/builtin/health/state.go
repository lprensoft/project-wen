// Package health 提供健康的系统插件：角色会像真人一样偶尔有些小恙。
//
// 分工只有一条：代码管时间，模型管判断。会不会生病、生什么、多重、怎么处理，
// 全是叙事判断，由模型在事发的那一轮经工具记下；之后严重度随时间按病程曲线自动
// 起、峰、落，恢复快慢由处理方式决定，读取时按落盘时刻折算、不回写。插件不做
// 疾病模拟器，没有随机事件，也没有 HP 条——注入给模型看的是处境，不是数值。
//
// 本插件依赖 roleplay：没有角色，就没有「谁的身体」。
package health

import (
	"fmt"
	"math"
	"time"
)

// 严重度三档。模型只跟这三个词打交道，内部的 0-100 数值只用来推曲线。
const (
	sevMild     = "有点不舒服"
	sevModerate = "难受"
	sevSevere   = "病倒了"
)

// 处理方式三种，决定恢复的快慢。
const (
	careTough  = "硬扛"
	careMeds   = "吃药"
	careDoctor = "就医"
)

const (
	// riseEnd 是病程里加重段的占比：前三成在往重里走，峰值落在这里，之后转好。
	riseEnd = 0.3
	// onsetRatio 是刚发作时的严重度占峰值的比例——发作不是从零起跳的。
	onsetRatio = 0.25

	// maxNameRunes 限制状况名的长度。它由模型写入又逐轮进上下文，必须限死。
	maxNameRunes = 12
	// 病程天数的取值范围。日常小恙不会拖过两周，过了那就不是小恙。
	minDays, maxDays = 1, 14
	// 延迟发作最多两天：「下午淋雨晚上难受」是两三个小时，不是下个星期。
	maxOnsetHours = 48
)

// Condition 是一条身体状况。
//
// Progress / ProgressAt 是病程进度的折算基准：改处理方式时先把进度折算到此刻再
// 换速率，读取路径只折算不回写。刚登记时基准就是发作时刻、进度为 0。
type Condition struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Peak       int       `json:"peak"`  // 峰值严重度 0-100
	Onset      time.Time `json:"onset"` // 预计（或实际）发作时刻
	Days       int       `json:"days"`  // 硬扛着走完整个病程要几天
	Care       string    `json:"care"`
	Progress   float64   `json:"progress"`
	ProgressAt time.Time `json:"progress_at"`
	Cued       bool      `json:"cued,omitempty"` // 发作的开口理由是否已投递（或已错过）
}

// 阶段。
const (
	phasePending   = "pending"   // 还没发作，只是苗头
	phaseRising    = "rising"    // 在加重
	phaseFalling   = "falling"   // 在好转
	phaseRecovered = "recovered" // 已走完病程
)

// View 是一条状况折算到某一时刻的样子，渲染与状态行都从它取。
type View struct {
	Cond      Condition
	Phase     string
	Severity  int           // 此刻的严重度 0-100
	Day       int           // 第几天（按日历日，发作当天为第 1 天）
	Remaining time.Duration // 距预计痊愈还有多久
}

// peakFor 把严重度的词换成峰值数值；不认识的词返回 false。
func peakFor(word string) (int, bool) {
	switch word {
	case sevMild:
		return 30, true
	case sevModerate:
		return 55, true
	case sevSevere:
		return 85, true
	}
	return 0, false
}

// capFor 把「严重度上限」的配置值换成峰值封顶。
func capFor(value string) int {
	switch value {
	case capMild:
		return 35
	case capSevere:
		return 100
	}
	return 65
}

// band 按数值给出严重度的词。分档线与 capFor 的封顶一致：封在「难受」档就永远到不了
// 「病倒了」那个词。
func band(v int) string {
	switch {
	case v <= 0:
		return ""
	case v <= 35:
		return sevMild
	case v <= 65:
		return sevModerate
	}
	return sevSevere
}

// careFactor 是各处理方式在好转段的速率倍数。加重段不受它影响——药能让人好得快，
// 拦不住病先发出来。
func careFactor(care string) float64 {
	switch care {
	case careMeds:
		return 1.6
	case careDoctor:
		return 2.5
	}
	return 1
}

func validCare(care string) bool {
	return care == careTough || care == careMeds || care == careDoctor
}

func (c Condition) course() time.Duration {
	return time.Duration(max(c.Days, minDays)) * 24 * time.Hour
}

// progressAt 把病程进度折算到 now。基准时刻之前（还没发作）原样返回。
func (c Condition) progressAt(now time.Time) float64 {
	p := c.Progress
	elapsed := now.Sub(c.ProgressAt)
	if elapsed <= 0 || p >= 1 {
		return min(p, 1)
	}
	course := c.course()
	if p < riseEnd {
		need := time.Duration((riseEnd - p) * float64(course))
		if elapsed < need {
			return p + float64(elapsed)/float64(course)
		}
		p, elapsed = riseEnd, elapsed-need
	}
	p += careFactor(c.Care) * float64(elapsed) / float64(course)
	return min(p, 1)
}

// recoveryAt 给出按当前处理方式走完病程的时刻。
func (c Condition) recoveryAt() time.Time {
	p := c.Progress
	if p >= 1 {
		return c.ProgressAt
	}
	course := float64(c.course())
	var d float64
	if p < riseEnd {
		d += (riseEnd - p) * course
		p = riseEnd
	}
	d += (1 - p) * course / careFactor(c.Care)
	return c.ProgressAt.Add(time.Duration(d))
}

// severityAt 把进度换成严重度：加重段从峰值的一部分线性升到峰值，好转段线性落到 0。
func severityAt(peak int, p float64) int {
	switch {
	case p <= 0:
		return 0
	case p >= 1:
		return 0
	case p < riseEnd:
		return int(math.Round(float64(peak) * (onsetRatio + (1-onsetRatio)*p/riseEnd)))
	}
	return int(math.Round(float64(peak) * (1 - p) / (1 - riseEnd)))
}

// view 折算一条状况到 now。
func (c Condition) view(now time.Time) View {
	v := View{Cond: c}
	if now.Before(c.Onset) {
		v.Phase = phasePending
		v.Remaining = c.recoveryAt().Sub(now)
		return v
	}
	p := c.progressAt(now)
	v.Severity = severityAt(c.Peak, p)
	v.Day = calendarDays(c.Onset, now) + 1
	v.Remaining = c.recoveryAt().Sub(now)
	switch {
	case p >= 1:
		v.Phase = phaseRecovered
	case p < riseEnd:
		v.Phase = phaseRising
	default:
		v.Phase = phaseFalling
	}
	return v
}

// calendarDays 算两个时刻隔了几个日历日。按日历日而不是按 24 小时：昨晚发作的病
// 今天早上就是「第 2 天」。倒流时按 0 算。
func calendarDays(from, to time.Time) int {
	day := func(x time.Time) time.Time {
		y, m, d := x.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, x.Location())
	}
	days := int(day(to).Sub(day(from.In(to.Location()))).Hours() / 24)
	return max(days, 0)
}

// ---------- 翻成话 ----------

// careText 是状况行里处理方式的说法。
func careText(care string) string {
	switch care {
	case careMeds:
		return "吃了药"
	case careDoctor:
		return "看过医生"
	}
	return "硬扛着"
}

// careStatus 是状态行里处理方式的说法。
func careStatus(care string) string {
	switch care {
	case careMeds:
		return "吃药中"
	case careDoctor:
		return "看过医生"
	}
	return "硬扛中"
}

// untilText 把「还有多久发作」说成话。
func untilText(d time.Duration) string {
	h := d.Hours()
	switch {
	case h < 1:
		return "不到一小时后"
	case h < 24:
		return fmt.Sprintf("约 %d 小时后", int(math.Round(h)))
	}
	return fmt.Sprintf("约 %d 天后", int(math.Round(h/24)))
}

// recoverText 把「还有多久好」说成话。说的是区间而不是精确值：病程本来就是估的，
// 「预计再 2.4 天好」这种精度是假的。
func recoverText(d time.Duration) string {
	h := d.Hours()
	switch {
	case h <= 0:
		return "该好了"
	case h < 12:
		return "预计今天内就好"
	case h < 36:
		return "预计明天就好"
	}
	days := int(math.Ceil(h / 24))
	var span string
	switch {
	case days <= 2:
		span = "两三天"
	case days == 3:
		span = "三四天"
	case days == 4:
		span = "四五天"
	case days <= 6:
		span = "五六天"
	case days <= 9:
		span = "一周左右"
	default:
		span = fmt.Sprintf(" %d 天左右", days)
	}
	return "预计再" + span + "好"
}

// renderView 渲染一条状况。四种形态：苗头 / 在加重 / 停在峰值档 / 从峰值档转轻。
// 数值一个都不出现：模型看到的是处境，不是 HP。
func renderView(v View, now time.Time) string {
	c := v.Cond
	switch v.Phase {
	case phasePending:
		return fmt.Sprintf("%s的苗头：预计%s发作，到时大概会%s。",
			c.Name, untilText(c.Onset.Sub(now)), band(c.Peak))
	case phaseRising:
		return fmt.Sprintf("%s第 %d 天，%s，还在往重里走（%s），%s。",
			c.Name, v.Day, band(v.Severity), careText(c.Care), recoverText(v.Remaining))
	}
	cur, peak := band(v.Severity), band(c.Peak)
	if cur == peak {
		return fmt.Sprintf("%s第 %d 天，%s（%s），%s。",
			c.Name, v.Day, cur, careText(c.Care), recoverText(v.Remaining))
	}
	return fmt.Sprintf("%s第 %d 天，从%s转成%s（%s），%s。",
		c.Name, v.Day, peak, cur, careText(c.Care), recoverText(v.Remaining))
}

// statusText 是状态行里一条状况的说法。
func statusText(v View, now time.Time) string {
	c := v.Cond
	if v.Phase == phasePending {
		return fmt.Sprintf("%s（预计%s发作）", c.Name, untilText(c.Onset.Sub(now)))
	}
	return fmt.Sprintf("%s第 %d 天（%s）", c.Name, v.Day, careStatus(c.Care))
}
