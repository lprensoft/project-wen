package server

import (
	"strings"
	"testing"
	"time"

	"wen/internal/llm"
	"wen/internal/session"
)

func testNotice(text string) session.StoredMessage {
	return session.StoredMessage{
		Message: llm.Message{Role: llm.RoleSystem, Content: text},
		Kind:    session.KindNotice,
		Origin:  "memory",
		TS:      time.Now(),
	}
}

func TestNoticeHubBroadcasts(t *testing.T) {
	h := newNoticeHub()
	a, closeA := h.subscribe()
	b, closeB := h.subscribe()
	defer closeA()
	defer closeB()

	h.publish("s1", testNotice("🧠 记忆提炼：新增「事实/某事」"))

	for i, ch := range []<-chan []byte{a, b} {
		select {
		case data := <-ch:
			if !strings.Contains(string(data), "记忆提炼") || !strings.Contains(string(data), `"session_id":"s1"`) {
				t.Errorf("订阅者 %d 收到的内容不对：%s", i, data)
			}
		case <-time.After(time.Second):
			t.Errorf("订阅者 %d 没收到广播", i)
		}
	}
}

func TestNoticeHubUnsubscribe(t *testing.T) {
	h := newNoticeHub()
	ch, unsubscribe := h.subscribe()
	unsubscribe()

	if _, ok := <-ch; ok {
		t.Error("退订后通道应关闭")
	}
	h.publish("s1", testNotice("退订之后不该再收到")) // 不该 panic（往已关闭的通道写）
	unsubscribe()                            // 重复退订也应安全
}

func TestNoticeHubNeverBlocksOnSlowSubscriber(t *testing.T) {
	h := newNoticeHub()
	_, cleanup := h.subscribe() // 订阅了但一直不读
	defer cleanup()

	// publish 跑在插件的后台 goroutine 上，被一个不读的浏览器卡住是不可接受的：
	// 缓冲满了就丢，内容已经落盘，刷新页面即可补齐
	done := make(chan struct{})
	go func() {
		for range noticeBuffer * 3 {
			h.publish("s1", testNotice("塞满缓冲"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("慢订阅者不该拖住广播")
	}
}
