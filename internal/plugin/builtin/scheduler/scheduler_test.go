package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wen/internal/plugin"
	"wen/internal/session"
)

func newInited(t *testing.T, runTurn plugin.RunTurnFunc) (*Plugin, *session.Store) {
	t.Helper()
	sessDir := t.TempDir()
	store, err := session.NewStore(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	ictx := plugin.InitContext{
		StateDir:   t.TempDir(),
		Sessions:   store,
		RunTurn:    runTurn,
		NewSession: func() (string, error) { m, err := store.Create(); return m.ID, err },
	}
	if err := p.Init(ictx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	// Init 起的后台循环立刻停掉：本包的用例都是直接调内部方法来驱动的，循环留着跑
	// 只会和它们抢同一份状态。它第一轮什么时候被调度是不确定的——本地通常在这里就
	// 算好了下一次到期时刻然后 park 住，CI 机器忙时会晚一步，那时用例已经把「早就
	// 到期」的状态塞了进去，于是循环立刻多跑一次，断言的计数就多了一。
	// 要测循环本身的话另起一个不停的 helper，别把这里改回去。
	p.Stop()
	return p, store
}

func noTurn(context.Context, string, string) (string, error) { return "", nil }

func mustExec(t *testing.T, tool plugin.Tool, args string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s 执行失败: %v", tool.Name(), err)
	}
	return out
}

// nextRun 的三种类型语义。
func TestNextRun(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.Local)

	once := &Task{Type: typeOnce, At: now.Add(time.Hour)}
	if got := once.nextRun(now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("once nextRun = %v", got)
	}
	once.Done = true
	if got := once.nextRun(now); !got.IsZero() {
		t.Fatalf("已完成任务不该再有下次: %v", got)
	}

	iv := &Task{Type: typeInterval, IntervalMin: 30, CreatedAt: now}
	if got := iv.nextRun(now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("interval 首跑 = %v", got)
	}
	iv.LastRun = now.Add(30 * time.Minute)
	if got := iv.nextRun(now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("interval 续跑 = %v", got)
	}

	daily := &Task{Type: typeDaily, DailyTime: "13:30"}
	if got := daily.nextRun(now); got.Hour() != 13 || got.Minute() != 30 || got.Day() != now.Day() {
		t.Fatalf("daily 今天未到点应排今天: %v", got)
	}
	daily.LastRun = time.Date(2026, 8, 17, 13, 30, 0, 0, time.Local)
	if got := daily.nextRun(now.Add(2 * time.Hour)); got.Day() != now.Day()+1 {
		t.Fatalf("daily 今天已跑过应排明天: %v", got)
	}
}

// schedule_task / list_tasks / cancel_task 全链路。
func TestToolsRoundTrip(t *testing.T) {
	p, _ := newInited(t, noTurn)
	tools := p.Tools()
	sched, list, cancel := tools[0], tools[1], tools[2]

	at := time.Now().Add(2 * time.Hour).Format("2006-01-02 15:04")
	out := mustExec(t, sched, `{"content":"提醒喝水","schedule_type":"once","at":"`+at+`"}`)
	if !strings.Contains(out, "已创建") {
		t.Fatalf("创建回执异常: %s", out)
	}
	mustExec(t, sched, `{"content":"巡检","schedule_type":"interval","interval_minutes":15}`)
	mustExec(t, sched, `{"content":"早报","schedule_type":"daily","daily_time":"08:30"}`)

	listing := mustExec(t, list, `{}`)
	for _, want := range []string{"提醒喝水", "巡检", "早报"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("列表缺少 %q:\n%s", want, listing)
		}
	}

	// 取第一个任务的 ID 并取消
	p.mu.Lock()
	id := p.tasks[0].ID
	p.mu.Unlock()
	out = mustExec(t, cancel, `{"id":"`+id+`"}`)
	if !strings.Contains(out, "已取消") {
		t.Fatalf("取消回执异常: %s", out)
	}
	if _, err := cancel.Execute(context.Background(), json.RawMessage(`{"id":"不存在"}`)); err == nil {
		t.Fatal("取消不存在的任务应报错")
	}
}

// 非法参数被拒绝。
func TestScheduleValidation(t *testing.T) {
	p, _ := newInited(t, noTurn)
	sched := p.Tools()[0]
	bad := []string{
		`{"content":"","schedule_type":"once","at":"2030-01-01 00:00"}`,
		`{"content":"x","schedule_type":"once","at":"2000-01-01 00:00"}`,
		`{"content":"x","schedule_type":"once","at":"乱写"}`,
		`{"content":"x","schedule_type":"interval","interval_minutes":0}`,
		`{"content":"x","schedule_type":"daily","daily_time":"25:00"}`,
		`{"content":"x","schedule_type":"weekly"}`,
	}
	for _, args := range bad {
		if _, err := sched.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("参数应被拒绝: %s", args)
		}
	}
}

