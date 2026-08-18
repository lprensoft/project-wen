package weather

import (
	"fmt"
	"strings"
	"time"

	"wen/internal/plugin"
)

// StatusLines 报一行天气的运行状况。
//
// 「注入了什么」与「为什么没注入」在这里是同一个问题：没配城市、取不到、取到了但
// 已经过期，三种情况在对话里看起来都一样——角色不提天气。状态行是唯一能把它们
// 区分开的地方，所以三种都说。
func (p *Plugin) StatusLines() []string {
	s := p.snapshot()
	if s.location == "" {
		return []string{"🌦 天气：未设置城市，不注入"}
	}

	rep, ok, lastErr := p.lastReport()
	if !ok {
		line := fmt.Sprintf("🌦 天气：尚未取得（%s）", s.location)
		if lastErr != "" {
			line += "，" + lastErr
		}
		return []string{line}
	}

	age := time.Since(rep.Fetched)
	line := fmt.Sprintf("🌦 天气：%s %s %.0f℃（%s", shortPlace(rep.Place), rep.Condition, rep.TempC, humanizeAge(age))
	if s.stale > 0 && age > s.stale {
		line += fmt.Sprintf("，已超过 %d 分钟未更新，暂不注入）", int(s.stale.Minutes()))
	} else {
		line += fmt.Sprintf("，每 %d 分钟刷新）", int(s.refresh.Minutes()))
	}
	return []string{line}
}

var _ plugin.StatusReporter = (*Plugin)(nil)

// lastReport 返回最近一次成功的观测（不判过期）与最近一次失败的原因。
func (p *Plugin) lastReport() (Report, bool, string) {
	p.dataMu.RLock()
	defer p.dataMu.RUnlock()
	return p.cur, p.curOK, p.lastErr
}

// shortPlace 取地名的第一段。状态行是给人扫一眼的，「杭州 · 浙江省 · 中国」太长。
func shortPlace(name string) string {
	if i := strings.Index(name, " · "); i > 0 {
		return name[:i]
	}
	return name
}

// humanizeAge 把间隔说成人话。
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚更新"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前更新", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前更新", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前更新", int(d.Hours()/24))
	}
}
