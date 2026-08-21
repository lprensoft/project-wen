package updater

import "testing"

// 发布正文是流水线拼出来的：CHANGELOG 那一节 + `---` + 下载说明。
// 自更新窗口里只该出现前半段——后半段讲的是怎么手工下载，正是它替人做掉的事。
func TestReleaseNotes(t *testing.T) {
	body := "### 修复\n\n- 修了一个问题\n\n---\n\n### 下载哪个\n\n| 系统 | 文件 |\n"
	want := "### 修复\n\n- 修了一个问题"
	if got := ReleaseNotes(body); got != want {
		t.Fatalf("没有在分隔线处截断: %q", got)
	}

	// 没有分隔线时原样返回，不因为格式变了就把说明整个吞掉
	plain := "  就一句话  "
	if got := ReleaseNotes(plain); got != "就一句话" {
		t.Fatalf("没有分隔线时不该丢内容: %q", got)
	}

	// CRLF 的正文同样认得出那条线
	crlf := "### 修复\r\n\r\n- 修了一个问题\r\n\r\n---\r\n\r\n### 下载哪个\r\n"
	if got := ReleaseNotes(crlf); got != "### 修复\r\n\r\n- 修了一个问题" {
		t.Fatalf("CRLF 正文没截断: %q", got)
	}
}
