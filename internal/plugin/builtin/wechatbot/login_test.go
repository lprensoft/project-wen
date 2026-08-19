package wechatbot

import (
	"testing"

	"wen/internal/plugin"
)

// 二维码的 PNG 与链接原文成对下发（图形界面用前者、终端界面用后者），
// 流程结束后两者一起清掉，不留在内存里。
func TestBindStateCarriesLink(t *testing.T) {
	p := New()
	p.setBind(0, plugin.ActionPending, "请扫码", []byte{0x89, 0x50}, "https://example.com/q/abc")

	st, err := p.ActionState("bind")
	if err != nil {
		t.Fatal(err)
	}
	if st.Link != "https://example.com/q/abc" || len(st.Image) == 0 {
		t.Fatalf("state = %+v，PNG 与链接应成对下发", st)
	}

	// 中途的纯文字进展不该动二维码
	p.setBind(0, plugin.ActionPending, "已扫码，请确认", nil, "")
	st, _ = p.ActionState("bind")
	if st.Link == "" || len(st.Image) == 0 {
		t.Fatalf("文字进展不该清掉二维码：%+v", st)
	}

	p.setBind(0, plugin.ActionDone, "绑定成功", nil, "")
	st, _ = p.ActionState("bind")
	if st.Link != "" || st.Image != nil {
		t.Fatalf("结束后二维码应清掉：%+v", st)
	}
}
