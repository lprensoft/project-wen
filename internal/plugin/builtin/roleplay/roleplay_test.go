package roleplay

import (
	"context"
	"strings"
	"testing"

	"wen/internal/llm"
	"wen/internal/plugin"
)

func newTestPlugin(t *testing.T, cfg map[string]any) *Plugin {
	t.Helper()
	p := New()
	if err := p.Init(plugin.InitContext{}, cfg); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSystemPromptComposition(t *testing.T) {
	p := newTestPlugin(t, map[string]any{
		"persona":      "你是林绘，二十四岁，身高一米六二，说话轻但不软。",
		"user_profile": "对方姓沈，身高一米八三，做后端开发。",
	})
	got := p.SystemPrompt()

	for _, want := range []string{
		"[角色设定 · 最高优先级]", "林绘",
		"[对方信息]", "沈",
		"[互动演绎]", "[体型与动作]", "[边界与感受]", "[自然表达]", "[时间一致性]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
	// 不得出现任何身份类提示词（项目硬性约定）
	for _, bad := range []string{"Claude", "GPT", "Anthropic", "OpenAI", "助手", "AI 助手"} {
		if strings.Contains(got, bad) {
			t.Errorf("提示词不应包含身份信息 %q", bad)
		}
	}
}

func TestSystemPromptOmitsEmptySections(t *testing.T) {
	// 设定留空是合法状态：此时只保留表达规则，不注入空的设定段
	p := newTestPlugin(t, nil)
	got := p.SystemPrompt()
	if strings.Contains(got, "[角色设定") || strings.Contains(got, "[对方信息]") {
		t.Errorf("设定为空时不应注入设定段:\n%s", got)
	}
	if !strings.Contains(got, "[自然表达]") {
		t.Error("表达规则应仍然注入")
	}
	// 身高规则依附于双方身高，缺一方就没有作用对象
	if strings.Contains(got, "[体型与动作]") {
		t.Error("双方设定都没有时不应注入身高规则")
	}

	p = newTestPlugin(t, map[string]any{"persona": "只有角色设定"})
	if strings.Contains(p.SystemPrompt(), "[体型与动作]") {
		t.Error("只有一方设定时不应注入身高规则")
	}
}

func TestSectionsCanBeDisabled(t *testing.T) {
	p := newTestPlugin(t, map[string]any{
		"persona":     "角色",
		"interaction": false,
		"humanize":    false,
		"time_rules":  false,
		"boundaries":  false,
	})
	got := p.SystemPrompt()
	for _, bad := range []string{"[互动演绎]", "[自然表达]", "[时间一致性]", "[边界与感受]"} {
		if strings.Contains(got, bad) {
			t.Errorf("关掉后仍注入了 %q", bad)
		}
	}
	if !strings.Contains(got, "[角色设定") {
		t.Error("角色设定不应被这些开关影响")
	}
}

func TestPersonaClippedToBudget(t *testing.T) {
	long := strings.Repeat("设", 4000) // 12000 字节
	p := newTestPlugin(t, map[string]any{
		"persona": long, "user_profile": long, "max_text_bytes": 1024,
	})
	// 每轮全额重发且不参与预算裁剪，必须有硬上限
	s := p.snapshot()
	if len(s.persona)+len(s.userProfile) > 1024 {
		t.Errorf("设定合计 %d 字节，超出上限", len(s.persona)+len(s.userProfile))
	}
	// 截断而不是整段丢弃：砍掉一半仍能扮演，整段没了就不能
	if s.persona == "" {
		t.Error("角色设定不应被整段丢弃")
	}
	if !strings.Contains(s.persona, "截断") {
		t.Error("截断应留下说明")
	}
	// 不能切断 UTF-8 字符
	if !isValidUTF8(s.persona) {
		t.Error("截断处切断了多字节字符")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestPersonaTakesPriorityInBudget(t *testing.T) {
	// 预算不足时角色设定优先：它是这个插件存在的理由
	p := newTestPlugin(t, map[string]any{
		"persona": strings.Repeat("角", 400), "user_profile": strings.Repeat("对", 400),
		"max_text_bytes": 600,
	})
	s := p.snapshot()
	if len(s.persona) == 0 {
		t.Error("角色设定应优先占用预算")
	}
	if len(s.persona)+len(s.userProfile) > 600 {
		t.Errorf("合计 %d 字节超出预算", len(s.persona)+len(s.userProfile))
	}
}

func TestVoiceSamplesInjectedAfterPersona(t *testing.T) {
	p := newTestPlugin(t, map[string]any{
		"persona":       "你是林绘。",
		"voice_samples": "对方：今天累吗？\n林绘：【揉了揉眼睛】还行，就是眼睛有点酸。",
		"user_profile":  "对方姓沈。",
	})
	got := p.SystemPrompt()
	// 样例是「这个角色是谁」的延伸，注入位置紧跟角色设定、在对方信息之前
	iPersona := strings.Index(got, "[角色设定")
	iSamples := strings.Index(got, "[台词样例]")
	iProfile := strings.Index(got, "[对方信息]")
	if iSamples < 0 {
		t.Fatal("样例未注入")
	}
	if !(iPersona < iSamples && iSamples < iProfile) {
		t.Errorf("注入顺序应为 设定 < 样例 < 对方信息，得到 %d %d %d", iPersona, iSamples, iProfile)
	}
	// 防火墙：样例里的人和事不得被当成历史
	if !strings.Contains(got, "不是发生过的历史") {
		t.Error("样例段应声明其内容不是历史")
	}
}

func TestVoiceSamplesAloneStillInjected(t *testing.T) {
	// 只填样例不填设定时，样例本身就是最小的角色设定（声音即人）
	p := newTestPlugin(t, map[string]any{"voice_samples": "对方：在吗\n角色：嗯。"})
	if !strings.Contains(p.SystemPrompt(), "[台词样例]") {
		t.Error("没有角色设定时样例也应注入")
	}
	// 留空则整段不注入
	q := newTestPlugin(t, map[string]any{"persona": "角色"})
	if strings.Contains(q.SystemPrompt(), "[台词样例]") {
		t.Error("样例为空时不应注入该段")
	}
}

func TestVoiceSamplesClippedAtSegmentBoundary(t *testing.T) {
	// 样例排在预算最末：被拦腰砍断的样例是坏范本，超出时按空行整段丢弃
	seg := strings.Repeat("样", 100) // 300 字节一段
	p := newTestPlugin(t, map[string]any{
		"persona":        strings.Repeat("设", 100),
		"voice_samples":  seg + "\n\n" + seg + "\n\n" + seg,
		"max_text_bytes": 800,
	})
	s := p.snapshot()
	if s.persona == "" || strings.Contains(s.persona, "截断") {
		t.Error("预算应先满足角色设定")
	}
	if s.voiceSamples == "" {
		t.Fatal("放得下的段落应保留")
	}
	if got := strings.Count(s.voiceSamples, seg); got != 1 {
		t.Errorf("300 字节预算外的段应整段丢弃，保留了 %d 段", got)
	}
	if !strings.Contains(s.voiceSamples, "已略去") {
		t.Error("丢弃了段落应留下说明")
	}
	// 一段都放不下时整个不要，不做字节级截断
	q := newTestPlugin(t, map[string]any{
		"persona": strings.Repeat("设", 100), "voice_samples": seg + "\n\n" + seg,
		"max_text_bytes": 350,
	})
	if q.snapshot().voiceSamples != "" {
		t.Errorf("一段都放不下时应整个丢弃: %q", q.snapshot().voiceSamples)
	}
}

func TestVoiceSamplesCRLFNormalized(t *testing.T) {
	// textarea 提交的换行可能是 \r\n，按段截断认的是 \n\n 空行
	seg := strings.Repeat("样", 200)
	p := newTestPlugin(t, map[string]any{
		"voice_samples": seg + "\r\n\r\n" + seg, "max_text_bytes": 700,
	})
	s := p.snapshot()
	if strings.Contains(s.voiceSamples, "\r") {
		t.Error("换行未归一")
	}
	if got := strings.Count(s.voiceSamples, seg); got != 1 {
		t.Errorf("\\r\\n 分段应同样按段丢弃，保留了 %d 段", got)
	}
}

func TestRequiresMemoryAndSessionSearch(t *testing.T) {
	got := New().Requires()
	if len(got) != 2 || got[0] != "memory" || got[1] != "session_search" {
		t.Errorf("Requires = %v", got)
	}
}

func TestNoTools(t *testing.T) {
	if got := New().Tools(); len(got) != 0 {
		t.Errorf("本插件不提供工具，得到 %v", got)
	}
}

// 间隔本身由核心每轮给出（<本轮状态> 的「距上一条消息」与历史里的间隔标记），
// 这里只负责说清该拿它怎么办——两处都报一遍只会让措辞慢慢漂移。
func TestTimeRulesTellsWhatToDoWithGap(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"persona": "角色"})
	got := p.SystemPrompt()
	if !strings.Contains(got, "距上一条消息") {
		t.Errorf("时间规则应当接住核心给出的间隔:\n%s", got)
	}
	if !strings.Contains(got, "场景与两人的状态应当相应推进") {
		t.Errorf("应当说明间隔足够长时该怎么演:\n%s", got)
	}
	// 关掉时间约束后整段都不注入
	q := newTestPlugin(t, map[string]any{"time_rules": false})
	if strings.Contains(q.SystemPrompt(), "距上一条消息") {
		t.Error("关掉时间约束后不该出现")
	}
}

func TestOnCompactKeepsLastScene(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"persona": "角色"})
	ev := plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "【靠在窗边，手里端着凉掉的茶】早些年不是这样的。"},
		{Role: llm.RoleUser, Content: "那后来呢"},
		{Role: llm.RoleAssistant, Content: "【转过身，把茶放到桌上】后来就散了。【顿了顿】不提了。"},
	}}

	note, err := p.OnCompact(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	// 摘要会丢掉场景与姿态这类细节，压缩一次场景就断了
	if !strings.Contains(note, "顿了顿") {
		t.Errorf("应取最后一处【】: %q", note)
	}
	if strings.Contains(note, "靠在窗边") {
		t.Errorf("不该取更早的场景: %q", note)
	}
}

