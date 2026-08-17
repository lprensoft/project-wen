package qqbot

import (
	"sync"
	"time"
)

// replyLimiter 追踪每条入站消息已被动回复的次数：官方限制同一 msg_id 在 60 分钟内
// 最多回复 4 条（超时或超次都只能改发主动消息）。msg_seq 用递增计数，同一 msg_id
// 下不可重复。
type replyLimiter struct {
	mu      sync.Mutex
	entries map[string]*replyEntry
}

type replyEntry struct {
	count int
	first time.Time
}

const (
	replyLimit  = 4
	replyWindow = time.Hour
	maxTracked  = 10000
)

func newReplyLimiter() *replyLimiter {
	return &replyLimiter{entries: map[string]*replyEntry{}}
}

// next 判定下一条出站消息能否挂在 msgID 上被动回复。
// 可以则返回 (true, 本次要用的 msg_seq) 并记账；不能（超窗/超次）返回 (false, 0)，
// 调用方应降级为主动消息。
func (l *replyLimiter) next(msgID string) (bool, int) {
	if msgID == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.entries[msgID]
	if !ok {
		if len(l.entries) >= maxTracked {
			l.evictLocked(now)
		}
		e = &replyEntry{first: now}
		l.entries[msgID] = e
	}
	if now.Sub(e.first) > replyWindow || e.count >= replyLimit {
		return false, 0
	}
	e.count++
	return true, e.count
}

func (l *replyLimiter) evictLocked(now time.Time) {
	for id, e := range l.entries {
		if now.Sub(e.first) > replyWindow {
			delete(l.entries, id)
		}
	}
}

// deduper 按消息 id 去重：平台会因网络重试、断线重连等原因重复推送同一事件。
// 窗口取 5 分钟——模型一轮回复动辄几十秒，太短的窗口挡不住重连场景的重推。
type deduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

const (
	dedupWindow  = 5 * time.Minute
	dedupMaxSize = 1000
)

func newDeduper() *deduper { return &deduper{seen: map[string]time.Time{}} }

// isDuplicate 判定并记录一条消息；重复返回 true。
func (d *deduper) isDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if len(d.seen) > dedupMaxSize {
		for id, ts := range d.seen {
			if now.Sub(ts) > dedupWindow {
				delete(d.seen, id)
			}
		}
	}
	if ts, ok := d.seen[msgID]; ok && now.Sub(ts) <= dedupWindow {
		return true
	}
	d.seen[msgID] = now
	return false
}
