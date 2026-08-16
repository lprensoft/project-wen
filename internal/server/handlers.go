package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"wen/internal/agent"
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Agent 从单 goroutine 调用 emit，无需加锁
	emit := func(ev agent.Event) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		flusher.Flush()
	}

	s.agent.Run(r.Context(), req.SessionID, req.Message, emit)
}
