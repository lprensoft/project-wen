package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wen/internal/plugin"
)

// draftPlugin 记下每次操作拿到的草稿配置值。
type draftPlugin struct {
	got map[string]any
}

func (p *draftPlugin) Name() string        { return "drafty" }
func (p *draftPlugin) Description() string { return "记录草稿值的假插件" }
func (p *draftPlugin) Init(plugin.InitContext, map[string]any) error {
	return nil
}
func (p *draftPlugin) SystemPrompt() string { return "" }
func (p *draftPlugin) Tools() []plugin.Tool { return nil }
func (p *draftPlugin) Actions() []plugin.ActionDef {
	return []plugin.ActionDef{{Key: "test", Label: "测试"}}
}
func (p *draftPlugin) StartAction(ctx context.Context, key string) error {
	p.got = plugin.ActionValuesFrom(ctx)
	return nil
}
func (p *draftPlugin) ActionState(string) (plugin.ActionState, error) {
	return plugin.ActionState{Status: plugin.ActionDone, Message: "好了"}, nil
}
func (p *draftPlugin) ConfigFields() []plugin.ConfigField {
	return []plugin.ConfigField{{Key: "location", Label: "城市", Type: plugin.FieldString, Default: ""}}
}

func startAction(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/plugins/drafty/actions/test", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/plugins/drafty/actions/test", strings.NewReader(body))
	}
	r.SetPathValue("name", "drafty")
	r.SetPathValue("key", "test")
	w := httptest.NewRecorder()
	s.startPluginAction(w, r)
	return w
}

func newDraftServer(t *testing.T) (*Server, *draftPlugin) {
	t.Helper()
	p := &draftPlugin{}
	m := plugin.NewManager(plugin.InitContext{}, "")
	if err := m.Register(p, plugin.PluginConfig{Enabled: true}); err != nil {
		t.Fatalf("注册插件: %v", err)
	}
	m.Resolve()
	return &Server{plugins: m}, p
}

// 配置弹窗里尚未保存的值要原样送到插件手上——「测试」类操作全靠它先验后存。
func TestStartActionPassesDraftConfig(t *testing.T) {
	s, p := newDraftServer(t)

	w := startAction(t, s, `{"config":{"location":"苏州"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d：%s", w.Code, w.Body.String())
	}
	if p.got["location"] != "苏州" {
		t.Errorf("插件拿到的草稿值 = %v", p.got)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if resp["status"] != "started" {
		t.Errorf("响应 = %v", resp)
	}
}

// 空 body 合法：不关心草稿值的操作（重置、清空、扫码绑定）照旧不带。
func TestStartActionWithoutBody(t *testing.T) {
	s, p := newDraftServer(t)
	if w := startAction(t, s, ""); w.Code != http.StatusOK {
		t.Fatalf("HTTP %d：%s", w.Code, w.Body.String())
	}
	if p.got != nil {
		t.Errorf("没带 body 时不该有草稿值，实际 %v", p.got)
	}
}

// body 坏了也只当没带草稿值：操作本身不该因为这个失败。
func TestStartActionIgnoresBadBody(t *testing.T) {
	s, p := newDraftServer(t)
	if w := startAction(t, s, "{不是 JSON"); w.Code != http.StatusOK {
		t.Fatalf("HTTP %d：%s", w.Code, w.Body.String())
	}
	if p.got != nil {
		t.Errorf("坏 body 不该产生草稿值，实际 %v", p.got)
	}
}

// mdPlugin 的操作正文声明为 markdown。
type mdPlugin struct{ draftPlugin }

func (p *mdPlugin) Name() string { return "mdy" }
func (p *mdPlugin) ActionState(string) (plugin.ActionState, error) {
	return plugin.ActionState{Status: plugin.ActionDone, Message: "### 标题\n\n- 一条", Markdown: true}, nil
}

// 「按 markdown 渲染」是插件声明的，要原样传到界面；不声明的一律是纯文本，
// 字段不出现在 JSON 里——绝大多数操作给的是纯文本，界面不该去猜。
func TestActionStateCarriesMarkdownFlag(t *testing.T) {
	plain := &draftPlugin{}
	md := &mdPlugin{}
	m := plugin.NewManager(plugin.InitContext{}, "")
	for _, p := range []plugin.Plugin{plain, md} {
		if err := m.Register(p, plugin.PluginConfig{Enabled: true}); err != nil {
			t.Fatalf("注册插件: %v", err)
		}
	}
	s := &Server{plugins: m}

	get := func(name string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/plugins/"+name+"/actions/test", nil)
		r.SetPathValue("name", name)
		r.SetPathValue("key", "test")
		w := httptest.NewRecorder()
		s.pluginActionState(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d — %s", name, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: 解析响应: %v", name, err)
		}
		return out
	}

	if got := get("mdy"); got["markdown"] != true {
		t.Fatalf("声明了 markdown 的没传出去: %v", got)
	}
	if got := get("drafty"); got["markdown"] != nil {
		t.Fatalf("没声明的不该出现 markdown 字段: %v", got)
	}
}
