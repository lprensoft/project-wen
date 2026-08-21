package weather

import (
	"fmt"
	"testing"

	"wen/internal/plugin"
)

// dayRep 造一次带昨天与今天概要的观测。
func dayRep(yesterday, today string) Report {
	return Report{
		Yesterday: DayInfo{Date: yesterday, Condition: "多云", MinC: 20, MaxC: 28},
		Today:     DayInfo{Date: today, Condition: "小雨", MinC: 24, MaxC: 31},
	}
}

func TestRecordDaysAndDayFacts(t *testing.T) {
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: t.TempDir()},
		map[string]any{"persona_location": "上海"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)

	p.recordDays("上海", dayRep("2026-08-20", "2026-08-21"))

	got := p.DayFacts("2026-08-21")
	if len(got) != 1 || got[0] != "天气：小雨 24~31℃" {
		t.Fatalf("当天的天气不符: %v", got)
	}
	if got := p.DayFacts("2026-08-20"); len(got) != 1 || got[0] != "天气：多云 20~28℃" {
		t.Fatalf("昨天的天气不符: %v", got)
	}
	// 没有存档的日子不编
	if got := p.DayFacts("2026-08-01"); got != nil {
		t.Fatalf("没记录的那天该沉默: %v", got)
	}
}

// 第二天实测到的「昨天」要盖掉当天记下的预报值：后到的更准。
func TestRecordDaysLaterWins(t *testing.T) {
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: t.TempDir()},
		map[string]any{"persona_location": "上海"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)

	p.recordDays("上海", dayRep("2026-08-20", "2026-08-21"))
	p.recordDays("上海", Report{
		Yesterday: DayInfo{Date: "2026-08-21", Condition: "雷阵雨", MinC: 25, MaxC: 33},
		Today:     DayInfo{Date: "2026-08-22", Condition: "晴", MinC: 26, MaxC: 35},
	})
	if got := p.DayFacts("2026-08-21"); len(got) != 1 || got[0] != "天气：雷阵雨 25~33℃" {
		t.Fatalf("次日的实测没有盖掉预报值: %v", got)
	}
}

// 没配城市时不作声：DayFacts 取的是角色所在地。
func TestDayFactsWithoutLocation(t *testing.T) {
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: t.TempDir()}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	if got := p.DayFacts("2026-08-21"); got != nil {
		t.Fatalf("没配城市不该有天气: %v", got)
	}
}

func TestDayRecordsAreCapped(t *testing.T) {
	p := New()
	for i := range maxDayRecords + 10 {
		date := fmt.Sprintf("2026-01-%02d", i%28+1)
		p.recordDays("上海", Report{Today: DayInfo{Date: date, Condition: "晴", MinC: 1, MaxC: 2}})
	}
	p.dataMu.RLock()
	n := len(p.obs["上海"].days)
	p.dataMu.RUnlock()
	if n > maxDayRecords {
		t.Fatalf("日历没有封顶: %d 条", n)
	}
}

// 日历要跨重启活下来：隔几天再聊时，那几天的收束还指望它。
func TestDayRecordsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	p := New()
	p.dataMu.Lock()
	p.entryLocked("上海")
	p.dataMu.Unlock()
	p.recordDays("上海", dayRep("2026-08-20", "2026-08-21"))
	p.save(dir)

	p2 := New()
	if err := p2.Init(plugin.InitContext{StateDir: dir},
		map[string]any{"persona_location": "上海"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p2.Stop)
	if got := p2.DayFacts("2026-08-20"); len(got) != 1 || got[0] != "天气：多云 20~28℃" {
		t.Fatalf("重启后日历丢了: %v", got)
	}
}
