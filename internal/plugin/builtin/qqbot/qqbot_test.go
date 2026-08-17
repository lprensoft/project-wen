package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"wen/internal/plugin"
	"wen/internal/session"
)

// ---------- 假 QQ 开放平台：鉴权 + 网关 + 发消息 API 三合一 ----------

type sentMsg struct {
	openid  string
	content string
	msgID   string
	seq     int
}

type fakeQQ struct {
	t     *testing.T
	srv   *httptest.Server
	sends chan sentMsg

	mu   sync.Mutex
	conn *websocket.Conn
	seq  int64

	ready chan struct{} // 收到 Identify 并回了 READY
}

func newFakeQQ(t *testing.T) *fakeQQ {
	f := &fakeQQ{t: t, sends: make(chan sentMsg, 32), ready: make(chan struct{}, 4)}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/getAppAccessToken", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "TESTTOKEN", "expires_in": "7200"})
	})
	mux.HandleFunc("GET /gateway", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "QQBot TESTTOKEN" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"op": opHello, "d": map[string]any{"heartbeat_interval": 60000}})
		// 期待 Identify（或 Resume）
		var frame wsFrame
		if _, raw, err := conn.ReadMessage(); err != nil || json.Unmarshal(raw, &frame) != nil {
			return
		}
		if frame.Op != opIdentify && frame.Op != opResume {
			f.t.Errorf("期待 Identify/Resume，收到 op=%d", frame.Op)
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		f.push("READY", map[string]any{"session_id": "gw-sess", "user": map[string]string{"username": "测试机器人"}})
		f.ready <- struct{}{}
		for { // 消耗心跳直到断开
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("POST /v2/users/{openid}/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
			MsgID   string `json:"msg_id"`
			MsgSeq  int    `json:"msg_seq"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.sends <- sentMsg{openid: r.PathValue("openid"), content: body.Content, msgID: body.MsgID, seq: body.MsgSeq}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "out-1"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// push 向已连接的客户端推一条事件。
func (f *fakeQQ) push(eventType string, d any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		f.t.Fatalf("推送 %s 时网关尚未连接", eventType)
	}
	f.seq++
	raw, _ := json.Marshal(d)
	_ = f.conn.WriteJSON(map[string]any{"op": opDispatch, "s": f.seq, "t": eventType, "d": json.RawMessage(raw)})
}

var msgCounter atomic.Int64

// pushC2C 推一条私聊消息。
func (f *fakeQQ) pushC2C(openid, content string) string {
	id := fmt.Sprintf("msg-%d", msgCounter.Add(1))
	f.push("C2C_MESSAGE_CREATE", map[string]any{
		"id": id, "content": content,
		"author": map[string]string{"user_openid": openid},
	})
	return id
}

// expectSend 等待一条发出的消息。
func (f *fakeQQ) expectSend(t *testing.T) sentMsg {
	t.Helper()
	select {
	case m := <-f.sends:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("等待出站消息超时")
		return sentMsg{}
	}
}

func (f *fakeQQ) expectNoSend(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-f.sends:
		t.Fatalf("不应有出站消息，收到: %+v", m)
	case <-time.After(d):
	}
}

// newInited 起一个连着假平台的插件。
func newInited(t *testing.T, runTurn plugin.RunTurnFunc, whitelist string) (*Plugin, *fakeQQ, *session.Store) {
	t.Helper()
	f := newFakeQQ(t)
	sessDir := t.TempDir()
	store, err := session.NewStore(sessDir)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	p.tokenURLOverride = f.srv.URL + "/app/getAppAccessToken"
	ictx := plugin.InitContext{
		StateDir:   t.TempDir(),
		SessionDir: sessDir,
		RunTurn:    runTurn,
		NewSession: func() (string, error) { m, err := store.Create(); return m.ID, err },
		Status: func(sessionID string) (plugin.StatusInfo, error) {
			return plugin.StatusInfo{
				Provider: "testprov", Model: "m1", Thinking: "off", ContextLength: 131072,
				HasSession: sessionID != "", MessageCount: 2, MeasuredTokens: 1234, EstTokens: 999,
			}, nil
		},
	}
	cfg := map[string]any{
		"app_id": "123", "app_secret": "secret",
		"api_base": f.srv.URL, "whitelist": whitelist,
	}
	if err := p.Init(ictx, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("等待网关握手超时")
	}
	return p, f, store
}

// ---------- 全链路 ----------

// 消息进来 → 跑一轮对话 → 被动回复带 msg_id 与 msg_seq。
func TestChatRoundTrip(t *testing.T) {
	runTurn := func(ctx context.Context, sid, input string) (string, error) {
		if !plugin.IsInteractive(ctx) {
			t.Error("QQ 轮次应标记为真人交互")
		}
		return "收到：" + input, nil
	}
	_, f, _ := newInited(t, runTurn, "user1")

	id := f.pushC2C("user1", "你好")
	m := f.expectSend(t)
	if m.openid != "user1" || m.content != "收到：你好" {
		t.Fatalf("回复不符: %+v", m)
	}
	if m.msgID != id || m.seq != 1 {
		t.Fatalf("被动回复应带 msg_id=%s seq=1: %+v", id, m)
	}
}

// 白名单外的用户被拒绝，无任何回复。
func TestWhitelistBlocksStrangers(t *testing.T) {
	_, f, _ := newInited(t, func(context.Context, string, string) (string, error) {
		t.Error("陌生人不该触发对话")
		return "", nil
	}, "user1")
	f.pushC2C("stranger", "在吗")
	f.expectNoSend(t, 500*time.Millisecond)
}

// /help /status /new 命令链路；/new 换绑会话。
func TestCommands(t *testing.T) {
	var turns atomic.Int32
	runTurn := func(_ context.Context, sid, input string) (string, error) {
		turns.Add(1)
		return "ok", nil
	}
	p, f, _ := newInited(t, runTurn, "user1")

	f.pushC2C("user1", "/help")
	if m := f.expectSend(t); !strings.Contains(m.content, "/compact") {
		t.Fatalf("/help 应列出命令: %s", m.content)
	}

	f.pushC2C("user1", "先聊一句")
	f.expectSend(t)
	sid1 := p.binding.get("user1")
	if sid1 == "" {
		t.Fatal("对话后应有会话映射")
	}

	f.pushC2C("user1", "/new")
	if m := f.expectSend(t); !strings.Contains(m.content, "已新建会话") {
		t.Fatalf("/new 回执异常: %s", m.content)
	}
	if sid2 := p.binding.get("user1"); sid2 == sid1 {
		t.Fatal("/new 后应换绑新会话")
	}

	f.pushC2C("user1", "/status")
	if m := f.expectSend(t); !strings.Contains(m.content, "📊 Agent 状态") ||
		!strings.Contains(m.content, "testprov / m1") ||
		!strings.Contains(m.content, "上下文窗口：131,072 tokens") ||
		!strings.Contains(m.content, "实测 1,234 tokens（占用 0.94%）") {
		t.Fatalf("/status 应与 Web UI 同格式: %s", m.content)
	}

	f.pushC2C("user1", "/什么鬼")
	if m := f.expectSend(t); !strings.Contains(m.content, "未知命令") {
		t.Fatalf("未知命令回执异常: %s", m.content)
	}
}

// 确认链路：轮次中发起确认 → QQ 收到请求文案 → /apply 放行、/deny 拒绝。
func TestConfirmFlow(t *testing.T) {
	verdicts := make(chan bool, 2)
	runTurn := func(ctx context.Context, sid, input string) (string, error) {
		confirm, ok := plugin.ConfirmerFrom(ctx)
		if !ok {
			t.Error("QQ 轮次应带确认通道")
			return "", nil
		}
		approved, err := confirm(ctx, plugin.ConfirmRequest{
			Source: "exec_command", Title: "要执行危险命令", Detail: "rm -rf /tmp/x", Reason: "删除文件",
		})
		if err != nil {
			approved = false
		}
		verdicts <- approved
		return "完成", nil
	}
	_, f, _ := newInited(t, runTurn, "user1")

	// 第一轮：/apply
	f.pushC2C("user1", "删掉临时文件")
	if m := f.expectSend(t); !strings.Contains(m.content, "/apply") || !strings.Contains(m.content, "rm -rf") {
		t.Fatalf("确认请求文案异常: %s", m.content)
	}
	f.pushC2C("user1", "/apply")
	if m := f.expectSend(t); !strings.Contains(m.content, "已允许") {
		t.Fatalf("/apply 回执异常: %s", m.content)
	}
	select {
	case ok := <-verdicts:
		if !ok {
			t.Fatal("/apply 应放行")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待确认结果超时")
	}
	f.expectSend(t) // 最终回复「完成」

	// 第二轮：/deny
	f.pushC2C("user1", "再删一次")
	f.expectSend(t) // 确认请求
	f.pushC2C("user1", "/deny")
	f.expectSend(t) // 已拒绝回执
	select {
	case ok := <-verdicts:
		if ok {
			t.Fatal("/deny 应拒绝")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待确认结果超时")
	}
	f.expectSend(t)
}

// 没有等待中的确认时 /apply 得到提示。
func TestApplyWithoutPending(t *testing.T) {
	_, f, _ := newInited(t, func(context.Context, string, string) (string, error) { return "ok", nil }, "user1")
	f.pushC2C("user1", "/apply")
	if m := f.expectSend(t); !strings.Contains(m.content, "没有等待确认") {
		t.Fatalf("回执异常: %s", m.content)
	}
}

// 重复推送同一条消息只处理一次。
func TestDedup(t *testing.T) {
	var turns atomic.Int32
	runTurn := func(context.Context, string, string) (string, error) {
		turns.Add(1)
		return "ok", nil
	}
	p, f, _ := newInited(t, runTurn, "user1")

	id := f.pushC2C("user1", "你好")
	f.expectSend(t)
	// 手动重放同一 id
	p.handleInbound(context.Background(), inbound{openid: "user1", msgID: id, content: "你好"})
	f.expectNoSend(t, 500*time.Millisecond)
	if turns.Load() != 1 {
		t.Fatalf("重复消息不该重复处理，轮次=%d", turns.Load())
	}
}

// ---------- 纯函数 ----------

func TestSplitText(t *testing.T) {
	if parts := splitText("短消息", 1500); len(parts) != 1 || parts[0] != "短消息" {
		t.Fatalf("短消息不该分段: %v", parts)
	}
	long := strings.Repeat("这是一行比较长的内容。\n", 300) // 约 3600 rune
	parts := splitText(long, 1500)
	if len(parts) < 3 {
		t.Fatalf("长文应分成 3 段以上，得到 %d", len(parts))
	}
	for i, part := range parts {
		if !strings.HasPrefix(part, fmt.Sprintf("(%d/%d) ", i+1, len(parts))) {
			t.Fatalf("分段应带标注: %q", part[:20])
		}
		if len([]rune(part)) > 1520 {
			t.Fatalf("分段过长: %d", len([]rune(part)))
		}
	}
}

func TestReplyLimiter(t *testing.T) {
	l := newReplyLimiter()
	for i := 1; i <= replyLimit; i++ {
		ok, seq := l.next("m1")
		if !ok || seq != i {
			t.Fatalf("第 %d 次应放行且 seq=%d，得到 ok=%v seq=%d", i, i, ok, seq)
		}
	}
	if ok, _ := l.next("m1"); ok {
		t.Fatal("超过 4 次应降级")
	}
	if ok, _ := l.next(""); ok {
		t.Fatal("空 msg_id 不可被动回复")
	}
	// 超窗
	l.entries["m2"] = &replyEntry{count: 1, first: time.Now().Add(-2 * time.Hour)}
	if ok, _ := l.next("m2"); ok {
		t.Fatal("超过 60 分钟应降级")
	}
}

func TestCommaPct(t *testing.T) {
	if got := comma(131072); got != "131,072" {
		t.Fatalf("comma = %s", got)
	}
	if got := comma(999); got != "999" {
		t.Fatalf("comma = %s", got)
	}
	if got := pct(1234, 131072); got != "0.94" {
		t.Fatalf("pct = %s", got)
	}
	if got := pct(1, 0); got != "0.00" {
		t.Fatalf("pct 除零 = %s", got)
	}
}

func TestDeduper(t *testing.T) {
	d := newDeduper()
	if d.isDuplicate("a") {
		t.Fatal("首次不算重复")
	}
	if !d.isDuplicate("a") {
		t.Fatal("窗口内应判重")
	}
	d.seen["b"] = time.Now().Add(-10 * time.Minute)
	if d.isDuplicate("b") {
		t.Fatal("超窗后不算重复")
	}
}
