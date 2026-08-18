package heartbeat

import (
	"context"
	"errors"
	"testing"
	"time"

	"wen/internal/plugin"
	"wen/internal/session"
)

func newInited(t *testing.T, runTurn plugin.RunTurnFunc, cfg map[string]any) (*Plugin, *session.Store) {
	t.Helper()
	sessDir := t.TempDir()
	store, err := session.NewStore(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	ictx := plugin.InitContext{
		StateDir:   t.TempDir(),
		SessionDir: sessDir,
		RunTurn:    runTurn,
		NewSession: func() (string, error) { m, err := store.Create(); return m.ID, err },
	}
	if err := p.Init(ictx, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p, store
}

func noTurn(context.Context, string, string) (string, error) { return "", nil }

// 间隔配置必须满足 最快 ≤ 基础 ≤ 最慢。
func TestInitValidatesIntervals(t *testing.T) {
	p := New()
	err := p.Init(plugin.InitContext{StateDir: t.TempDir(), SessionDir: t.TempDir(), RunTurn: noTurn},
		map[string]any{"interval_minutes": 3, "min_minutes": 5, "max_minutes": 120})
	if err == nil {
		t.Fatal("基础间隔小于最快间隔应报错")
	}
}

// Init 可重入：重复 Init 后旧循环被停掉，Stop 幂等。
func TestInitReentrant(t *testing.T) {
	p, store := newInited(t, noTurn, nil)
	ictx := plugin.InitContext{
		StateDir: t.TempDir(), SessionDir: t.TempDir(), RunTurn: noTurn,
		NewSession: func() (string, error) { m, err := store.Create(); return m.ID, err },
	}
	if err := p.Init(ictx, map[string]any{"interval_minutes": 10}); err != nil {
		t.Fatal(err)
	}
	p.Stop()
	p.Stop() // 幂等
}

// pickSession 挑最近活跃的会话；旧会话没有 LastActiveAt 时回落创建时间；没有会话则新建。
func TestPickSession(t *testing.T) {
	p, store := newInited(t, noTurn, nil)

	old, _ := store.Create()
	time.Sleep(1100 * time.Millisecond) // 会话 ID 精确到秒，隔开保证新会话排后
	fresh, _ := store.Create()

	// 都没有 LastActiveAt：回落创建时间挑后建的，但「上次对话时间」仍是未知
	sid, active, err := p.pickSession()
	if err != nil {
		t.Fatal(err)
	}
	if sid != fresh.ID {
		t.Fatalf("应挑最近创建的 %s，得到 %s", fresh.ID, sid)
	}
	if !active.IsZero() {
		t.Fatalf("没有 LastActiveAt 时不该拿创建时间充数，得到 %v", active)
	}

	// 老会话被真人使用过后应反超，并带回该会话的真人对话时间
	used := time.Now().Add(time.Hour).Round(time.Second)
	_ = store.SetLastActive(old.ID, used)
	sid, active, err = p.pickSession()
	if err != nil {
		t.Fatal(err)
	}
	if sid != old.ID {
		t.Fatalf("应挑最近活跃的 %s，得到 %s", old.ID, sid)
	}
	if !active.Equal(used) {
		t.Fatalf("应带回该会话的真人对话时间 %v，得到 %v", used, active)
	}
}

// 没有任何会话时心跳自己新建一个。
func TestPickSessionCreates(t *testing.T) {
	p, store := newInited(t, noTurn, nil)
	sid, active, err := p.pickSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(sid); err != nil {
		t.Fatalf("新建的会话应存在: %v", err)
	}
	if !active.IsZero() {
		t.Fatalf("刚建的会话没有「上次对话」可言，得到 %v", active)
	}
}

// 心跳轮次带一次性输入标记。
func TestBeatMarksEphemeral(t *testing.T) {
	var sawEphemeral bool
	turn := func(ctx context.Context, _, _ string) (string, error) {
		sawEphemeral = plugin.IsEphemeralInput(ctx)
		return "ok", nil
	}
	p, store := newInited(t, turn, nil)
	_, _ = store.Create()
	p.beat(context.Background())
	if !sawEphemeral {
		t.Fatal("心跳轮次应标记为一次性输入")
	}
}

// beat 在会话忙时静默跳过，不算错误。
func TestBeatSkipsBusy(t *testing.T) {
	called := 0
	busyTurn := func(context.Context, string, string) (string, error) {
		called++
		return "", plugin.ErrSessionBusy
	}
	p, store := newInited(t, busyTurn, nil)
	_, _ = store.Create()
	p.beat(context.Background())
	if called != 1 {
		t.Fatalf("RunTurn 应被调用一次，实际 %d", called)
	}
}

// 空闲衰减：超过一个间隔没有真人交互时放缓 1.5 倍，到最慢间隔封顶；动态关闭时不衰减。
func TestMaybeDecay(t *testing.T) {
	p := &Plugin{dynamic: true, minIv: 5 * time.Minute, maxIv: 60 * time.Minute, cur: 30 * time.Minute}
	p.lastActive = time.Now().Add(-time.Hour)

	p.maybeDecay()
	if p.cur != 45*time.Minute {
		t.Fatalf("衰减后 cur = %v，期望 45m", p.cur)
	}
	p.maybeDecay()
	if p.cur != 60*time.Minute {
		t.Fatalf("衰减应封顶在最慢间隔，得到 %v", p.cur)
	}

	// 刚聊过天：不衰减
	p.cur = 30 * time.Minute
	p.lastActive = time.Now()
	p.maybeDecay()
	if p.cur != 30*time.Minute {
		t.Fatalf("活跃期内不该衰减，得到 %v", p.cur)
	}

	// 动态关闭：不衰减
	p.dynamic = false
	p.lastActive = time.Now().Add(-time.Hour)
	p.maybeDecay()
	if p.cur != 30*time.Minute {
		t.Fatalf("动态关闭时不该衰减，得到 %v", p.cur)
	}
}

// 动态判定：加快对折、放缓翻倍、限幅、答非所问按保持。
func TestJudgeAdjust(t *testing.T) {
	cases := []struct {
		verdict string
		want    time.Duration
	}{
		{"加快", 15 * time.Minute},
		{"放缓", 60 * time.Minute},
		{"保持", 30 * time.Minute},
		{"我觉得可以聊聊天气", 30 * time.Minute},
	}
	for _, c := range cases {
		p := &Plugin{
			minIv: 5 * time.Minute, maxIv: 120 * time.Minute, cur: 30 * time.Minute,
			stateDir: t.TempDir(), wake: make(chan struct{}, 1), adjusting: true,
		}
		complete := func(context.Context, string) (string, error) { return c.verdict, nil }
		p.judge(context.Background(), complete, plugin.TurnEndEvent{UserInput: "在吗", FinalText: "在"}, time.Time{})
		if p.cur != c.want {
			t.Fatalf("判定 %q 后 cur = %v，期望 %v", c.verdict, p.cur, c.want)
		}
		if p.adjusting {
			t.Fatal("judge 结束后 adjusting 应复位")
		}
	}
	// 限幅：已在最快间隔时继续加快保持不变
	p := &Plugin{
		minIv: 5 * time.Minute, maxIv: 120 * time.Minute, cur: 5 * time.Minute,
		stateDir: t.TempDir(), wake: make(chan struct{}, 1), adjusting: true,
	}
	complete := func(context.Context, string) (string, error) { return "加快", nil }
	p.judge(context.Background(), complete, plugin.TurnEndEvent{}, time.Time{})
	if p.cur != 5*time.Minute {
		t.Fatalf("已到最快间隔仍被调整: %v", p.cur)
	}
}

// 判定失败不改节奏。
func TestJudgeErrorKeepsPace(t *testing.T) {
	p := &Plugin{
		minIv: 5 * time.Minute, maxIv: 120 * time.Minute, cur: 30 * time.Minute,
		stateDir: t.TempDir(), wake: make(chan struct{}, 1), adjusting: true,
	}
	complete := func(context.Context, string) (string, error) { return "", errors.New("模型不可用") }
	p.judge(context.Background(), complete, plugin.TurnEndEvent{}, time.Time{})
	if p.cur != 30*time.Minute {
		t.Fatalf("判定失败不该改间隔，得到 %v", p.cur)
	}
}

// 真人交互的轮次结束后心跳时钟重置：下一次心跳从这一刻起重新计时，聊天途中不会被心跳打断。
func TestTurnEndResetsClock(t *testing.T) {
	p, _ := newInited(t, noTurn, map[string]any{"interval_minutes": 30, "dynamic": false})

	p.mu.Lock()
	p.lastBeat = time.Now().Add(-29 * time.Minute) // 差一分钟就要心跳了
	p.mu.Unlock()

	end := time.Now()
	p.OnTurnEnd(context.Background(), plugin.TurnEndEvent{Interactive: true, StartedAt: end.Add(-time.Minute), EndedAt: end})

	p.mu.Lock()
	beat, active := p.lastBeat, p.lastActive
	p.mu.Unlock()
	if !beat.Equal(end) {
		t.Fatalf("真人轮次结束后心跳时钟应重置到 %v，得到 %v", end, beat)
	}
	if !active.Equal(end) {
		t.Fatalf("活跃时间应刷新到 %v，得到 %v", end, active)
	}
	select { // 循环挂在旧的到期时刻上，必须被叫醒重算
	case <-p.wake:
	default:
		t.Fatal("重置心跳时钟后应唤醒循环重算下一次心跳时刻")
	}
}

// 后台轮次（心跳自己、定时任务等）不重置心跳时钟，否则背景活动会把心跳无限推后。
func TestTurnEndBackgroundKeepsClock(t *testing.T) {
	p, _ := newInited(t, noTurn, map[string]any{"dynamic": false})
	before := time.Now().Add(-20 * time.Minute)
	p.mu.Lock()
	p.lastBeat = before
	p.mu.Unlock()

	p.OnTurnEnd(context.Background(), plugin.TurnEndEvent{Origin: "scheduler", EndedAt: time.Now()})
	p.OnTurnEnd(context.Background(), plugin.TurnEndEvent{EndedAt: time.Now()}) // 非交互

	p.mu.Lock()
	beat := p.lastBeat
	p.mu.Unlock()
	if !beat.Equal(before) {
		t.Fatalf("后台轮次不该重置心跳时钟，得到 %v", beat)
	}
}

// 时钟只前推不回拨：时间戳缺失或落后于当前起点时保持原值。
func TestResetClockNeverRewinds(t *testing.T) {
	now := time.Now()
	p := &Plugin{lastBeat: now, wake: make(chan struct{}, 1)}

	p.resetClockLocked(now.Add(-time.Hour))
	if !p.lastBeat.Equal(now) {
		t.Fatalf("过去的时间戳不该回拨时钟，得到 %v", p.lastBeat)
	}

	p.lastBeat = time.Time{}
	p.resetClockLocked(time.Time{}) // 零值当作「现在」
	if p.lastBeat.IsZero() {
		t.Fatal("零值时间戳应回落到当前时刻")
	}
}

// 持久化的间隔在重启后生效并按新配置限幅。
func TestIntervalPersistence(t *testing.T) {
	stateDir := t.TempDir()
	sessDir := t.TempDir()
	ictx := plugin.InitContext{StateDir: stateDir, SessionDir: sessDir, RunTurn: noTurn}

	p := New()
	if err := p.Init(ictx, nil); err != nil {
		t.Fatal(err)
	}
	p.saveInterval(10 * time.Minute)
	p.Stop()

	p2 := New()
	if err := p2.Init(ictx, nil); err != nil {
		t.Fatal(err)
	}
	defer p2.Stop()
	p2.mu.Lock()
	cur := p2.cur
	p2.mu.Unlock()
	if cur != 10*time.Minute {
		t.Fatalf("重启后应恢复持久化间隔 10m，得到 %v", cur)
	}
}

// 心跳提示词末尾附上距上次真人对话的时长；时间未知或不足一分钟时不附。
func TestGapNote(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		lastActive time.Time
		want       string
	}{
		{"未知不附", time.Time{}, "提示词"},
		{"不足一分钟不附", now.Add(-30 * time.Second), "提示词"},
		{"分钟", now.Add(-20 * time.Minute), "提示词\n\n【距上次对话】20 分钟"},
		{"跨天", now.Add(-30 * time.Hour), "提示词\n\n【距上次对话】1 天 6 小时"},
	}
	for _, c := range cases {
		if got := gapNote("提示词", c.lastActive, now); got != c.want {
			t.Errorf("%s: gapNote = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// 心跳实际发出的输入带上了时长注记，且仍是一次性输入。
func TestBeatSendsGap(t *testing.T) {
	var sent string
	turn := func(_ context.Context, _, input string) (string, error) {
		sent = input
		return "ok", nil
	}
	p, store := newInited(t, turn, map[string]any{"prompt": "心跳内容"})
	m, _ := store.Create()
	_ = store.SetLastActive(m.ID, time.Now().Add(-2*time.Hour))

	p.beat(context.Background())
	want := "心跳内容\n\n【距上次对话】2 小时"
	if sent != want {
		t.Fatalf("心跳输入 = %q，期望 %q", sent, want)
	}
}
