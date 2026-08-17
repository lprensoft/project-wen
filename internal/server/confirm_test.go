package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"wen/internal/plugin"
)

// captureEmit 记录 SSE 原始帧。
func captureEmit() (func(string, any), func() []map[string]any) {
	var mu sync.Mutex
	var frames []map[string]any
	emit := func(name string, v any) {
		raw, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["_event"] = name
		mu.Lock()
		frames = append(frames, m)
		mu.Unlock()
	}
	return emit, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), frames...)
	}
}

func newTestServer() *Server { return &Server{confirms: newConfirmBroker()} }

func TestConfirmRoundTrip(t *testing.T) {
	s := newTestServer()
	emit, frames := captureEmit()
	confirm := s.confirmerFor(emit)

	result := make(chan bool, 1)
	go func() {
		ok, err := confirm(context.Background(), plugin.ConfirmRequest{
			Source: "exec_command", Title: "执行命令", Detail: "rm -rf build", Reason: "删除文件或目录",
		})
		if err != nil {
			t.Error(err)
		}
		result <- ok
	}()

	// 等请求帧发出，从里面取 id——界面就是这么拿到 id 的
	id := waitForID(t, frames)
	if !s.confirms.resolve(id, true) {
		t.Fatal("resolve 应找到等待中的请求")
	}
	select {
	case ok := <-result:
		if !ok {
			t.Error("应返回同意")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("确认没有返回，等待方被挂住了")
	}

	// 请求帧要带上界面渲染需要的全部字段
	got := frames()
	if len(got) != 2 {
		t.Fatalf("应发出请求与定稿两帧，得到 %d 帧", len(got))
	}
	req := got[0]
	for _, k := range []string{"id", "source", "title", "detail", "reason"} {
		if req[k] == nil || req[k] == "" {
			t.Errorf("请求帧缺少 %q: %v", k, req)
		}
	}
	if req["type"] != "confirm_request" || got[1]["type"] != "confirm_done" {
		t.Errorf("帧类型不对: %v / %v", req["type"], got[1]["type"])
	}
	if got[1]["approved"] != true {
		t.Errorf("定稿帧应带上结果: %v", got[1])
	}
}

func TestConfirmDenied(t *testing.T) {
	s := newTestServer()
	emit, frames := captureEmit()
	confirm := s.confirmerFor(emit)

	result := make(chan bool, 1)
	go func() {
		ok, _ := confirm(context.Background(), plugin.ConfirmRequest{Detail: "rm x"})
		result <- ok
	}()
	s.confirms.resolve(waitForID(t, frames), false)
	if ok := <-result; ok {
		t.Error("应返回拒绝")
	}
}

func TestConfirmCancelledByDisconnect(t *testing.T) {
	// 用户关掉页面时请求上下文被取消，等待方必须解除阻塞并按拒绝处理
	s := newTestServer()
	emit, frames := captureEmit()
	confirm := s.confirmerFor(emit)

	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		ok  bool
		err error
	}
	done := make(chan res, 1)
	go func() {
		ok, err := confirm(ctx, plugin.ConfirmRequest{Detail: "rm x"})
		done <- res{ok, err}
	}()
	waitForID(t, frames)
	cancel()

	select {
	case r := <-done:
		if r.ok {
			t.Error("取消不能当作同意")
		}
		if r.err == nil {
			t.Error("应返回 error，让调用方知道这次确认没完成")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后仍被挂住")
	}
	// 定稿帧要标出是超时/断开而不是用户点了拒绝
	all := frames()
	if last := all[len(all)-1]; last["expired"] != true {
		t.Errorf("定稿帧应标记 expired: %v", last)
	}
}

func TestPendingReleasedAfterEachWait(t *testing.T) {
	// 不释放的话 pending 会随每次确认一直涨
	s := newTestServer()
	emit, frames := captureEmit()
	confirm := s.confirmerFor(emit)

	for i := range 3 {
		done := make(chan struct{})
		go func() {
			confirm(context.Background(), plugin.ConfirmRequest{Detail: "rm x"})
			close(done)
		}()
		if !s.confirms.resolve(waitForNthID(t, frames, i), false) {
			t.Fatalf("第 %d 次确认没能交付", i)
		}
		<-done
	}
	s.confirms.mu.Lock()
	n := len(s.confirms.pending)
	s.confirms.mu.Unlock()
	if n != 0 {
		t.Errorf("等待结束后 pending 应清空，剩余 %d 条", n)
	}
}

func TestResolveUnknownID(t *testing.T) {
	s := newTestServer()
	if s.confirms.resolve("nope", true) {
		t.Error("未知 id 不该被当成已交付")
	}
}

func TestConfirmHandlerStatuses(t *testing.T) {
	s := newTestServer()
	emit, frames := captureEmit()
	confirm := s.confirmerFor(emit)

	done := make(chan struct{})
	go func() {
		confirm(context.Background(), plugin.ConfirmRequest{Detail: "rm x"})
		close(done)
	}()
	id := waitForID(t, frames)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/confirmations/{id}", s.confirmResolve)

	// 正常回执
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/confirmations/"+id,
		strings.NewReader(`{"approved":true}`)))
	if rec.Code != http.StatusNoContent {
		t.Errorf("正常回执 = %d, want 204（body: %s）", rec.Code, rec.Body.String())
	}
	<-done

	// 重复回执：已失效，要让界面知道这次点击没生效
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/confirmations/"+id,
		strings.NewReader(`{"approved":true}`)))
	if rec.Code != http.StatusConflict {
		t.Errorf("重复回执 = %d, want 409", rec.Code)
	}

	// 坏 body
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/confirmations/x",
		strings.NewReader(`不是 JSON`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("坏 body = %d, want 400", rec.Code)
	}
}

func TestRandomIDsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id := randomID()
		if id == "" || seen[id] {
			t.Fatalf("id 重复或为空: %q", id)
		}
		seen[id] = true
	}
}

// waitForID 等第一个请求帧出现并返回其 id。
func waitForID(t *testing.T, frames func() []map[string]any) string {
	t.Helper()
	return waitForNthID(t, frames, 0)
}

// waitForNthID 等第 n 个（从 0 起）请求帧出现并返回其 id。
// 帧是累积的，所以连续多次确认必须按序号取，否则会重复拿到上一次那个已经处理过的 id。
func waitForNthID(t *testing.T, frames func() []map[string]any, n int) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var ids []string
		for _, f := range frames() {
			if f["type"] == "confirm_request" {
				if id, ok := f["id"].(string); ok && id != "" {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) > n {
			return ids[n]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("没有收到第 %d 个确认请求帧", n)
	return ""
}
