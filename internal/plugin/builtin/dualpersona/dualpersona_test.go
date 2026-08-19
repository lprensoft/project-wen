package dualpersona

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"wen/internal/imbot"
	"wen/internal/plugin"
)

func newTestPlugin(t *testing.T, cfg map[string]any) *Plugin {
	t.Helper()
	return newPluginAt(t, t.TempDir(), cfg)
}

// newPluginAt 在指定目录上建插件，便于模拟「重启后仍在同一份状态上」。
func newPluginAt(t *testing.T, dir string, cfg map[string]any) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(plugin.InitContext{StateDir: dir}, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

// decide 走一轮裁决，返回本轮的可见域。
func decide(t *testing.T, p *Plugin, sessionID, input string) plugin.Scope {
	t.Helper()
	sc, err := p.DecideScope(context.Background(), plugin.TurnEvent{SessionID: sessionID, UserInput: input})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func defaultCfg() map[string]any {
	return map[string]any{
		"inner_persona": "你另有一面，说话更直接。",
		"to_inner":      "换个说法\n只有我们两个人的时候",
		"to_outer":      "好了\n说正经的",
	}
}

func TestInitRequiresStateDir(t *testing.T) {
	// 状态一丢，用户下次进来就莫名回到表人格，比不启用更糟
	if err := New().Init(plugin.InitContext{}, nil); err == nil {
		t.Fatal("没有持久化目录时应拒绝启用")
	}
}

func TestDefaultsToOuter(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	sc := decide(t, p, "s1", "你好")
	if sc.Write != personaOuter {
		t.Errorf("新会话应默认表人格，得到 %q", sc.Write)
	}
	if !slices.Equal(sc.Read, []string{personaOuter}) {
		t.Errorf("表人格只读表侧，得到 %v", sc.Read)
	}
	// 里侧内容对表人格不可读，共享内容始终可读
	if sc.CanRead(personaInner) {
		t.Error("表人格不该读得到里侧")
	}
	if !sc.CanRead("") {
		t.Error("共享内容应始终可读")
	}
}

func TestSwitchToInnerTakesEffectSameTurn(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	decide(t, p, "s1", "先聊点别的")

	// 命中的那条消息本身就带新标签落盘，暗号因此不留在表人格的历史里
	sc := decide(t, p, "s1", "换个说法吧")
	if sc.Write != personaInner {
		t.Fatalf("应切到里人格，得到 %q", sc.Write)
	}
	if !sc.CanRead(personaOuter) || !sc.CanRead(personaInner) {
		t.Errorf("里人格应读得到表里两侧: %v", sc.Read)
	}

	// 之后没有触发词也保持在里人格
	if got := decide(t, p, "s1", "普通的一句话"); got.Write != personaInner {
		t.Errorf("状态未保持，得到 %q", got.Write)
	}
}

func TestSwitchBackToOuter(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	decide(t, p, "s1", "换个说法")
	if got := decide(t, p, "s1", "好了，说说明天的安排"); got.Write != personaOuter {
		t.Errorf("应切回表人格，得到 %q", got.Write)
	}
}

func TestBothDirectionsHitFavoursOuter(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	decide(t, p, "s1", "换个说法")
	// 宁可多切回来一次，也不要卡在里人格出不去
	if got := decide(t, p, "s1", "换个说法，好了"); got.Write != personaOuter {
		t.Errorf("同时命中时应回表人格，得到 %q", got.Write)
	}
}

func TestKeywordMatchingDetails(t *testing.T) {
	p := newTestPlugin(t, map[string]any{
		"to_inner": "  换个说法  \n\n\n只有我们两个人的时候\n",
		"to_outer": "好了",
	})
	s := p.snapshot()
	if !slices.Equal(s.toInner, []string{"换个说法", "只有我们两个人的时候"}) {
		t.Errorf("触发词解析不对（应去空白、丢空行）: %v", s.toInner)
	}

	// 大小写不敏感
	q := newTestPlugin(t, map[string]any{"to_inner": "Switch Now", "to_outer": "back"})
	if got := decide(t, q, "s1", "请 switch NOW 吧"); got.Write != personaInner {
		t.Errorf("匹配应不区分大小写，得到 %q", got.Write)
	}
}

func TestMatchModeEquals(t *testing.T) {
	p := newTestPlugin(t, map[string]any{
		"to_inner": "换个说法", "to_outer": "好了", "match_mode": matchEquals,
	})
	if got := decide(t, p, "s1", "我想换个说法来讲这件事"); got.Write != personaOuter {
		t.Errorf("整句相等模式下不该被包含匹配触发，得到 %q", got.Write)
	}
	if got := decide(t, p, "s1", "  换个说法  "); got.Write != personaInner {
		t.Errorf("整条消息就是触发词时应命中，得到 %q", got.Write)
	}
}

func TestEmptyKeywordsNeverSwitch(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"inner_persona": "另一面"})
	for _, input := range []string{"", "   ", "随便说点什么"} {
		if got := decide(t, p, "s1", input); got.Write != personaOuter {
			t.Errorf("没有配置触发词时不该切换（输入 %q）", input)
		}
	}
}

