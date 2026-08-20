package availability

import (
	"testing"
	"time"
)

func reset() {
	mu.Lock()
	states = nil
	notify = nil
	mu.Unlock()
}

// Set 覆盖同源记录；Current 取未过期里档位最高的一条。
func TestSetOverridesSameSource(t *testing.T) {
	reset()
	t.Cleanup(reset)
	now := time.Now()
	Set(State{Source: "a", Activity: "跑步", Level: Light, Until: now.Add(time.Hour)})
	Set(State{Source: "a", Activity: "查资料", Level: Heavy, Until: now.Add(2 * time.Hour)})
	Set(State{Source: "b", Activity: "开会", Level: Light, Until: now.Add(time.Hour)})

	cur, ok := Current(now)
	if !ok || cur.Source != "a" || cur.Activity != "查资料" || cur.Level != Heavy {
		t.Fatalf("应取 a 的最新一条（重忙），得到 %+v ok=%v", cur, ok)
	}
	mu.Lock()
	n := len(states)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("每个来源只留一条，得到 %d 条", n)
	}
}

// Until 过期后 Current 不再返回它；Until 不在未来的写入被丢弃。
func TestExpiry(t *testing.T) {
	reset()
	t.Cleanup(reset)
	now := time.Now()
	Set(State{Source: "a", Activity: "跑步", Level: Light, Until: now.Add(time.Minute)})
	if _, ok := Current(now); !ok {
		t.Fatal("未过期时应能取到")
	}
	if _, ok := Current(now.Add(2 * time.Minute)); ok {
		t.Fatal("过期后不应再取到")
	}
	Set(State{Source: "b", Activity: "过去的事", Level: Heavy, Until: now.Add(-time.Minute)})
	if _, ok := Current(now); ok {
		t.Fatal("Until 不在未来的写入应被丢弃")
	}
	Set(State{Source: "c", Activity: "空闲", Level: Free, Until: now.Add(time.Hour)})
	if _, ok := Current(now); ok {
		t.Fatal("空闲档位不占记录")
	}
}

// Clear 撤掉记录并叫醒读取方；Set 同样叫醒。
func TestClearAndNotify(t *testing.T) {
	reset()
	t.Cleanup(reset)
	calls := 0
	SetNotify(func() { calls++ })
	now := time.Now()
	Set(State{Source: "a", Activity: "跑步", Level: Light, Until: now.Add(time.Hour)})
	if calls != 1 {
		t.Fatalf("Set 应叫醒一次，得到 %d", calls)
	}
	Clear("a")
	if calls != 2 {
		t.Fatalf("Clear 应叫醒一次，得到 %d", calls)
	}
	if _, ok := Current(now); ok {
		t.Fatal("Clear 后不应再有记录")
	}
	Clear("a") // 没有记录时不叫醒
	if calls != 2 {
		t.Fatalf("撤回不存在的记录不该叫醒，得到 %d", calls)
	}
}

func TestLevelNames(t *testing.T) {
	for _, l := range []Level{Free, Light, Heavy, Away} {
		got, ok := ParseLevel(l.String())
		if !ok || got != l {
			t.Fatalf("%v 往返失败：%v %v", l, got, ok)
		}
	}
	if _, ok := ParseLevel("很忙"); ok {
		t.Fatal("不认识的说法不该解析成功")
	}
	if Level(9).String() != "未知" {
		t.Fatal("越界档位应为「未知」")
	}
}
