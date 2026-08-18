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
		return "Anthropic 兼容"
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

// Config 是创建 Provider 所需的提供商级参数。用结构体而不是一串位置参数：
// 后面两项都是「只对某一种 API 模式有意义」的可选项，位置传参很容易接错。
type Config struct {
	Type    string
	BaseURL string
	APIKey  string
	// Dialect 是思考参数方言（见 dialect.go），仅 openai_compat 使用；
	// 空串等同 DeepSeek 方言。
	Dialect string
	// PromptCache 是否使用提示词缓存，仅 anthropic 使用——它要求调用方在请求里
	// 显式打断点，且缓存写入单独计费。openai_compat 那边（DeepSeek、OpenAI）的
	// 缓存由服务端自动维护，与这个开关无关，命中与否只取决于前缀是否稳定。
	PromptCache bool
}

// New 按 API 模式创建 Provider。
func New(cfg Config) (Provider, error) {
	if !IsKnownDialect(cfg.Dialect) {
		return nil, fmt.Errorf("不支持的思考参数方言 %q（可选：%s）", cfg.Dialect, strings.Join(KnownDialects, " / "))
	}
	switch cfg.Type {
	case TypeOpenAICompat:
		return NewOpenAICompat(cfg.BaseURL, cfg.APIKey, cfg.Dialect), nil
	case TypeAnthropic:
		return NewAnthropic(cfg.BaseURL, cfg.APIKey, cfg.PromptCache), nil
	}
	return nil, fmt.Errorf("不支持的 API 模式 %q（可选：%s）", cfg.Type, strings.Join(KnownTypes, " / "))
}
