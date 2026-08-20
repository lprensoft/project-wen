package weather

// 天气是最自然的开口理由产地：「刚下起雨了，记得带伞」比任何无话找话都像真人。
// 本文件在每次刷新后对比前后两次观测，出现降水的开始或停止时向 internal/cue
// 投递一条开口理由，由心跳带进主动开口的轮次。
//
// 只报「转变」，不报状态——此刻的天气本来就由 TurnPrompt 每轮注入，重复报状态
// 只是噪声；转变才是「值得现在说」的事。前一次观测太旧时不报：宕机横跨的转变
// 说「刚下起来」就是错的，宁可错过。

import (
	"fmt"
	"strings"
	"time"

	"wen/internal/cue"
)

// cueTTL 是天气理由的有效期。「刚下起雨」过了这个时长还没说出口就不再算数——
// 那时天气状态早由 TurnPrompt 注入的现况接管了。
const cueTTL = 90 * time.Minute

// maybePostCue 对比一处地点的前后两次观测，值得开口时投递理由。
// 以 (weather, 地点) 为键：同一地点更晚的转变覆盖更早的——先下起又停了时，
// 该说的是「停了」，而不是按顺序把两件事都播一遍。
func (p *Plugin) maybePostCue(loc string, prev Report, prevOK bool, cur Report) {
	s := p.snapshot()
	// 前一次观测断档太久（超过两个刷新周期）就当没有先前状态
	if !prevOK || cur.Fetched.Sub(prev.Fetched) > 2*s.refresh {
		return
	}
	prefix := s.rolePrefix(loc)
	if prefix == "" {
		return // 配置已不再关心的地点
	}
	text := changeText(prefix, prev, cur)
	if text == "" {
		return
	}
	cue.Post(cue.Cue{Source: "weather", Key: loc, Text: text, Expire: cur.Fetched.Add(cueTTL)})
}

// rolePrefix 给出「这处地点是谁的」的措辞前缀，与 TurnPrompt 的注入措辞一致。
func (s settings) rolePrefix(loc string) string {
	switch {
	case s.sameCity && loc == s.personaLoc:
		return "你与对方所在的"
	case loc == s.personaLoc:
		return "你所在的"
	case loc == s.userLoc:
		return "对方所在的"
	}
	return ""
}

// changeText 把一次值得开口的天气转变写成一句话；无事可说返回空串。
func changeText(prefix string, prev, cur Report) string {
	wasWet, nowWet := isWet(prev.Condition), isWet(cur.Condition)
	switch {
	case !wasWet && nowWet:
		return fmt.Sprintf("%s%s刚下起了%s。", prefix, cur.Place, cur.Condition)
	case wasWet && !nowWet:
		return fmt.Sprintf("%s%s的%s停了，现在%s。", prefix, cur.Place, prev.Condition, cur.Condition)
	}
	return ""
}

// isWet 判断一个天气现象是否属于降水。按中文说法的用字判断而不是 WMO 代码：
// 观测缓存里的旧条目没有存代码，而这些说法是本插件自己的固定词表。
func isWet(condition string) bool {
	for _, w := range []string{"雨", "雪", "雹"} {
		if strings.Contains(condition, w) {
			return true
		}
	}
	return false
}