func TestPerSessionStateIsIndependent(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	decide(t, p, "s1", "换个说法")
	// 另一个会话不受影响？——不：新会话继承上一次对话的人格，这是要的行为
	if got := decide(t, p, "s2", "你好"); got.Write != personaInner {
		t.Errorf("新会话应继承上次的人格，得到 %q", got.Write)
	}
	// 但切回旧会话时应按它自己的记录恢复
	decide(t, p, "s2", "好了")
	if got := decide(t, p, "s1", "继续"); got.Write != personaInner {
		t.Errorf("切回旧会话应恢复它自己的人格，得到 %q", got.Write)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	p := newPluginAt(t, dir, defaultCfg())
	decide(t, p, "s1", "换个说法")

	// 重启：状态文件是权威来源，历史推导不可靠（压缩会按可见域重排消息）
	q := newPluginAt(t, dir, defaultCfg())
	if got := decide(t, q, "s1", "继续"); got.Write != personaInner {
		t.Errorf("重启后应恢复里人格，得到 %q", got.Write)
	}
	// 全新会话也继承
	if got := decide(t, q, "brand-new", "你好"); got.Write != personaInner {
		t.Errorf("重启后新建会话应继承上次人格，得到 %q", got.Write)
	}
}

func TestCorruptStateFileDoesNotBlockInit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{ 不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newPluginAt(t, dir, defaultCfg())
	if got := decide(t, p, "s1", "你好"); got.Write != personaOuter {
		t.Errorf("坏状态文件应退回默认人格而不是让插件不可用，得到 %q", got.Write)
	}
}

func TestStateFileTrimmed(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())
	for i := range maxTrackedSessions + 20 {
		decide(t, p, "session-"+strconv.Itoa(i), "你好")
	}

	st := p.snapshot().store
	st.mu.Lock()
	sessions, order := len(st.st.Sessions), len(st.st.Order)
	st.mu.Unlock()
	// 这个文件每轮对话都要重写，不能随会话数无限增长
	if sessions > maxTrackedSessions || order > maxTrackedSessions {
		t.Errorf("状态记录未裁剪: sessions=%d order=%d", sessions, order)
	}
	// 最近的会话必须还在（裁剪只该丢最早的）
	if got := decide(t, p, "session-"+strconv.Itoa(maxTrackedSessions+19), "继续"); got.Write == "" {
		t.Error("最近的会话记录不应被裁掉")
	}
}

func TestTurnPromptOnlyForInner(t *testing.T) {
	p := newTestPlugin(t, defaultCfg())

	// 表人格模式下必须一个字都不注入：提到里人格的存在，就等于让表人格知道了它
	got, err := p.TurnPrompt(context.Background(), plugin.TurnEvent{Scope: scopeFor(personaOuter)})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("表人格模式下不该注入任何内容:\n%s", got)
	}

	got, err = p.TurnPrompt(context.Background(), plugin.TurnEvent{Scope: scopeFor(personaInner)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "另有一面") || !strings.Contains(got, "优先于上文的角色设定") {
		t.Errorf("里人格模式应注入设定并声明优先级:\n%s", got)
	}
}

func TestTurnPromptEmptyWhenNoInnerPersona(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"to_inner": "换", "to_outer": "回"})
	if got, _ := p.TurnPrompt(context.Background(), plugin.TurnEvent{Scope: scopeFor(personaInner)}); got != "" {
		t.Errorf("没有配置里人格设定时不该注入空段:\n%s", got)
	}
}

func TestSystemPromptAlwaysEmpty(t *testing.T) {
	// 设定与人格有关，只能按轮注入；静态注入会让表人格也看到
	if got := newTestPlugin(t, defaultCfg()).SystemPrompt(); got != "" {
		t.Errorf("SystemPrompt 应恒为空:\n%s", got)
	}
}

