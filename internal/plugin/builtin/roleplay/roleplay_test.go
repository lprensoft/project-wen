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
		"[互动演绎]", "[体型与动作]", "[自然表达]", "[时间一致性]",
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
	})
	got := p.SystemPrompt()
	for _, bad := range []string{"[互动演绎]", "[自然表达]", "[时间一致性]"} {
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
		"persona":      plugin.FieldText,
		"user_profile": plugin.FieldText,
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