func TestOnCompactIgnoresUserBrackets(t *testing.T) {
	// 场景的连续性由扮演方维持，用户消息里的【】不算
	p := newTestPlugin(t, nil)
	note, _ := p.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleUser, Content: "【我也伸手】"},
	}})
	if note != "" {
		t.Errorf("不该采用用户消息里的【】: %q", note)
	}
}

func TestOnCompactSilentWithoutScene(t *testing.T) {
	p := newTestPlugin(t, nil)
	note, _ := p.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "没有演绎的普通回答"},
	}})
	if note != "" {
		t.Errorf("没有场景时不该产生注记: %q", note)
	}

	// 关掉演绎后也不再回写场景
	q := newTestPlugin(t, map[string]any{"interaction": false})
	note, _ = q.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "【有场景】话"},
	}})
	if note != "" {
		t.Errorf("关掉演绎后不该产生注记: %q", note)
	}
}

func TestLastBracketed(t *testing.T) {
	cases := map[string]string{
		"【单个】":         "单个",
		"前【一】中【二】后":    "二",
		"没有括号":         "",
		"【未闭合":         "",
		"未打开】":         "",
		"【 带空格的内容  】话": "带空格的内容",
	}
	for in, want := range cases {
		if got := lastBracketed(in); got != want {
			t.Errorf("lastBracketed(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSceneTruncated(t *testing.T) {
	long := strings.Repeat("景", maxSceneRunes+200)
	note, _ := newTestPlugin(t, nil).OnCompact(context.Background(), plugin.CompactEvent{
		History: []llm.Message{{Role: llm.RoleAssistant, Content: "【" + long + "】"}},
	})
	// 注记会长期留在会话历史里，不能任其膨胀
	if len([]rune(note)) > maxSceneRunes+60 {
		t.Errorf("注记过长: %d 字", len([]rune(note)))
	}
}

func TestInitReenteringIsSafe(t *testing.T) {
	// SetConfig 会在运行时重新 Init，此时可能有正在生成的提示词
	p := newTestPlugin(t, map[string]any{"persona": "初版"})
	if err := p.Init(plugin.InitContext{}, map[string]any{"persona": "改版"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.SystemPrompt(), "改版") {
		t.Error("重新 Init 后配置未生效")
	}
	// 清空设定必须能生效，否则字段永远删不掉
	if err := p.Init(plugin.InitContext{}, map[string]any{"persona": ""}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.SystemPrompt(), "[角色设定") {
		t.Error("清空后仍注入了角色设定")
	}
}

func TestLabelAndPromptUseOppositePerson(t *testing.T) {
	// 界面上那一项叫「我的信息」（填的人是用户），注入给模型的段落写作「对方信息」
	// ——提示词是模型以第一人称读的，人称一统一意思就反了。
	var label, desc string
	for _, f := range New().ConfigFields() {
		if f.Key == "user_profile" {
			label, desc = f.Label, f.Description
		}
	}
	if label != "我的信息" {
		t.Errorf("界面标签 = %q, want 我的信息", label)
	}
	if strings.Contains(desc, "对方") {
		t.Errorf("界面说明应站在用户视角: %q", desc)
	}

	p := newTestPlugin(t, map[string]any{"persona": "角色", "user_profile": "身高一米八三"})
	got := p.SystemPrompt()
	if !strings.Contains(got, "[对方信息]") {
		t.Errorf("注入给模型的段落应写作「对方信息」:\n%s", got)
	}
	if strings.Contains(got, "[我的信息]") {
		t.Errorf("注入的提示词里不能出现「我的信息」，模型会把「我」当成自己:\n%s", got)
	}
}

func TestConfigFieldsDeclareTextType(t *testing.T) {
	want := map[string]string{
		"persona":       plugin.FieldText,
		"user_profile":  plugin.FieldText,
		"voice_samples": plugin.FieldText,
	}
	got := map[string]string{}
	for _, f := range New().ConfigFields() {
		got[f.Key] = f.Type
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("字段 %q 类型 = %q, want %q", k, got[k], v)
		}
	}
	// 声明的字段必须都能通过校验
	if _, err := plugin.NormalizeConfig(New().ConfigFields(), nil); err != nil {
		t.Errorf("默认配置无法通过校验: %v", err)
	}
}

func TestCompactPromptOnlyWithCharacter(t *testing.T) {
	// 只开了表达规则、没有角色：摘要要求没有对象
	if got := newTestPlugin(t, nil).CompactPrompt(context.Background()); got != "" {
		t.Errorf("没有角色时不该追加要求: %q", got)
	}
	for _, cfg := range []map[string]any{
		{"persona": "角色"},
		{"voice_samples": "你：在吗\n她：嗯。"},
	} {
		got := newTestPlugin(t, cfg).CompactPrompt(context.Background())
		if !strings.Contains(got, "关系") || !strings.Contains(got, "称呼") {
			t.Errorf("cfg=%v: 应要求摘要保住关系状态与称呼: %q", cfg, got)
		}
	}
}

func TestOnCompactKeepsRecentVoice(t *testing.T) {
	// 关掉【】演绎，单独看语气样本
	p := newTestPlugin(t, map[string]any{"persona": "角色", "interaction": false})
	note, err := p.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "第一句，早该被挤出去的。"},
		{Role: llm.RoleAssistant, Content: "【靠在窗边】早些年不是这样的。"},
		{Role: llm.RoleUser, Content: "【皱眉】那后来呢"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{Name: "x"}}},
		{Role: llm.RoleAssistant, Content: "【转过身】"},
		{Role: llm.RoleAssistant, Content: "【把茶放到桌上】后来就散了。【顿了顿】不提了。"},
		{Role: llm.RoleAssistant, Content: "你别问了。"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "「早些年不是这样的。」「后来就散了。不提了。」「你别问了。」"
	if !strings.Contains(note, want) {
		t.Errorf("应按时间顺序取最近三句原话、去掉【】、跳过空句:\n%q", note)
	}
	if strings.Contains(note, "第一句") || strings.Contains(note, "皱眉") || strings.Contains(note, "场景演绎") {
		t.Errorf("多余内容混进了注记: %q", note)
	}
}

func TestOnCompactCombinesSceneAndVoice(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"persona": "角色"})
	note, _ := p.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "【转过身，把茶放到桌上】后来就散了。"},
	}})
	if !strings.Contains(note, "场景演绎，后续对话应从此处继续：转过身") || !strings.Contains(note, "同一个声音：「后来就散了。」") {
		t.Errorf("场景与原话应各占一行同时保留:\n%q", note)
	}
	// 没有角色时只剩场景
	q := newTestPlugin(t, nil)
	note, _ = q.OnCompact(context.Background(), plugin.CompactEvent{History: []llm.Message{
		{Role: llm.RoleAssistant, Content: "【抬头】嗯。"},
	}})
	if !strings.Contains(note, "抬头") || strings.Contains(note, "同一个声音") {
		t.Errorf("没有角色时不该附语气样本: %q", note)
	}
}