func TestDeclaredDependenciesAndConflicts(t *testing.T) {
	p := New()
	if got := p.Requires(); !slices.Equal(got, []string{"roleplay"}) {
		t.Errorf("Requires = %v", got)
	}
	// 通用的文件与命令通道能绕过可见域，必须告警
	conflicts := p.Conflicts()
	for _, want := range []string{"exec_command", "read_file"} {
		if !slices.Contains(conflicts, want) {
			t.Errorf("Conflicts 应含 %q，得到 %v", want, conflicts)
		}
	}
}

func TestScopeSurvivesManagerValidation(t *testing.T) {
	// 标签会被拼进持久化目录，核心按插件名的字符集校验，不合规的会被整条作废。
	// 走一遍真实的 Manager 裁决链路，确认本插件给出的标签能过关。
	m := plugin.NewManager(plugin.InitContext{StateDir: t.TempDir()}, "")
	// 配置必须经 PluginConfig 传入：Register 会用它重新 Init，此处直接建的实例会被覆盖
	if err := m.Register(New(), plugin.PluginConfig{Enabled: true, Config: defaultCfg()}); err != nil {
		t.Fatal(err)
	}

	sc := m.DecideScope(context.Background(), plugin.TurnEvent{SessionID: "s1", UserInput: "你好"})
	if sc.Write != personaOuter {
		t.Errorf("表人格标签未通过核心校验: %+v", sc)
	}
	sc = m.DecideScope(context.Background(), plugin.TurnEvent{SessionID: "s1", UserInput: "换个说法"})
	if sc.Write != personaInner || !sc.CanRead(personaOuter) {
		t.Errorf("里人格标签未通过核心校验: %+v", sc)
	}
}

func TestConfigFieldsValidate(t *testing.T) {
	fields := New().ConfigFields()
	if _, err := plugin.NormalizeConfig(fields, nil); err != nil {
		t.Fatalf("默认配置无法通过校验: %v", err)
	}
	// 三个多行字段都必须是 text，否则界面上会渲染成单行输入
	want := map[string]string{
		"inner_persona": plugin.FieldText,
		"to_inner":      plugin.FieldText,
		"to_outer":      plugin.FieldText,
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f.Key] = f.Type
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("字段 %q 类型 = %q, want %q", k, got[k], v)
		}
	}
	// 多行触发词能存下空串（清空必须真的能清掉）
	out, err := plugin.NormalizeConfig(fields, map[string]any{"to_inner": "", "to_outer": ""})
	if err != nil {
		t.Fatal(err)
	}
	if out["to_inner"] != "" {
		t.Errorf("触发词应能清空，得到 %v", out["to_inner"])
	}
}

func TestKeywordsSurviveCRLF(t *testing.T) {
	// 界面提交的多行文本可能带 \r\n，核心已统一，这里确认解析层也不会被残留的 \r 坑到
	p := newTestPlugin(t, map[string]any{"to_inner": "换个说法\r\n另一个词", "to_outer": "好了"})
	if got := p.snapshot().toInner; !slices.Equal(got, []string{"换个说法", "另一个词"}) {
		t.Errorf("触发词解析被 \\r 干扰: %q", got)
	}
	if got := decide(t, p, "s1", "换个说法"); got.Write != personaInner {
		t.Errorf("应命中，得到 %q", got.Write)
	}
}

func TestInitReenteringIsSafe(t *testing.T) {
	// SetConfig 会在运行时重新 Init，此时可能有 in-flight 的裁决在读这些字段
	dir := t.TempDir()
	p := newPluginAt(t, dir, defaultCfg())
	decide(t, p, "s1", "换个说法")

	if err := p.Init(plugin.InitContext{StateDir: dir}, map[string]any{
		"inner_persona": "改过的设定", "to_inner": "新暗号", "to_outer": "回来",
	}); err != nil {
		t.Fatal(err)
	}
	// 重新 Init 不该丢掉已经记住的人格状态
	if got := decide(t, p, "s1", "继续"); got.Write != personaInner {
		t.Errorf("改配置后人格状态丢失，得到 %q", got.Write)
	}
	got, _ := p.TurnPrompt(context.Background(), plugin.TurnEvent{Scope: scopeFor(personaInner)})
	if !strings.Contains(got, "改过的设定") {
		t.Errorf("新设定未生效:\n%s", got)
	}
	if got := decide(t, p, "s1", "回来吧"); got.Write != personaOuter {
		t.Errorf("新触发词未生效，得到 %q", got.Write)
	}
}

