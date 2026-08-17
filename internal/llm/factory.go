package llm

import (
	"fmt"
	"slices"
	"strings"
)

// Provider 的 API 模式。
const (
	TypeOpenAICompat = "openai_compat"
	TypeAnthropic    = "anthropic"
)

// KnownTypes 是支持的 API 模式（配置校验与界面下拉共用，避免两处漂移）。
var KnownTypes = []string{TypeOpenAICompat, TypeAnthropic}

// TypeLabel 返回 API 模式的中文显示名。
func TypeLabel(typ string) string {
	switch typ {
	case TypeOpenAICompat:
		return "OpenAI 兼容"
	case TypeAnthropic:
		return "Anthropic"
	}
	return typ
}

// DefaultBaseURL 返回该 API 模式常用的默认地址，供界面预填。
func DefaultBaseURL(typ string) string {
	switch typ {
	case TypeOpenAICompat:
		return "https://api.deepseek.com"
	case TypeAnthropic:
		return "https://api.anthropic.com"
	}
	return ""
}

func IsKnownType(typ string) bool { return slices.Contains(KnownTypes, typ) }

// New 按 API 模式创建 Provider。
func New(typ, baseURL, apiKey string) (Provider, error) {
	switch typ {
	case TypeOpenAICompat:
		return NewOpenAICompat(baseURL, apiKey), nil
	case TypeAnthropic:
		return NewAnthropic(baseURL, apiKey), nil
	}
	return nil, fmt.Errorf("不支持的 API 模式 %q（可选：%s）", typ, strings.Join(KnownTypes, " / "))
}
