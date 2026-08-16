package plugin

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// NormalizeEncoding 处理中文 Windows 等环境下的 GBK 输出：
// 输入为非法 UTF-8 时尝试按 GBK 解码，失败则原样返回。
func NormalizeEncoding(out []byte) []byte {
	if utf8.Valid(out) {
		return out
	}
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(out); err == nil {
		return decoded
	}
	return out
}

// TruncateUTF8 将字节序列截断到 max 字节以内，并保证不切坏多字节字符。
// 第二个返回值表示是否发生截断。
func TruncateUTF8(b []byte, max int) ([]byte, bool) {
	if len(b) <= max {
		return b, false
	}
	b = b[:max]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b, true
}
