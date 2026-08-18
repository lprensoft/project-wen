package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"wen/internal/agent"
	"wen/internal/plugin"
	"wen/internal/session"
	"wen/internal/version"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	metas, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, metas)
}

func (s *Server) createSession(w http.ResponseWriter, _ *http.Request) {
	meta, err := s.store.Create()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	meta, msgs, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if os.IsNotExist(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if msgs == nil {
		msgs = []session.StoredMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta, "messages": msgs})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if os.IsNotExist(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chat 处理一条用户消息，以 SSE 流返回 Agent 事件。
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
		// DebugPrompt 打开时逐次发回实际提交给模型的请求体，供界面调试查看。
		// 默认关闭：整轮的完整上下文可能有数 MB，不看的时候不该付这份代价。
		DebugPrompt bool `json:"debug_prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SessionID == "" || req.Message == "" {
		writeError(w, http.StatusBadRequest, "session_id and message are required")
		return
	}
	if _, _, err := s.store.Get(req.SessionID); err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("session not found: %s", req.SessionID))
		return
	}

	emit, emitRaw, ok := startSSE(w)
	if !ok {
		return
	}
	// 工具要请求用户确认时经这条 SSE 流问、经 /api/confirmations/{id} 收答复。
	// 按请求注入而不是放进 InitContext：确认必须回到发起这轮对话的那个界面。
	ctx := plugin.WithConfirmer(r.Context(), s.confirmerFor(emitRaw))
	// 前台界面的轮次有真人在交互：更新会话活跃时间，供后台功能判定「最近活跃会话」
	ctx = plugin.WithInteractive(ctx)
	if req.DebugPrompt {
		ctx = agent.WithPromptTrace(ctx)
	}
	s.agent.Run(ctx, req.SessionID, req.Message, emit)
}

// startSSE 设置 SSE 响应头，返回 Agent 事件写入函数与一个原始帧写入函数
// （Agent 与工具执行同在一个 goroutine 上，无需加锁）。
// 原始帧留给不属于 Agent 事件模型的东西，如工具的确认请求。
func startSSE(w http.ResponseWriter) (func(agent.Event), func(string, any), bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	emitRaw := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		flusher.Flush()
	}
	emit := func(ev agent.Event) {
		emitRaw(string(ev.Type), ev)
	}
	return emit, emitRaw, true
}

// status 返回 Agent 配置与（可选的）当前会话上下文用量。
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	provider, model, thinking, contextLength := s.models.Status()
	resp := map[string]any{
		"version":        version.Version,
		"provider":       provider,
		"model":          model,
		"thinking":       thinking,
		"context_length": contextLength,
	}
	// 插件贡献的状态行（如心跳节奏）：无插件可报时不下发该字段，界面据此不占版面
	if lines := s.plugins.StatusLines(); len(lines) > 0 {
		resp["plugin_lines"] = lines
	}
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		if meta, msgs, err := s.store.Get(sid); err == nil {
			sess := map[string]any{
				"id":            sid,
				"message_count": len(msgs),
				"est_tokens":    agent.EstimateStoredTokens(msgs),
			}
			if meta.LastUsage != nil {
				// 实测值：最近一次请求的完整上下文 + 本次输出
				sess["measured_tokens"] = meta.LastUsage.PromptTokens + meta.LastUsage.CompletionTokens
				// 缓存命中：只在真的命中或写入过时下发，界面据此不占版面
				if meta.LastUsage.CachedTokens > 0 || meta.LastUsage.CacheWriteTokens > 0 {
					sess["cached_tokens"] = meta.LastUsage.CachedTokens
					sess["cache_write_tokens"] = meta.LastUsage.CacheWriteTokens
					sess["prompt_tokens"] = meta.LastUsage.PromptTokens
				}
			}
			resp["session"] = sess
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// listPlugins 返回全部插件状态。
func (s *Server) listPlugins(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.plugins.List())
}

// setPlugin 运行时启用/禁用插件。
func (s *Server) setPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.plugins.SetEnabled(r.PathValue("name"), req.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.plugins.List())
}

// setPluginConfig 保存插件配置，保存成功后立即生效。
func (s *Server) setPluginConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.plugins.SetConfig(r.PathValue("name"), req.Config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.plugins.List())
}

// startPluginAction 触发插件的一个操作入口（长流程在插件后台进行，本请求立即返回）。
//
// 请求体可选，形如 {"config": {...}}：配置弹窗里当前填写、尚未保存的值。它随 ctx 交给
// 插件（见 plugin.WithActionValues），使「测试」类操作能验证还没保存的配置。空 body 合法，
// 解析失败也只当作没带草稿值——操作本身不该因为这个失败。
func (s *Server) startPluginAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config map[string]any `json:"config"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ctx := plugin.WithActionValues(r.Context(), req.Config)
	if err := s.plugins.StartAction(ctx, r.PathValue("name"), r.PathValue("key")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// pluginActionState 查询插件操作的进展，供界面轮询。
func (s *Server) pluginActionState(w http.ResponseWriter, r *http.Request) {
	st, err := s.plugins.ActionState(r.PathValue("name"), r.PathValue("key"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// compact 压缩指定会话，过程以 SSE 流返回。
func (s *Server) compact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, _, err := s.store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("session not found: %s", id))
		return
	}
	emit, _, ok := startSSE(w)
	if !ok {
		return
	}
	s.agent.Compact(r.Context(), id, emit)
}
