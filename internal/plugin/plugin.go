package plugin

import (
	"context"
	"encoding/json"
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