func TestRecentVoiceTruncated(t *testing.T) {
	long := strings.Repeat("话", maxVoiceRunes+50)
	lines := recentVoice([]llm.Message{{Role: llm.RoleAssistant, Content: long}})
	if len(lines) != 1 || len([]rune(lines[0])) != maxVoiceRunes+1 || !strings.HasSuffix(lines[0], "…") {
		t.Errorf("超长原话应截断并加省略号: len=%d", len([]rune(lines[0])))
	}
}

func TestStripBracketed(t *testing.T) {
	for in, want := range map[string]string{
		"【a】说话【b】": "说话",
		"没有括号":     "没有括号",
		"【没配对的 说话": "【没配对的 说话",
		"说话】多一个":   "说话】多一个",
	} {
		if got := stripBracketed(in); got != want {
			t.Errorf("stripBracketed(%q) = %q, want %q", in, got, want)
		}
	}
}

// 边界记忆的两条配套：当场就存，想起来时对得上。它们随记忆约束一起开关，
// 关掉时不该留下半截。
func TestMemoryRulesCoverBoundaryMemories(t *testing.T) {
	p := newTestPlugin(t, map[string]any{"memory_rules": true})
	got := p.SystemPrompt()
	for _, want := range []string{
		"当场就存成「边界」类记忆",
		"想起一条「边界」类记忆时",
		"用同一个标题修订那条记忆",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("记忆约束缺少边界配套 %q", want)
		}
	}
	// 依赖方向是 relationship 依赖 roleplay，反向引用它的块名会在它关掉时悬空
	if strings.Contains(got, "[关系]") {
		t.Error("roleplay 不应引用 relationship 的块名")
	}

	q := newTestPlugin(t, map[string]any{"memory_rules": false})
	if strings.Contains(q.SystemPrompt(), "「边界」类记忆") {
		t.Error("关掉记忆约束后不该留下边界配套")
	}
}