// 任务持久化：重启（重 Init）后任务仍在。
func TestPersistence(t *testing.T) {
	sessDir, stateDir := t.TempDir(), t.TempDir()
	store, _ := session.NewStore(sessDir)
	ictx := plugin.InitContext{
		StateDir: stateDir, Sessions: store, RunTurn: noTurn,
		NewSession: func() (string, error) { m, err := store.Create(); return m.ID, err },
	}
	p := New()
	if err := p.Init(ictx, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, p.Tools()[0], `{"content":"巡检","schedule_type":"interval","interval_minutes":15}`)
	p.Stop()

	p2 := New()
	if err := p2.Init(ictx, nil); err != nil {
		t.Fatal(err)
	}
	defer p2.Stop()
	listing := mustExec(t, p2.Tools()[1], `{}`)
	if !strings.Contains(listing, "巡检") {
		t.Fatalf("重启后任务丢失:\n%s", listing)
	}
}

// 错过太久的 once 任务在启动时标记过期，不补跑。
func TestCatchUpExpiresStaleOnce(t *testing.T) {
	p, _ := newInited(t, noTurn)
	p.mu.Lock()
	p.tasks = append(p.tasks, &Task{
		ID: "tstale", Content: "旧任务", Type: typeOnce,
		At: time.Now().Add(-48 * time.Hour), CreatedAt: time.Now().Add(-72 * time.Hour),
	})
	p.catchUpLocked()
	stale := p.tasks[len(p.tasks)-1]
	p.mu.Unlock()
	if !stale.Done || !strings.Contains(stale.Note, "过期") {
		t.Fatalf("超窗任务应标记过期: %+v", stale)
	}
}

// 到期任务真的会被派发执行，且执行发生在独立 goroutine（panic 被隔离）。
func TestDispatchAndPanicIsolation(t *testing.T) {
	var calls atomic.Int32
	turn := func(ctx context.Context, sid, input string) (string, error) {
		calls.Add(1)
		if strings.Contains(input, "会崩") {
			panic("boom")
		}
		return "ok", nil
	}
	p, store := newInited(t, turn)
	_, _ = store.Create()

	p.mu.Lock()
	p.tasks = append(p.tasks,
		&Task{ID: "t1", Content: "会崩的任务", Type: typeOnce, At: time.Now().Add(-time.Second), CreatedAt: time.Now()},
		&Task{ID: "t2", Content: "正常任务", Type: typeOnce, At: time.Now().Add(-time.Second), CreatedAt: time.Now()},
	)
	p.mu.Unlock()

	p.dispatchDue(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("两个任务都应被执行，实际 %d", calls.Load())
	}
	// panic 任务不影响进程（走到这里本身即证明），且两个任务都已标记结束
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, task := range p.tasks {
		if !task.Done {
			t.Fatalf("once 任务派发后应标记结束: %+v", task)
		}
	}
}

// 会话忙时周期任务跳过本次，不重试。
func TestBusySkipsPeriodic(t *testing.T) {
	var calls atomic.Int32
	turn := func(context.Context, string, string) (string, error) {
		calls.Add(1)
		return "", plugin.ErrSessionBusy
	}
	p, store := newInited(t, turn)
	_, _ = store.Create()

	task := &Task{ID: "tp", Content: "巡检", Type: typeInterval, IntervalMin: 30, CreatedAt: time.Now().Add(-time.Hour)}
	p.mu.Lock()
	p.tasks = append(p.tasks, task)
	p.mu.Unlock()

	p.wg.Add(1)
	p.runTask(context.Background(), task)
	if calls.Load() != 1 {
		t.Fatalf("周期任务忙时应只试一次，实际 %d", calls.Load())
	}
	if !strings.Contains(task.Note, "忙") {
		t.Fatalf("应记录忙跳过: %q", task.Note)
	}
}

// 执行失败记录到备注。
func TestRunFailureNoted(t *testing.T) {
	turn := func(context.Context, string, string) (string, error) {
		return "", errors.New("模型宕了")
	}
	p, store := newInited(t, turn)
	_, _ = store.Create()
	task := &Task{ID: "tf", Content: "x", Type: typeOnce, At: time.Now(), CreatedAt: time.Now()}
	p.wg.Add(1)
	p.runTask(context.Background(), task)
	if !strings.Contains(task.Note, "失败") {
		t.Fatalf("应记录失败: %q", task.Note)
	}
}
