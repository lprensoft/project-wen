package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wen/internal/plugin"
)

// askedStub 记录每次地名解析问到的城市，并按城市返回不同的解析结果。
func askedStub(t *testing.T) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			name := r.URL.Query().Get("name")
			asked = append(asked, name)
			_, _ = w.Write([]byte(`{"results":[{"name":"` + name + `","latitude":30.1,"longitude":120.1,"country":"中国"}]}`))
			return
		}
		_, _ = w.Write([]byte(fcOK))
	}))
	pointAt(t, srv.URL)
	t.Cleanup(srv.Close)
	return &asked
}

// 测试按钮读的是配置弹窗里尚未保存的草稿值：先验后存正是它存在的理由。
func TestActionUsesDraftLocations(t *testing.T) {
	asked := askedStub(t)
	p := New()
	defer p.Stop()
	// 直接写已保存的配置，不走 Init：Init 会起后台循环，它自己那次取数会混进计数里
	p.personaLoc, p.sameCity = "已保存的城市", true

	ctx := plugin.WithActionValues(context.Background(), map[string]any{
		"persona_location": "苏州", "same_city": false, "user_location": "南京",
	})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	st := waitAction(t, p)
	if st.Status != plugin.ActionDone {
		t.Fatalf("测试应当成功，实际 %+v", st)
	}
	if len(*asked) != 2 || (*asked)[0] != "苏州" || (*asked)[1] != "南京" {
		t.Errorf("查询的城市 = %v，应当用草稿里的两处", *asked)
	}
	for _, want := range []string{"角色所在", "我所在", "两处都可用"} {
		if !strings.Contains(st.Message, want) {
			t.Errorf("结果里缺少 %q：%s", want, st.Message)
		}
	}
}

// 同城时只查一次：这正是这个开关存在的理由。
func TestActionSameCityQueriesOnce(t *testing.T) {
	asked := askedStub(t)
	p := New()
	defer p.Stop()

	ctx := plugin.WithActionValues(context.Background(), map[string]any{
		"persona_location": "杭州", "same_city": true, "user_location": "上海",
	})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	st := waitAction(t, p)
	if st.Status != plugin.ActionDone {
		t.Fatalf("测试应当成功，实际 %+v", st)
	}
	if len(*asked) != 1 || (*asked)[0] != "杭州" {
		t.Errorf("同城时只该查一次角色的城市，实际 %v", *asked)
	}
	if !strings.Contains(st.Message, "角色与我同在") {
		t.Errorf("同城时的措辞不对：%s", st.Message)
	}
}

// 两处填了同一个地方也只查一次。
func TestActionIdenticalCitiesQueryOnce(t *testing.T) {
	asked := askedStub(t)
	p := New()
	defer p.Stop()

	ctx := plugin.WithActionValues(context.Background(), map[string]any{
		"persona_location": "杭州", "same_city": false, "user_location": "杭州",
	})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	waitAction(t, p)
	if len(*asked) != 1 {
		t.Errorf("同一个城市不该查两次，实际 %v", *asked)
	}
}

