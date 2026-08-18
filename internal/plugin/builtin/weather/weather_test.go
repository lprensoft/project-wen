package weather

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"wen/internal/plugin"
)

func TestRenderReport(t *testing.T) {
	got := renderReport(Report{
		Place: "杭州 · 浙江省 · 中国", Condition: "小雨",
		TempC: 18.4, FeelsC: 16.9, Humidity: 82, WindKmh: 9.3,
	})
	want := "杭州 · 浙江省 · 中国：小雨，18℃（体感 17℃），湿度 82%，风速 9 km/h。"
	if got != want {
		t.Errorf("渲染 = %q\n期望 = %q", got, want)
	}
}

// 体感与气温差不到一度时不写体感：一行里两个一样的数字只是噪声。
func TestRenderReportOmitsCloseApparent(t *testing.T) {
	got := renderReport(Report{Place: "杭州", Condition: "晴", TempC: 18, FeelsC: 18.2, Humidity: 40, WindKmh: 3})
	if strings.Contains(got, "体感") {
		t.Errorf("体感与气温接近时不应写出来: %q", got)
	}
}

// ---------- 配置 ----------

func TestInitRejectsStaleShorterThanRefresh(t *testing.T) {
	p := New()
	defer p.Stop()
	err := p.Init(plugin.InitContext{}, map[string]any{
		"location": "杭州", "refresh_minutes": 60, "stale_minutes": 30,
	})
	if err == nil {
		t.Fatal("过期时限小于刷新间隔时应当拒绝：那样刚取到的天气也立刻过期，等于永不注入")
	}
}

func TestInitDefaultsAreConsistent(t *testing.T) {
	var refresh, stale int
	for _, f := range New().ConfigFields() {
		switch f.Key {
		case "refresh_minutes":
			refresh, _ = f.Default.(int)
		case "stale_minutes":
			stale, _ = f.Default.(int)
		}
	}
	if refresh == 0 || stale == 0 {
		t.Fatal("刷新间隔与过期时限都应当有默认值")
	}
	if stale < refresh {
		t.Fatalf("默认值自相矛盾：过期时限 %d < 刷新间隔 %d", stale, refresh)
	}
}

// 没填城市时不起后台循环，也不注入任何东西——包括那段讲天气怎么用的规则。
func TestNoLocationInjectsNothing(t *testing.T) {
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": ""}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := p.SystemPrompt(); got != "" {
		t.Errorf("未设置城市时不应注入规则，实际 %q", got)
	}
	out, err := p.TurnPrompt(context.Background(), plugin.TurnEvent{})
	if err != nil || out != "" {
		t.Errorf("未设置城市时不应注入天气，实际 %q / %v", out, err)
	}
}

func TestSystemPromptWithLocation(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "杭州"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !strings.Contains(p.SystemPrompt(), "[天气感知]") {
		t.Error("设置了城市后应当注入使用规则")
	}
}

// ---------- 过期 ----------

func TestFreshRespectsStale(t *testing.T) {
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{
		"location": "", "refresh_minutes": 10, "stale_minutes": 60,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	now := time.Now()
	p.cur, p.curOK = Report{Condition: "晴", Fetched: now.Add(-30 * time.Minute)}, true
	if _, ok := p.fresh(now); !ok {
		t.Error("30 分钟前的数据在 60 分钟时限内，应当可用")
	}
	p.cur.Fetched = now.Add(-90 * time.Minute)
	if _, ok := p.fresh(now); ok {
		t.Error("超过时限的数据不该注入：让角色不知道天气，好过把旧天气当成现在的")
	}
}

func TestTurnPromptStaleInjectsNothing(t *testing.T) {
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "", "stale_minutes": 60}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p.cur, p.curOK = Report{Place: "杭州", Condition: "晴", TempC: 20, Fetched: time.Now().Add(-3 * time.Hour)}, true
	out, err := p.TurnPrompt(context.Background(), plugin.TurnEvent{})
	if err != nil {
		t.Fatalf("TurnPrompt: %v", err)
	}
	if out != "" {
		t.Errorf("过期数据不应注入，实际 %q", out)
	}

	p.cur.Fetched = time.Now()
	out, _ = p.TurnPrompt(context.Background(), plugin.TurnEvent{})
	if !strings.Contains(out, "[当前天气]") || !strings.Contains(out, "杭州") {
		t.Errorf("新鲜数据应当注入，实际 %q", out)
	}
}

