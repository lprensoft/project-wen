package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"wen/internal/agent"
	"wen/internal/llm"
	"wen/internal/session"
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

	emit, ok := startSSE(w)
	if !ok {
		return
	}
	s.agent.Run(r.Context(), req.SessionID, req.Message, emit)
}

// startSSE 设置 SSE 响应头并返回事件写入函数（Agent 从单 goroutine 调用，无需加锁）。
func startSSE(w http.ResponseWriter) (func(agent.Event), bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return func(ev agent.Event) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		flusher.Flush()
	}, true
}

// status 返回 Agent 配置与（可选的）当前会话上下文用量。
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"provider":       s.info.Provider,
		"model":          s.info.Model,
		"thinking":       s.info.Thinking,
		"context_length": s.info.ContextLength,
	}
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		if meta, msgs, err := s.store.Get(sid); err == nil {
			lms := make([]llm.Message, 0, len(msgs))
			for _, m := range msgs {
				lms = append(lms, m.Message)
			}
			sess := map[string]any{
				"id":            sid,
				"message_count": len(msgs),
				"est_tokens":    agent.EstimateHistoryTokens(lms),
			}
			if meta.LastUsage != nil {
				// 实测值：最近一次请求的完整上下文 + 本次输出
				sess["measured_tokens"] = meta.LastUsage.PromptTokens + meta.LastUsage.CompletionTokens
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

// compact 压缩指定会话，过程以 SSE 流返回。
func (s *Server) compact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, _, err := s.store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("session not found: %s", id))
		return
	}
	emit, ok := startSSE(w)
	if !ok {
		return
	}
	s.agent.Compact(r.Context(), id, emit)
}
