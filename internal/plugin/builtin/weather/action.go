package weather

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"wen/internal/plugin"
)

// actionTest 是设置页上的「测试」按钮。
//
// 它测的是「这个城市名能不能被解析成一个地方」——这件事光看配置项看不出来：
// 地名写法五花八门，重名的地方也多，解析结果只有查一次才知道。所以它读的是
// 配置弹窗里**尚未保存**的草稿值，先验后存，而不是让人先保存一个错的再来发现。
const actionTest = "test"

func (p *Plugin) Actions() []plugin.ActionDef {
	return []plugin.ActionDef{{
		Key:         actionTest,
		Label:       "测试这个城市",
		Description: "用上面填写的城市查一次天气，看看解析成了哪个地方、接口通不通。不改动已保存的配置。",
	}}
}

// StartAction 立即返回，查询在后台进行。
func (p *Plugin) StartAction(ctx context.Context, key string) error {
	if key != actionTest {
		return fmt.Errorf("未知的操作 %q", key)
	}
	s := p.snapshot()
	// 草稿值必须在这里同步取出：ctx 属于那个 HTTP 请求，响应发出后就失效了，
	// 而查询是在后台 goroutine 里跑的。
	location := plugin.ActionValueOr(plugin.ActionValuesFrom(ctx), "location", s.location)
	if location == "" {
		err := fmt.Errorf("请先填写城市再测试")
		p.setActionState(plugin.ActionState{Status: plugin.ActionError, Message: err.Error() + "。"})
		return err
	}
	client := s.client
	if client == nil {
		// 插件启用但城市为空时不会建 client；测试不该因此用不了
		client = &http.Client{Timeout: requestTimeout}
	}

	seq := p.beginAction(plugin.ActionState{
		Status:  plugin.ActionPending,
		Message: "正在查询「" + location + "」的天气…",
	})
	go p.runTest(seq, client, location)
	return nil
}

// runTest 在后台查一次天气并写回结果。ctx 自建：请求的 ctx 到这里已经作废。
func (p *Plugin) runTest(seq uint64, client *http.Client, location string) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	place, rep, err := probe(ctx, client, location)
	if err != nil {
		p.finishAction(seq, plugin.ActionState{
			Status:  plugin.ActionError,
			Message: "查询失败：" + err.Error() + "。",
		})
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "解析到：%s（纬度 %.4f，经度 %.4f）\n", place.Name, place.Lat, place.Lon)
	b.WriteString("当前天气：" + renderReport(rep) + "\n")
	b.WriteString("这个城市可用。保存后会按刷新间隔自动取用。")
	p.finishAction(seq, plugin.ActionState{Status: plugin.ActionDone, Message: b.String()})
}

func (p *Plugin) ActionState(key string) (plugin.ActionState, error) {
	if key != actionTest {
		return plugin.ActionState{}, fmt.Errorf("未知的操作 %q", key)
	}
	p.actMu.Lock()
	defer p.actMu.Unlock()
	if p.actState.Status == "" {
		return plugin.ActionState{Status: plugin.ActionIdle}, nil
	}
	return p.actState, nil
}

// beginAction 记下新一次测试的状态并返回它的序号。
//
// 序号是为「进行中重复触发 = 重新开始」准备的：上一次查询还挂在网络上，用户改了
// 城市又点了一次，先发的那次可能后回来，没有序号的话它会把新结果覆盖掉。
func (p *Plugin) beginAction(st plugin.ActionState) uint64 {
	p.actMu.Lock()
	defer p.actMu.Unlock()
	p.actSeq++
	p.actState = st
	return p.actSeq
}

// finishAction 只在这一次仍是最新的那次时写回结果。
func (p *Plugin) finishAction(seq uint64, st plugin.ActionState) {
	p.actMu.Lock()
	defer p.actMu.Unlock()
	if seq != p.actSeq {
		return
	}
	p.actState = st
}

func (p *Plugin) setActionState(st plugin.ActionState) {
	p.actMu.Lock()
	defer p.actMu.Unlock()
	p.actSeq++
	p.actState = st
}
