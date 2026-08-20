package heartbeat

import (
	"context"
	"strings"
	"testing"
	"time"

	"wen/internal/cue"
	"wen/internal/plugin"
)

func drainCues() { cue.Take(time.Now().Add(24 * time.Hour)) }

func postCue(text string) {
	cue.Post(cue.Cue{Source: "test", Key: text, Text: text, Expire: time.Now().Add(time.Hour)})
}

// 有待说的理由时下一拍提前到最快间隔；固定节奏不提前；暂停压过一切。
func TestNextBeatPulledForwardByCue(t *testing.T) {
	drainCues()
	defer drainCues()
	p, _ := newInited(t, noTurn, map[string]any{
		"interval_minutes": 60, "min_minutes": 5, "max_minutes": 120,
	})

	p.mu.Lock()
	p.lastBeat = time.Now()
	base := p.nextBeatLocked()
	p.mu.Unlock()

	postCue("下雨了")
	p.mu.Lock()
	pulled := p.nextBeatLocked()
	p.mu.Unlock()
	if !pulled.Before(base) {
		t.Errorf("有理由待说时下一拍应提前: base=%v pulled=%v", base, pulled)
	}
	if got := pulled.Sub(p.lastBeat); got != 5*time.Minute {
		t.Errorf("应提前到最快间隔，得到 %v", got)
	}

	// 暂停仍然压过提前：睡着的人不为一场雨叫醒
	p.mu.Lock()
	p.pausedUntil = time.Now().Add(8 * time.Hour)
	paused := p.nextBeatLocked()
	p.pausedUntil = time.Time{}
	p.mu.Unlock()
	if paused.Sub(p.lastBeat) < 8*time.Hour {
		t.Errorf("暂停期间不该被理由叫醒: %v", paused)
	}
	drainCues()

	// 固定节奏雷打不动
	q, _ := newInited(t, noTurn, map[string]any{
		"interval_minutes": 60, "min_minutes": 5, "max_minutes": 120, "dynamic": false,
	})
	postCue("下雨了")
	q.mu.Lock()
	q.lastBeat = time.Now()
	fixed := q.nextBeatLocked()
	q.mu.Unlock()
	if got := fixed.Sub(q.lastBeat); got != 60*time.Minute {
		t.Errorf("固定节奏不该提前: %v", got)
	}
}

// 心跳把待说的理由带进当轮输入，消费即清。
func TestBeatCarriesCues(t *testing.T) {
	drainCues()
	defer drainCues()
	var got string
	p, _ := newInited(t, func(_ context.Context, _ string, input string) (string, error) {
		got = input
		return "好", nil
	}, nil)

	postCue("你所在的杭州刚下起了小雨。")
	p.beat(context.Background())

	if !strings.Contains(got, "【值得开口的事】") || !strings.Contains(got, "刚下起了小雨") {
		t.Errorf("心跳输入应带上开口理由:\n%s", got)
	}
	if cue.Pending(time.Now()) {
		t.Error("送达后公告板应已清空")
	}

	// 没有理由时不带这一段
	got = ""
	p.beat(context.Background())
	if strings.Contains(got, "值得开口的事") {
		t.Errorf("无理由时不该出现该段:\n%s", got)
	}
}

// 轮次失败时理由放回公告板，等下一拍再说。
func TestBeatRepostsCuesOnFailure(t *testing.T) {
	drainCues()
	defer drainCues()
	p, _ := newInited(t, func(context.Context, string, string) (string, error) {
		return "", plugin.ErrSessionBusy
	}, nil)

	postCue("下雨了")
	p.beat(context.Background())
	if !cue.Pending(time.Now()) {
		t.Error("会话忙时理由不该被吞掉")
	}
}
