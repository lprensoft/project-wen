package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"wen/internal/session"
)

// 会话注记的实时通道。
//
// /api/chat 的事件流是按请求的，一轮返回就关；而后台工作（记忆提炼、清扫这类）在那
// 之后才跑完，结果无处可送。这里开一条常驻的流：界面订阅一次，之后任何会话上产生的
// 注记都即时推过去，由前端按当前会话筛选。
//
// 一条流服务所有会话而不是按会话开一条：界面同一时刻只看一个会话，按会话开需要在
// 切换时反复重连，还得处理旧连接的收尾。

// noticeBuffer 是每个订阅者的待发缓冲。慢的浏览器不该拖住写注记的后台 goroutine，
// 缓冲满了就丢——注记同时已经落盘，刷新页面就能补齐。
const noticeBuffer = 32

// noticeHub 向所有订阅者广播会话注记。
type noticeHub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan []byte
}

func newNoticeHub() *noticeHub {
	return &noticeHub{subs: map[int]chan []byte{}}
}

// subscribe 返回一个接收通道与退订函数。
func (h *noticeHub) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, noticeBuffer)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
		h.mu.Unlock()
	}
}

// publish 把一条注记广播出去。绝不阻塞：它跑在插件的后台 goroutine 上。
func (h *noticeHub) publish(sessionID string, m session.StoredMessage) {
	data, err := json.Marshal(map[string]any{
		"type":       "notice",
		"session_id": sessionID,
		"content":    m.Content,
		"origin":     m.Origin,
		"tag":        m.Tag,
		"ts":         m.TS,
	})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- data:
		default: // 该订阅者积压了，丢掉这条；内容已落盘，刷新即可补齐
		}
	}
}

// events 是注记的 SSE 端点。连接一直开着，直到客户端断开或进程退出。
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.notices.subscribe()
	defer unsubscribe()

	// 心跳兼作探活：中间的代理常在闲置若干分钟后掐断连接，
	// 而注记本身可能几小时都没有一条。
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: notice\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
