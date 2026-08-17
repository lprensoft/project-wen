package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Tool 是模型可调用的工具，由插件提供。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // 参数 JSON Schema
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// InitContext 是核心传给插件的运行环境。
type InitContext struct {
	Workdir string // agent 工作目录
}

// Plugin 是系统插件的最小协议。
// 所有提示词与描述遵循项目约定：中文、只写功能、不含身份信息。
type Plugin interface {
	Name() string        // 唯一标识（小写下划线）
	Description() string // 一句话说明，供 /plugins 列表展示
	// Init 在插件启用时调用；cfg 来自配置文件 plugins.<name>.config，可能为 nil。
	Init(ictx InitContext, cfg map[string]any) error
	// SystemPrompt 返回注入系统提示词的片段；空串表示不注入。
	SystemPrompt() string
	// Tools 返回插件提供的工具；可为空。
	Tools() []Tool
}

// 可配置项的类型取值。
const (
	FieldInt    = "int"
	FieldBool   = "bool"
	FieldString = "string"
	FieldSelect = "select"
)

// ConfigOption 是 select 型配置项的一个候选值。
type ConfigOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ConfigField 描述插件的一个可配置项，Web UI 据此生成表单。
type ConfigField struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Default     any            `json:"default"`
	Min         *int           `json:"min,omitempty"`     // 仅 int 型
	Max         *int           `json:"max,omitempty"`     // 仅 int 型
	Options     []ConfigOption `json:"options,omitempty"` // 仅 select 型
}

// Configurable 是插件的可选能力：声明可在界面上配置的项。
// 未实现该接口的插件在界面上不显示配置入口。
type Configurable interface {
	ConfigFields() []ConfigField
}

// IntPtr 便于在字段声明中书写 Min/Max。
func IntPtr(v int) *int { return &v }

// ConfigFieldsOf 返回插件声明的可配置项；未实现 Configurable 时返回 nil。
func ConfigFieldsOf(p Plugin) []ConfigField {
	if c, ok := p.(Configurable); ok {
		return c.ConfigFields()
	}
	return nil
}

// NormalizeConfig 按字段声明校验并规整配置：
// 未声明的键被忽略，缺失的键取默认值，字符串形式的数字/布尔值会被转换。
func NormalizeConfig(fields []ConfigField, in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		raw, ok := in[f.Key]
		if !ok || raw == nil || raw == "" {
			raw = f.Default
		}
		v, err := normalizeField(f, raw)
		if err != nil {
			return nil, err
		}
		out[f.Key] = v
	}
	return out, nil
}

func normalizeField(f ConfigField, raw any) (any, error) {
	label := f.Label
	if label == "" {
		label = f.Key
	}
	switch f.Type {
	case FieldInt:
		n, ok := toInt(raw)
		if !ok {
			return nil, fmt.Errorf("配置项 %q 需要整数", label)
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("配置项 %q 不能小于 %d", label, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("配置项 %q 不能大于 %d", label, *f.Max)
		}
		return n, nil
	case FieldBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("配置项 %q 需要布尔值", label)
			}
			return b, nil
		}
		return nil, fmt.Errorf("配置项 %q 需要布尔值", label)
	case FieldString:
		s, ok := raw.(string)
		if !ok {
			if raw == nil {
				return "", nil
			}
			return nil, fmt.Errorf("配置项 %q 需要文本", label)
		}
		return s, nil
	case FieldSelect:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("配置项 %q 需要文本", label)
		}
		for _, o := range f.Options {
			if o.Value == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("配置项 %q 的取值 %q 不在候选项中", label, s)
	}
	return nil, fmt.Errorf("配置项 %q 的类型 %q 不支持", label, f.Type)
}

// toInt 接受 int / float64（须为整数）/ 数字字符串。
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

// CfgInt 从插件配置中取整数，缺失或类型不符时返回默认值。
func CfgInt(cfg map[string]any, key string, def int) int {
	if v, ok := cfg[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case float64:
			return int(n)
		}
	}
	return def
}

// CfgString 从插件配置中取字符串，缺失时返回默认值。
func CfgString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}