// ---- 分通道 ----

// splitCfg 是打开了分通道的配置：表人格在 a_bot，里人格在 b_bot。
func splitCfg() map[string]any {
	cfg := defaultCfg()
	cfg["split_channels"] = true
	cfg["outer_channel"] = "a_bot"
	cfg["inner_channel"] = "b_bot"
	return cfg
}

// 每个用例自己收拾包级路由：它是进程级单例，留着会污染后面的用例。
func clearRouter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { imbot.SetRouter(nil) })
}

func TestChannelOptionsListAllDeclaredChannels(t *testing.T) {
	imbot.Declare("a_bot", "甲")
	opts := channelOptions()
	if len(opts) < 2 || opts[0].Value != "" {
		t.Fatalf("第一项应是「不指定」，得到 %+v", opts)
	}
	var found *plugin.ConfigOption
	for i := range opts {
		if opts[i].Value == "a_bot" {
			found = &opts[i]
		}
	}
	if found == nil {
		t.Fatalf("已声明的通道应出现在候选里：%+v", opts)
	}
	// 没启动的通道要标出来，否则选了个连不上的看不出问题
	if !strings.Contains(found.Label, "未启用") {
		t.Errorf("未启动的通道应标注出来，得到 %q", found.Label)
	}
}

// 候选是「已声明」而非「已启用」，所以关掉一条通道不会让这里的配置变得非法。
func TestConfigWithADisabledChannelStillValidates(t *testing.T) {
	imbot.Declare("a_bot", "甲")
	imbot.Declare("b_bot", "乙")
	if _, err := plugin.NormalizeConfig(New().ConfigFields(), splitCfg()); err != nil {
		t.Fatalf("选了未启动的通道也应能保存配置: %v", err)
	}
}

func TestRouteFollowsPersona(t *testing.T) {
	clearRouter(t)
	p := newTestPlugin(t, splitCfg())

	if got := p.route("s1"); got != "a_bot" {
		t.Errorf("默认表人格应发往 a_bot，得到 %q", got)
	}
	// 说出暗号的那一轮就该转投：裁决在轮次开头已经写下新人格
	decide(t, p, "s1", "只有我们两个人的时候")
	if got := p.route("s1"); got != "b_bot" {
		t.Errorf("切到里人格后应发往 b_bot，得到 %q", got)
	}
	decide(t, p, "s1", "好了")
	if got := p.route("s1"); got != "a_bot" {
		t.Errorf("切回表人格后应发往 a_bot，得到 %q", got)
	}
	// 另一个会话各算各的
	if got := p.route("s2"); got != "a_bot" {
		t.Errorf("新会话继承上一次的人格（表），应发往 a_bot，得到 %q", got)
	}
}

func TestRouterInstalledOnlyWhenConfigured(t *testing.T) {
	clearRouter(t)
	dir := t.TempDir()

	// 开关关着：不装路由
	newPluginAt(t, dir, defaultCfg())
	if imbot.Target("s1") != "" {
		t.Error("未开启分通道时不该安装路由")
	}

	// 开关开着但一条通道都没选：装了也只会答空串，不如不装
	cfg := defaultCfg()
	cfg["split_channels"] = true
	newPluginAt(t, dir, cfg)
	if imbot.Target("s1") != "" {
		t.Error("两个通道都没选时不该安装路由")
	}

	// 配齐了才装
	p := newPluginAt(t, dir, splitCfg())
	if got := imbot.Target("s1"); got != "a_bot" {
		t.Errorf("应安装路由并答出表人格的通道，得到 %q", got)
	}

	// 重新 Init 关掉开关：路由要跟着卸掉
	if err := p.Init(plugin.InitContext{StateDir: dir}, defaultCfg()); err != nil {
		t.Fatal(err)
	}
	if imbot.Target("s1") != "" {
		t.Error("关掉分通道后应卸掉路由")
	}
}

func TestStopClearsRouter(t *testing.T) {
	clearRouter(t)
	p := newTestPlugin(t, splitCfg())
	if imbot.Target("s1") == "" {
		t.Fatal("前置条件：路由应已安装")
	}
	p.Stop()
	if imbot.Target("s1") != "" {
		t.Error("插件停止后不该再有通道路由生效")
	}
}
