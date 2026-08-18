package weather

import (
	"context"
	"strings"
	"testing"
	"time"

	"wen/internal/plugin"
)

// 测试按钮读的是配置弹窗里尚未保存的草稿值：先验后存正是它存在的理由。
func TestActionUsesDraftLocation(t *testing.T) {
	_, asked := countingStub(t)
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "已保存的城市"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := plugin.WithActionValues(context.Background(), map[string]any{"location": "苏州"})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	st := waitAction(t, p)
	if st.Status != plugin.ActionDone {
		t.Fatalf("测试应当成功，实际 %+v", st)
	}
	if *asked != "苏州" {
		t.Errorf("查询的是 %q，应当用草稿里的城市", *asked)
	}
	if !strings.Contains(st.Message, "解析到") || !strings.Contains(st.Message, "小雨") {
		t.Errorf("结果应当同时给出解析到的地方与当时的天气，实际 %q", st.Message)
	}
}

// 草稿里没填时回落到已保存的城市。
func TestActionFallsBackToSavedLocation(t *testing.T) {
	_, asked := countingStub(t)
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "杭州"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.StartAction(context.Background(), actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	if st := waitAction(t, p); st.Status != plugin.ActionDone {
		t.Fatalf("测试应当成功，实际 %+v", st)
	}
	if *asked != "杭州" {
		t.Errorf("查询的是 %q，应当回落到已保存的城市", *asked)
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
	ctx := plugin.WithActionValues(context.Background(), map[string]any{"location": "并不存在的地方"})
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

// 测试不该动到正在使用的观测缓存：它测的是一个还没保存的城市。
func TestActionDoesNotTouchLiveCache(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	p.cur, p.curOK = Report{Place: "北京", Condition: "晴", TempC: 25, Fetched: time.Now()}, true

	ctx := plugin.WithActionValues(context.Background(), map[string]any{"location": "杭州"})
	if err := p.StartAction(ctx, actionTest); err != nil {
		t.Fatalf("StartAction: %v", err)
	}
	waitAction(t, p)

	if rep, _, _ := p.lastReport(); rep.Place != "北京" {
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
