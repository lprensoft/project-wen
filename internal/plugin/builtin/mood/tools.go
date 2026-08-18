package mood

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("心情尚未就绪")

type adjustTool struct{ p *Plugin }

func (t *adjustTool) Name() string { return "adjust_mood" }

func (t *adjustTool) Description() string {
	return "按本轮互动调整角色此刻的心情，并返回调整后的心情。" +
		"报的是变化量而不是目标值：正数变开心、负数变沮丧。" +
		"出现会影响心情的事时先调用它拿到新的心情，再据此写这一轮的表达；一轮最多调一次。"
}

func (t *adjustTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"delta": {"type": "integer", "description": "心情的变化量，正数变开心、负数变沮丧。小事几点，真正触动的事几十点"},
			"reason": {"type": "string", "description": "这次变化的起因，一句话，如「她夸了新剪的头发」"}
		},
		"required": ["delta", "reason"]
	}`)
}

func (t *adjustTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Delta  int    `json:"delta"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}

	s := t.p.snapshot()
	// 心情只读写本轮的写入域：两个人格各有各的心情
	store := t.p.storeFor(plugin.ScopeFrom(ctx).Write)
	if store == nil {
		return "", errNotReady
	}
	st, applied, err := store.Apply(a.Delta, s.maxDelta, s.decayPerHour, a.Reason, time.Now())
	if err != nil {
		return "", err
	}

	out := fmt.Sprintf("心情：%s（%+d，本次 %+d）", band(st.Value), st.Value, applied)
	// 拦截生效时要把规则告诉模型，否则它只会换个更大的数字再试一次
	if applied != a.Delta {
		out += fmt.Sprintf("。本次变化量按单次上限 ±%d 收了（原报 %+d）", s.maxDelta, a.Delta)
	}
	return out + "。", nil
}