// ---------- 后台刷新与缓存 ----------

func TestRefreshOncePopulatesCache(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	p.refreshOnce(context.Background(), http.DefaultClient, "杭州")

	rep, ok, _ := p.lastReport()
	if !ok {
		t.Fatal("刷新成功后应当有观测")
	}
	if rep.Condition != "小雨" || rep.Place != "杭州 · 浙江省 · 中国" {
		t.Errorf("观测 = %+v", rep)
	}
}

// 地名解析结果按城市缓存：它几乎不变，不该每次取天气都重解析一遍。
func TestGeocodeResultIsCached(t *testing.T) {
	hits, _ := countingStub(t)
	p := New()
	defer p.Stop()
	for i := 0; i < 3; i++ {
		p.refreshOnce(context.Background(), http.DefaultClient, "杭州")
	}
	if *hits != 1 {
		t.Errorf("地名解析发生了 %d 次，应当只有 1 次", *hits)
	}
}

// 取不到新数据时保留上一份，由过期时限决定它还能不能用——而不是当场清空。
func TestRefreshFailureKeepsLastReport(t *testing.T) {
	stubServer(t, geoOK, "")
	p := New()
	defer p.Stop()
	p.cur, p.curOK = Report{Place: "杭州", Condition: "晴", TempC: 20, Fetched: time.Now()}, true

	p.refreshOnce(context.Background(), http.DefaultClient, "杭州")

	rep, ok, lastErr := p.lastReport()
	if !ok || rep.Condition != "晴" {
		t.Error("取数失败不该丢掉上一份观测")
	}
	if lastErr == "" {
		t.Error("失败原因应当记下来，状态行要靠它解释为什么没有天气")
	}
}

// 改配置时重新 Init 必须能停掉旧循环：两套定时器并行会双倍打接口。
func TestInitIsReentrant(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	for i := 0; i < 3; i++ {
		if err := p.Init(plugin.InitContext{}, map[string]any{"location": "杭州"}); err != nil {
			t.Fatalf("第 %d 次 Init: %v", i, err)
		}
	}
	p.Stop()
	p.Stop() // 重复 Stop 不应 panic
}

// 换了城市要丢掉旧观测；只改刷新间隔则保留——那份数据仍然是对的。
func TestCacheClearedOnlyWhenLocationChanges(t *testing.T) {
	stubServer(t, geoOK, fcOK)
	p := New()
	defer p.Stop()
	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "杭州"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p.refreshOnce(context.Background(), http.DefaultClient, "杭州")

	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "杭州", "refresh_minutes": 20}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, ok, _ := p.lastReport(); !ok {
		t.Error("只改刷新间隔不该丢掉已有观测")
	}

	if err := p.Init(plugin.InitContext{}, map[string]any{"location": "北京"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	p.dataMu.RLock()
	resolved := p.placeOK
	p.dataMu.RUnlock()
	if resolved {
		t.Error("换了城市后旧的地名解析结果必须作废")
	}
}

// ---------- 契约 ----------

func TestPluginContract(t *testing.T) {
	p := New()
	if p.Name() != "weather" {
		t.Errorf("插件名 = %q", p.Name())
	}
	if p.Category() != plugin.CategoryPersona {
		t.Errorf("分组 = %q：注入型的环境状态属于角色演绎，不是基础工具", p.Category())
	}
	if len(p.Requires()) != 1 || p.Requires()[0] != "roleplay" {
		t.Errorf("依赖 = %v：没有角色，一行天气只是上下文里的噪声", p.Requires())
	}
	if len(p.Tools()) != 0 {
		t.Error("本插件不提供工具：它注入的是环境状态，不是一个可调用的能力")
	}
	// Init 不发网络请求：启动时断网不该让插件启用失败
	var _ plugin.Stoppable = p
	var _ plugin.TurnPrompter = p
	var _ plugin.Actionable = p
}
