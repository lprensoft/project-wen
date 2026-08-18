package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"wen/internal/plugin"
)

// confirmBroker 按 openid 登记等待中的确认请求（同一用户的消息由 worker 串行处理，
// 同一时刻至多一个确认在途）。/apply、/deny 在入站分发层直接投递——它们不能进
// worker 队列：worker 正阻塞在等确认上，排队会自锁。
type confirmBroker struct {
	mu      sync.Mutex
	pending map[string]chan bool
}

func newConfirmBroker() *confirmBroker {
	return &confirmBroker{pending: map[string]chan bool{}}
}

// register 登记一个等待中的确认，返回接收答复的 channel。
func (b *confirmBroker) register(openid string) chan bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan bool, 1)
	b.pending[openid] = ch
	return ch
}

func (b *confirmBroker) release(openid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, openid)
}

// take 取出该用户正在等的那次确认，答复由调用方投递；没有等待中的确认时返回 false。
//
// 取出与投递分成两步，是为了让回执能排在放行前面：答复一旦投出去，被解开的那一轮
// 立刻继续往下跑，它的最终回复可能抢在回执之前发到用户那里——用户会先看到执行
// 结果、再看到「已允许」。
func (b *confirmBroker) take(openid string) (chan<- bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.pending[openid]
	if !ok {
		return nil, false
	}
	delete(b.pending, openid)
	return ch, true
}

// confirmerFor 构造该 openid 本轮对话的确认通道：把确认请求文案发到 QQ，阻塞等
// /apply 或 /deny，超时或断开一律按拒绝。
func (p *Plugin) confirmerFor(openid, msgID string) plugin.ConfirmFunc {
	return func(ctx context.Context, req plugin.ConfirmRequest) (bool, error) {
		p.mu.Lock()
		broker, timeout := p.broker, p.confirmTimeout
		p.mu.Unlock()

		var b strings.Builder
		b.WriteString("⚠️ 有操作需要你确认\n")
		if req.Title != "" {
			b.WriteString(req.Title + "\n")
		}
		if req.Detail != "" {
			b.WriteString(req.Detail + "\n")
		}
		if req.Reason != "" {
			b.WriteString("原因：" + req.Reason + "\n")
		}
		fmt.Fprintf(&b, "回复 /apply 允许，/deny 拒绝（%s 内未回复按拒绝处理）", timeout)

		ch := broker.register(openid)
		defer broker.release(openid)
		p.send(ctx, openid, b.String(), msgID)

		wctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		select {
		case approved := <-ch:
			return approved, nil
		case <-wctx.Done():
			return false, fmt.Errorf("等待确认超时或连接中断")
		}
	}
}
