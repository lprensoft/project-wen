package tools

import (
	"context"
	"encoding/json"

	"wen/internal/llm"
)

// Tool 是 Agent 可调用的工具。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // 参数 JSON Schema
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry 按名字索引工具。
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Specs 返回全部工具的 LLM 声明（按注册顺序）。
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return specs
}
