package server

import (
	"embed"
	"io/fs"
	"net/http"

	"wen/internal/agent"
	"wen/internal/session"
)

//go:embed webui
var webuiFS embed.FS

type Server struct {
	agent *agent.Agent
	store *session.Store
}

func New(a *agent.Agent, store *session.Store) *Server {
	return &Server{agent: a, store: store}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(webuiFS, "webui")
	mux.Handle("GET /", http.FileServerFS(static))

	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /api/chat", s.chat)

	return mux
}
