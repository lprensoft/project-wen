package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"wen/internal/modelcfg"
	"wen/internal/plugin"
)

func TestDialAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "127.0.0.1:8080",
		// 「所有网卡」的写法不能拿去连接，要换成回环——顺带吃到回环免认证
		"0.0.0.0:8080": "127.0.0.1:8080",
		"[::]:8080":    "127.0.0.1:8080",
		":8080":        "127.0.0.1:8080",
		"10.0.0.5:80":  "10.0.0.5:80",
	}
	for in, want := range cases {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// 模型列表在 CLI 里以文本编辑，只能改 id 与显示名。已存在模型上的参数覆盖
// 必须原样带过来——否则一次无关的编辑就会把它们悄悄清空。
func TestTextToModelsKeepsOverrides(t *testing.T) {
	ctx := 32000
	prev := []modelcfg.Model{
		{ID: "a", Name: "甲", ContextLength: &ctx},
		{ID: "b", Name: "乙"},
	}
	got := textToModels("a | 甲改名\nc", prev)

	if len(got) != 2 {
		t.Fatalf("解析出 %d 条，want 2: %+v", len(got), got)
	}
	if got[0].ID != "a" || got[0].Name != "甲改名" {
		t.Errorf("首条 = %+v", got[0])
	}
	if got[0].ContextLength == nil || *got[0].ContextLength != ctx {
		t.Error("参数覆盖丢失")
	}
	if got[1].ID != "c" || got[1].ContextLength != nil {
		t.Errorf("新增条目 = %+v，不应带上任何覆盖", got[1])
	}
	// b 被删掉了，它的覆盖也该一并消失
	for _, m := range got {
		if m.ID == "b" {
			t.Error("已删除的模型仍在")
		}
	}
}

func TestModelsToTextRoundTrip(t *testing.T) {
	in := []modelcfg.Model{{ID: "x"}, {ID: "y", Name: "显示名"}}
	text := modelsToText(in)
	if text != "x\ny | 显示名" {
		t.Fatalf("渲染结果 = %q", text)
	}
	if got := textToModels(text, in); len(got) != 2 || got[1].Name != "显示名" {
		t.Errorf("往返后 = %+v", got)
	}
}

func TestCacheTriState(t *testing.T) {
	if cacheToString(nil) != "" || stringToCache("") != nil {
		t.Error("未设置状态没有原样往返")
	}
	on := stringToCache("on")
	if on == nil || !*on || cacheToString(on) != "on" {
		t.Error("开启状态往返错误")
	}
	off := stringToCache("off")
	if off == nil || *off || cacheToString(off) != "off" {
		t.Error("关闭状态往返错误")
	}
}

// 在线模式下取到的整数会经 JSON 变成 float64，直接 %v 会渲染成 1e+06。
func TestScalarString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(1000000), "1000000"},
		{float64(3600), "3600"},
		{int(42), "42"},
		{"文本", "文本"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := scalarString(c.in); got != c.want {
			t.Errorf("scalarString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBindingValues(t *testing.T) {
	boolField := plugin.ConfigField{Key: "on", Type: plugin.FieldBool, Default: false}
	bd := newBinding(boolField, true)
	if bd.value() != true {
		t.Error("布尔值未按原类型提交")
	}

	intField := plugin.ConfigField{Key: "n", Type: plugin.FieldInt, Default: 3}
	bd = newBinding(intField, float64(7))
	if bd.s != "7" {
		t.Errorf("整数初值 = %q, want \"7\"", bd.s)
	}
	// 整数以字符串提交，交给服务端的 NormalizeConfig 判定范围；空串 = 用默认值
	bd.s = "  "
	if bd.value() != "" {
		t.Errorf("空输入 = %q，应归一成空串以表示用默认值", bd.value())
	}

	textField := plugin.ConfigField{Key: "t", Type: plugin.FieldText, Default: ""}
	bd = newBinding(textField, "第一行\n第二行")
	// 文本字段的空串是合法取值，不能被当成「用默认值」而修剪掉
	bd.s = ""
	if bd.value() != "" {
		t.Error("文本字段的空串未原样保留")
	}
}

// onlineBackend 要打对路径、带上同源标记，并把服务端的错误原文交出来。
func TestOnlineBackendRequests(t *testing.T) {
	var gotPath, gotMethod, gotOrigin string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotOrigin = r.Header.Get("Origin")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		if r.URL.Path == "/api/plugins/broken/config" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "配置项 \"上限\" 不能大于 10"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	b := &onlineBackend{c: &client{base: srv.URL, http: srv.Client()}, addr: "x"}

	if err := b.setPluginConfig("memory", map[string]any{"size": "5"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/plugins/memory/config" || gotMethod != http.MethodPut {
		t.Errorf("请求 = %s %s", gotMethod, gotPath)
	}
	if gotOrigin != srv.URL {
		t.Errorf("Origin = %q, want %q（服务端会拒绝不同源的写请求）", gotOrigin, srv.URL)
	}
	if cfg, ok := gotBody["config"].(map[string]any); !ok || cfg["size"] != "5" {
		t.Errorf("请求体 = %+v", gotBody)
	}

	err := b.setPluginConfig("broken", map[string]any{"上限": "99"})
	if err == nil || !strings.Contains(err.Error(), "不能大于 10") {
		t.Errorf("错误未原样交出: %v", err)
	}
}

// 列表项必须严格占一行：huh 的视口按行算高度，一旦某项折行，光标往下挪一格
// 就会滚动好几行，表现为「刚进来按一下方向键，第一项就没了」。
func TestPluginOptionsFitOneLine(t *testing.T) {
	list := []plugin.Status{
		{Name: "read_file", Category: "基础工具", Enabled: true,
			Description: "读取本地文本文件内容"},
		{Name: "exec_command", Category: "基础工具", Enabled: true,
			Description: "在工作目录下执行 shell 命令，危险操作先由用户确认"},
		{Name: "dual_persona", Category: "角色演绎", Enabled: false,
			Description: "表里两套人格：里人格的对话与记忆对表人格不可见，由触发词在两者之间切换",
			Unmet:       []string{"roleplay"}},
	}

	opts := pluginOptions(list)
	if len(opts) != len(list)+1 { // 末尾是「← 返回」
		t.Fatalf("选项数 = %d, want %d", len(opts), len(list)+1)
	}

	limit := labelWidth()
	for _, o := range opts {
		if strings.Contains(o.Key, "\n") {
			t.Errorf("选项含换行: %q", o.Key)
		}
		if w := runewidth.StringWidth(o.Key); w > limit {
			t.Errorf("选项宽 %d 列，超过上限 %d: %q", w, limit, o.Key)
		}
	}

	// 插件名要对齐成一列：同组内两项的名字应从同一列开始
	first, second := opts[0].Key, opts[1].Key
	if runewidth.StringWidth(first[:strings.Index(first, "read_file")]) !=
		runewidth.StringWidth(second[:strings.Index(second, "exec_command")]) {
		t.Errorf("插件名未对齐:\n  %q\n  %q", first, second)
	}

	// 依赖未满足要在列表里就看得见，不必进详情才知道为什么开不起来
	if !strings.Contains(opts[2].Key, "roleplay") {
		t.Errorf("未提示未满足的依赖: %q", opts[2].Key)
	}
}

func TestFitTruncatesByDisplayWidth(t *testing.T) {
	long := strings.Repeat("中文", 200)
	got := fit(long)
	if w := runewidth.StringWidth(got); w > labelWidth() {
		t.Errorf("截断后仍宽 %d 列，上限 %d", w, labelWidth())
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("截断后应以省略号收尾")
	}
	if short := fit("短"); short != "短" {
		t.Errorf("未超宽的文本被改动了: %q", short)
	}
}

// 装得下就不该滚动——滚动正是问题的来源。
func TestListHeightDoesNotScrollWhenItFits(t *testing.T) {
	if got := listHeight(3); got != 3 {
		t.Errorf("3 项时高度 = %d, want 3（等于项数即不滚动）", got)
	}
	if got := listHeight(1000); got >= 1000 {
		t.Errorf("项数远超屏幕时应受限于屏幕高度，得到 %d", got)
	}
}