// 草稿里没带时回落到已保存的配置。
func TestActionFallsBackToSavedConfig(t *testing.T) {
	asked := askedStub(t)
	p := New()
	defer p.Stop()
	p.personaLoc, p.sameCity = "杭州", true // 同上：绕开 Init 的后台循环
	if err := p.StartAction(context.Background(), actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	if st := waitAction(t, p); st.Status != plugin.ActionDone {
		t.Fatalf("测试应当成功，实际 %+v", st)
	}
	if len(*asked) != 1 || (*asked)[0] != "杭州" {
		t.Errorf("应当回落到已保存的城市，实际 %v", *asked)
	}
}

func TestActionWithoutLocation(t *testing.T) {
	p := New()
	defer p.Stop()
	if err := p.StartAction(context.Background(), actionTest); err == nil {
		t.Fatal("没有城市可测时应当直接报错")
	}
	st, _ := p.ActionState(actionTest)
	if st.Status != plugin.ActionError {
		t.Errorf("状态 = %+v", st)
	}
}

func TestActionUnknownKey(t *testing.T) {
	p := New()
	defer p.Stop()
	if err := p.StartAction(context.Background(), "nope"); err == nil {
		t.Fatal("未知操作应当报错")
	}
	if _, err := p.ActionState("nope"); err == nil {
		t.Fatal("未知操作的状态查询应当报错")
	}
}

func TestActionStateStartsIdle(t *testing.T) {
	st, err := New().ActionState(actionTest)
	if err != nil {
		t.Fatalf("ActionState: %v", err)
	}
	if st.Status != plugin.ActionIdle {
		t.Errorf("初始状态 = %q", st.Status)
	}
}

// 地名查不到时，测试要把失败原因说清楚，而不是含糊地失败。
func TestActionReportsGeocodeFailure(t *testing.T) {
	stubServer(t, `{}`, fcOK)
	p := New()
	defer p.Stop()
	ctx := plugin.WithActionValues(context.Background(), map[string]any{"persona_location": "并不存在的地方"})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	st := waitAction(t, p)
	if st.Status != plugin.ActionError {
		t.Fatalf("应当失败，实际 %+v", st)
	}
	if !strings.Contains(st.Message, "并不存在的地方") {
		t.Errorf("失败说明应当点出是哪个地名，实际 %q", st.Message)
	}
}

// 一处失败不跳过另一处：一次点击就该把两处的结论都给出来。
func TestActionReportsBothWhenOneFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			if r.URL.Query().Get("name") == "坏地名" {
				_, _ = w.Write([]byte(`{}`)) // 查不到
				return
			}
			_, _ = w.Write([]byte(geoOK))
			return
		}
		_, _ = w.Write([]byte(fcOK))
	}))
	defer srv.Close()
	pointAt(t, srv.URL)

	p := New()
	defer p.Stop()
	ctx := plugin.WithActionValues(context.Background(), map[string]any{
		"persona_location": "坏地名", "same_city": false, "user_location": "上海",
	})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	st := waitAction(t, p)
	if st.Status != plugin.ActionError {
		t.Fatalf("有一处失败就该报错，实际 %+v", st)
	}
	if !strings.Contains(st.Message, "查询失败") || !strings.Contains(st.Message, "我所在") {
		t.Errorf("两处的结论都该给出来，实际 %q", st.Message)
	}
}

// 测试不该动到正在使用的观测缓存：它测的是还没保存的城市。
func TestActionDoesNotTouchLiveCache(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	setObs(p, "北京", report("北京", "晴", 25, 0))

	ctx := plugin.WithActionValues(context.Background(), map[string]any{"persona_location": "杭州"})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	waitAction(t, p)

	if rep, _, _ := p.lastReport("北京"); rep.Condition != "晴" {
		t.Errorf("测试污染了正在使用的观测：%+v", rep)
	}
}

// 进行中再点一次 = 重新开始：先发的那次即使后回来，也不能盖掉新一次的结果。
func TestStaleActionResultIsDiscarded(t *testing.T) {
	p := New()
	defer p.Stop()
	first := p.beginAction(plugin.ActionState{Status: plugin.ActionPending})
	second := p.beginAction(plugin.ActionState{Status: plugin.ActionPending, Message: "第二次"})

	p.finishAction(first, plugin.ActionState{Status: plugin.ActionDone, Message: "第一次的结果"})
	if st, _ := p.ActionState(actionTest); st.Message != "第二次" {
		t.Errorf("旧的一次不该写回结果，实际 %+v", st)
	}

	p.finishAction(second, plugin.ActionState{Status: plugin.ActionDone, Message: "第二次的结果"})
	if st, _ := p.ActionState(actionTest); st.Message != "第二次的结果" {
		t.Errorf("最新一次应当写回，实际 %+v", st)
	}
}

func waitAction(t *testing.T, p *Plugin) plugin.ActionState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := p.ActionState(actionTest)
		if err != nil {
			t.Fatalf("ActionState: %v", err)
		}
		if st.Status == plugin.ActionDone || st.Status == plugin.ActionError {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待测试结果超时")
	return plugin.ActionState{}
}
