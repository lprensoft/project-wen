package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAICompat 通过 OpenAI 兼容协议对接后端（DeepSeek 等）。
// 自实现请求与 SSE 解析，以支持 thinking / reasoning_effort / reasoning_content
// 等 DeepSeek 扩展字段的完整往返。
type OpenAICompat struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAICompat(baseURL, apiKey string) *OpenAICompat {
	return &OpenAICompat{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		// 总超时不设上限（长思考+长输出），连接阶段单独限时
		client: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 60 * time.Second,
		}},
	}
}

// ---------- 请求/响应的线上格式 ----------

type wireThinking struct {
	Type string `json:"type"` // enabled | disabled
}

type wireRequest struct {
	Model           string             `json:"model"`
	Messages        []wireMessage      `json:"messages"`
	Tools           []wireTool         `json:"tools,omitempty"`
	Temperature     *float64           `json:"temperature,omitempty"`
	MaxTokens       int                `json:"max_tokens,omitempty"`
	Stream          bool               `json:"stream"`
	StreamOptions   *wireStreamOptions `json:"stream_options,omitempty"`
	Thinking        *wireThinking      `json:"thinking,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function wireToolCallFunc `json:"function"`
}

type wireToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type wireTool struct {
	Type     string      `json:"type"`
	Function wireToolDef `json:"function"`
}

type wireToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"` // 最后一个 chunk 携带（需 stream_options.include_usage）
}

func buildRequest(req ChatRequest) wireRequest {
	wr := wireRequest{
		Model:         req.Model,
		MaxTokens:     req.MaxTokens,
		Stream:        true,
		StreamOptions: &wireStreamOptions{IncludeUsage: true},
	}
	switch req.Thinking {
	case "", "off":
		wr.Thinking = &wireThinking{Type: "disabled"}
		t := req.Temperature
		wr.Temperature = &t
	default:
		wr.Thinking = &wireThinking{Type: "enabled"}
		wr.ReasoningEffort = req.Thinking
		// 思考模式不支持 temperature 等采样参数，不发送
	}
	for _, m := range req.Messages {
		wm := wireMessage{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.Reasoning,
			ToolCallID:       m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunc{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		wr.Messages = append(wr.Messages, wm)
	}
	for _, t := range req.Tools {
		wr.Tools = append(wr.Tools, wireTool{
			Type: "function",
			Function: wireToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	return wr
}

// ---------- 流式调用 ----------

func (p *OpenAICompat) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	body, err := json.Marshal(buildRequest(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm api: status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		emit := func(ev StreamEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		acc := map[int]*partialCall{}
		finish := func() {
			if len(acc) > 0 {
				emit(StreamEvent{Type: EventToolCalls, ToolCalls: assembleCalls(acc)})
			}
			emit(StreamEvent{Type: EventDone})
		}

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finish()
				return
			}
			var chunk wireChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				if !emit(StreamEvent{Type: EventUsage, Usage: chunk.Usage}) {
					return
				}
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta
			if delta.ReasoningContent != "" {
				if !emit(StreamEvent{Type: EventReasoningDelta, Content: delta.ReasoningContent}) {
					return
				}
			}
			if delta.Content != "" {
				if !emit(StreamEvent{Type: EventContentDelta, Content: delta.Content}) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				pt, ok := acc[idx]
				if !ok {
					pt = &partialCall{}
					acc[idx] = pt
				}
				if tc.ID != "" {
					pt.id = tc.ID
				}
				if tc.Function.Name != "" {
					pt.name = tc.Function.Name
				}
				pt.args.WriteString(tc.Function.Arguments)
			}
		}
		if err := sc.Err(); err != nil {
			emit(StreamEvent{Type: EventError, Err: fmt.Errorf("read stream: %w", err)})
			return
		}
		// 流未见 [DONE] 即结束：按已收内容收尾
		finish()
	}()
	return ch, nil
}

// partialCall 累积流式下发的 tool_call 分片。
type partialCall struct {
	id   string
	name string
	args strings.Builder
}

func assembleCalls(acc map[int]*partialCall) []ToolCall {
	idxs := make([]int, 0, len(acc))
	for i := range acc {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	calls := make([]ToolCall, 0, len(acc))
	for _, i := range idxs {
		pt := acc[i]
		args := pt.args.String()
		if args == "" {
			args = "{}"
		}
		calls = append(calls, ToolCall{
			ID:        pt.id,
			Name:      pt.name,
			Arguments: json.RawMessage(args),
		})
	}
	return calls
}
