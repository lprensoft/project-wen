package weather

import (
	"strings"
	"testing"
	"time"

	"wen/internal/cue"
)

func drainCues() { cue.Take(time.Now().Add(24 * time.Hour)) }

func TestIsWet(t *testing.T) {
	for cond, want := range map[string]bool{
		"晴": false, "多云": false, "有雾": false, "雾凇": false, "天气不明": false,
		"小雨": true, "冻雨": true, "雷阵雨伴冰雹": true, "阵雪": true, "米雪": true,
	} {
		if got := isWet(cond); got != want {
			t.Errorf("isWet(%q) = %v, want %v", cond, got, want)
		}
	}
}

func TestChangeTextOnlyOnTransition(t *testing.T) {
	dry := Report{Place: "杭州", Condition: "多云"}
	wet := Report{Place: "杭州", Condition: "中雨"}

	if got := changeText("你所在的", dry, wet); !strings.Contains(got, "刚下起了中雨") {
		t.Errorf("开始下雨应报: %q", got)
	}
	if got := changeText("你所在的", wet, dry); !strings.Contains(got, "中雨停了") {
		t.Errorf("雨停应报: %q", got)
	}
	// 状态未变或雨型互换都不报：状态本来就由 TurnPrompt 每轮注入
	if got := changeText("你所在的", dry, dry); got != "" {
		t.Errorf("无转变不该报: %q", got)
	}
	if got := changeText("你所在的", wet, Report{Place: "杭州", Condition: "小雨"}); got != "" {
		t.Errorf("雨大雨小不算转变: %q", got)
	}
}

func TestRolePrefixMatchesTurnPromptWording(t *testing.T) {
	s := settings{personaLoc: "杭州", userLoc: "上海"}
	if got := s.rolePrefix("杭州"); got != "你所在的" {
		t.Errorf("角色所在地 = %q", got)
	}
	if got := s.rolePrefix("上海"); got != "对方所在的" {
		t.Errorf("用户所在地 = %q", got)
	}
	if got := (settings{personaLoc: "杭州", sameCity: true}).rolePrefix("杭州"); got != "你与对方所在的" {
		t.Errorf("同城 = %q", got)
	}
	if got := s.rolePrefix("北京"); got != "" {
		t.Errorf("配置外的地点不该有措辞: %q", got)
	}
}

func TestRenderDaysChecksDates(t *testing.T) {
	const layout = "2006-01-02"
	now := time.Now()
	r := Report{
		Yesterday: DayInfo{Date: now.AddDate(0, 0, -1).Format(layout), Condition: "阴", MinC: 16, MaxC: 24},
		Tomorrow:  DayInfo{Date: now.AddDate(0, 0, 1).Format(layout), Condition: "中雨", MinC: 12, MaxC: 18},
	}
	got := renderDays(r, now)
	if !strings.Contains(got, "昨天阴，16~24℃") || !strings.Contains(got, "明天预计中雨，12~18℃") {
		t.Errorf("renderDays = %q", got)
	}

	// 跨过午夜后缓存里的「明天」其实是今天，照字面注入就是错话——按日期核对后不注入
	stale := renderDays(r, now.AddDate(0, 0, 1))
	if strings.Contains(stale, "明天") {
		t.Errorf("日期对不上的「明天」不该注入: %q", stale)
	}
	// 旧缓存没有这两项：整段为空
	if got := renderDays(Report{}, now); got != "" {
		t.Errorf("无数据应为空: %q", got)
	}
}

func TestForecastCuePostsOnFirstSight(t *testing.T) {
	drainCues()
	defer drainCues()
	p := New()
	p.personaLoc, p.refresh = "杭州", 30*time.Minute

	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	dry := Report{Place: "杭州", Condition: "多云", Fetched: now.Add(-30 * time.Minute)}
	wet := Report{Place: "杭州", Condition: "多云", Fetched: now,
		Tomorrow: DayInfo{Date: tomorrow, Condition: "中雨", MinC: 12, MaxC: 18}}

	// 第一次看到明天有雨：投递，有效期到今天为止
	p.maybePostCue("杭州", dry, true, wet)
	got := cue.Take(now)
	if len(got) != 1 || !strings.Contains(got[0].Text, "明天预报有中雨") {
		t.Fatalf("应投递预报理由: %+v", got)
	}
	if got[0].Expire.After(now.AddDate(0, 0, 2)) {
		t.Errorf("有效期不该越过明天: %v", got[0].Expire)
	}

	// 同一天的预报上次已是降水：不重投——理由可能已被心跳说出口
	p.maybePostCue("杭州", wet, true, wet)
	if cue.Pending(now) {
		t.Error("同一天已投递过的预报不该重投")
	}
}

func TestForecastCueRetractsWhenRainGone(t *testing.T) {
	drainCues()
	defer drainCues()
	p := New()
	p.personaLoc, p.refresh = "杭州", 30*time.Minute

	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	wet := Report{Place: "杭州", Condition: "多云", Fetched: now.Add(-30 * time.Minute),
		Tomorrow: DayInfo{Date: tomorrow, Condition: "中雨", MinC: 12, MaxC: 18}}
	cleared := Report{Place: "杭州", Condition: "多云", Fetched: now,
		Tomorrow: DayInfo{Date: tomorrow, Condition: "多云", MinC: 12, MaxC: 18}}

	p.maybePostCue("杭州", Report{}, false, wet) // 首见有雨 → 投递
	if !cue.Pending(now) {
		t.Fatal("应先投递")
	}
	p.maybePostCue("杭州", wet, true, cleared) // 雨从预报里消失 → 撤回
	if cue.Pending(now) {
		t.Error("雨取消后还没说出口的理由应撤回")
	}
}

func TestMaybePostCue(t *testing.T) {
	drainCues()
	p := New()
	p.personaLoc, p.refresh = "杭州", 30*time.Minute

	now := time.Now()
	prev := Report{Place: "杭州", Condition: "多云", Fetched: now.Add(-30 * time.Minute)}
	curr := Report{Place: "杭州", Condition: "小雨", Fetched: now}

	p.maybePostCue("杭州", prev, true, curr)
	got := cue.Take(now)
	if len(got) != 1 || !strings.Contains(got[0].Text, "刚下起了小雨") {
		t.Fatalf("应投递开口理由: %+v", got)
	}
	if got[0].Source != "weather" || got[0].Key != "杭州" {
		t.Errorf("来源与键 = %q %q", got[0].Source, got[0].Key)
	}
	if !got[0].Expire.After(now) {
		t.Error("理由应带过期时刻")
	}

	// 前一次观测断档太久（宕机横跨）：说「刚下起来」就是错的，不报
	stale := Report{Place: "杭州", Condition: "多云", Fetched: now.Add(-3 * time.Hour)}
	p.maybePostCue("杭州", stale, true, curr)
	if cue.Pending(now) {
		t.Error("断档过久的转变不该投递")
	}
	// 没有先前观测（首次启用）同样不报
	p.maybePostCue("杭州", Report{}, false, curr)
	if cue.Pending(now) {
		t.Error("没有先前观测不该投递")
	}
	drainCues()
}
