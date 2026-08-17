package llm

import (
	"strings"
	"testing"
)

// 各方言拼出的思考参数。
func TestApplyThinking(t *testing.T) {
	req := func(thinking string) ChatRequest {
		return ChatRequest{Thinking: thinking, Temperature: 0.7}
	}
	build := func(dialect, thinking string) wireRequest {
		var wr wireRequest
		applyThinking(&wr, req(thinking), dialect)
		return wr
	}

	// DeepSeek（默认）：enabled/disabled + effort，思考态不发 temperature
	wr := build("", "high")
	if wr.Thinking == nil || wr.Thinking.Type != "enabled" || wr.ReasoningEffort != "high" || wr.Temperature != nil {
		t.Fatalf("deepseek 思考态: %+v", wr)
	}
	wr = build(DialectDeepSeek, "off")
	if wr.Thinking == nil || wr.Thinking.Type != "disabled" || wr.Temperature == nil {
		t.Fatalf("deepseek 关闭态: %+v", wr)
	}

	// MiniMax：adaptive/disabled + reasoning_split，temperature 照发，不发 effort
	wr = build(DialectMiniMax, "high")
	if wr.Thinking == nil || wr.Thinking.Type != "adaptive" || !wr.ReasoningSplit ||
		wr.ReasoningEffort != "" || wr.Temperature == nil {
		t.Fatalf("minimax 思考态: %+v", wr)
	}
	wr = build(DialectMiniMax, "off")
	if wr.Thinking == nil || wr.Thinking.Type != "disabled" || wr.ReasoningSplit || wr.Temperature == nil {
		t.Fatalf("minimax 关闭态: %+v", wr)
	}

	// Qwen：enable_thinking 布尔
	wr = build(DialectQwen, "medium")
	if wr.EnableThinking == nil || !*wr.EnableThinking || wr.Thinking != nil {
		t.Fatalf("qwen 思考态: %+v", wr)
	}
	wr = build(DialectQwen, "off")
	if wr.EnableThinking == nil || *wr.EnableThinking {
		t.Fatalf("qwen 关闭态: %+v", wr)
	}

	// effort：仅 reasoning_effort，思考态不发 temperature
	wr = build(DialectEffort, "low")
	if wr.ReasoningEffort != "low" || wr.Thinking != nil || wr.Temperature != nil {
		t.Fatalf("effort 思考态: %+v", wr)
	}
	wr = build(DialectEffort, "off")
	if wr.ReasoningEffort != "" || wr.Temperature == nil {
		t.Fatalf("effort 关闭态: %+v", wr)
	}

	// none：什么思考参数都不发
	wr = build(DialectNone, "high")
	if wr.Thinking != nil || wr.ReasoningEffort != "" || wr.EnableThinking != nil || wr.Temperature == nil {
		t.Fatalf("none: %+v", wr)
	}
}

// <think> 剥离：含标签被流式切碎的情况。
func TestThinkFilter(t *testing.T) {
	feedAll := func(chunks []string) (content, reasoning string) {
		f := &thinkFilter{}
		var c, r strings.Builder
		for _, ch := range chunks {
			cc, rr := f.feed(ch)
			c.WriteString(cc)
			r.WriteString(rr)
		}
		cc, rr := f.flush()
		c.WriteString(cc)
		r.WriteString(rr)
		return c.String(), r.String()
	}

	// 完整标签在同一 chunk
	c, r := feedAll([]string{"<think>思考中</think>答案"})
	if c != "答案" || r != "思考中" {
		t.Fatalf("整段: content=%q reasoning=%q", c, r)
	}

	// 标签被切碎在多个 chunk
	c, r = feedAll([]string{"<th", "ink>分", "析</thi", "nk>结", "论"})
	if c != "结论" || r != "分析" {
		t.Fatalf("切碎: content=%q reasoning=%q", c, r)
	}

	// 没有标签：原样通过
	c, r = feedAll([]string{"普通", "回答"})
	if c != "普通回答" || r != "" {
		t.Fatalf("无标签: content=%q reasoning=%q", c, r)
	}

	// 未闭合的思考：flush 后归入思考
	c, r = feedAll([]string{"<think>只有思考没有闭合"})
	if c != "" || r != "只有思考没有闭合" {
		t.Fatalf("未闭合: content=%q reasoning=%q", c, r)
	}

	// 形似标签前缀但不是标签：flush 时按原文吐回正文
	c, r = feedAll([]string{"结尾是 <thi"})
	if c != "结尾是 <thi" || r != "" {
		t.Fatalf("假前缀: content=%q reasoning=%q", c, r)
	}
}
