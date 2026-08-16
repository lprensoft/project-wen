package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestNormalizeEncodingGBK(t *testing.T) {
	const want = "系统找不到指定的文件。"
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if utf8.Valid(gbk) {
		t.Fatal("test setup: GBK bytes should not be valid UTF-8")
	}
	if got := string(normalizeEncoding(gbk)); got != want {
		t.Errorf("normalizeEncoding = %q, want %q", got, want)
	}
	// 合法 UTF-8 原样返回
	if got := string(normalizeEncoding([]byte(want))); got != want {
		t.Errorf("valid UTF-8 should pass through, got %q", got)
	}
}

func TestExecCommandGBKOutput(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("GBK 输出问题仅出现在中文 Windows 的 cmd 下")
	}
	tool := &ExecCommand{}
	// type 一个不存在的文件，cmd 会输出 GBK 编码的中文错误信息
	args, _ := json.Marshal(map[string]string{"command": `type not_exist_file_xyz.txt`})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result) {
		t.Errorf("result is not valid UTF-8: %q", result)
	}
	if strings.ContainsRune(result, '�') {
		t.Errorf("result contains replacement chars (mojibake): %q", result)
	}
	t.Logf("output: %s", result)
}
