package cue

import (
	"fmt"
	"testing"
	"time"
)

// drain 清空公告板，隔离用例（包级单例）。
func drain() { Take(time.Now().Add(24 * time.Hour)) }

func TestPostTakeInOrder(t *testing.T) {
	drain()
	exp := time.Now().Add(time.Hour)
	Post(Cue{Source: "weather", Key: "a", Text: "下雨了", Expire: exp})
	Post(Cue{Source: "weather", Key: "b", Text: "雨停了", Expire: exp})

	got := Take(time.Now())
	if len(got) != 2 || got[0].Text != "下雨了" || got[1].Text != "雨停了" {
		t.Errorf("应按投递顺序取走全部: %+v", got)
	}
	// 消费即清
	if Pending(time.Now()) {
		t.Error("Take 之后不该还有待消费的理由")
	}
}

func TestPostIdempotentByKey(t *testing.T) {
	drain()
	exp := time.Now().Add(time.Hour)
	Post(Cue{Source: "weather", Key: "k", Text: "旧说法", Expire: exp})
	Post(Cue{Source: "weather", Key: "k", Text: "新说法", Expire: exp})
	// 不同插件的同名键互不覆盖
	Post(Cue{Source: "other", Key: "k", Text: "别家的", Expire: exp})

	got := Take(time.Now())
	if len(got) != 2 || got[0].Text != "新说法" {
		t.Errorf("同 (Source,Key) 应覆盖，不同 Source 应并存: %+v", got)
	}
}

func TestExpiredNotDelivered(t *testing.T) {
	drain()
	Post(Cue{Source: "s", Key: "old", Text: "过期的", Expire: time.Now().Add(time.Minute)})
	// 「刚下起雨」放到两小时后说出口就是错的
	if got := Take(time.Now().Add(2 * time.Hour)); len(got) != 0 {
		t.Errorf("过期理由不该送达: %+v", got)
	}
	// 一开始就过期的投递直接拒收
	Post(Cue{Source: "s", Key: "dead", Text: "早过期", Expire: time.Now().Add(-time.Minute)})
	if Pending(time.Now()) {
		t.Error("已过期的投递不该入板")
	}
}

func TestCapDropsOldest(t *testing.T) {
	drain()
	exp := time.Now().Add(time.Hour)
	for i := range maxCues + 5 {
		Post(Cue{Source: "s", Key: fmt.Sprintf("k%d", i), Text: "x", Expire: exp})
	}
	got := Take(time.Now())
	if len(got) != maxCues {
		t.Errorf("容量上限失效: %d", len(got))
	}
	if got[0].Key != "k5" {
		t.Errorf("超限时应丢最早的: 首条是 %s", got[0].Key)
	}
}

func TestDropRetractsUndelivered(t *testing.T) {
	drain()
	exp := time.Now().Add(time.Hour)
	Post(Cue{Source: "weather", Key: "a", Text: "明天有雨", Expire: exp})
	Post(Cue{Source: "weather", Key: "b", Text: "另一条", Expire: exp})

	Drop("weather", "a")
	Drop("weather", "没有的键") // 幂等：不存在什么都不发生

	got := Take(time.Now())
	if len(got) != 1 || got[0].Key != "b" {
		t.Errorf("撤回后应只剩另一条: %+v", got)
	}
}

func TestNotifyOnPost(t *testing.T) {
	drain()
	called := 0
	SetNotify(func() { called++ })
	defer SetNotify(nil)
	Post(Cue{Source: "s", Key: "k", Text: "x", Expire: time.Now().Add(time.Hour)})
	if called != 1 {
		t.Errorf("投递应叫醒消费方: called=%d", called)
	}
	drain()
}
