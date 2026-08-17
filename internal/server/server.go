package server

import (
	"embed"
	"io/fs"
	"net/http"

	"wen/internal/agent"
	"wen/internal/plugin"
	"wen/internal/session"
)

//go:embed webui
var webuiFS embed.FS

// Info 是 /api/status 展示的静态配置信息。
type Info struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Thinking      string `json:"thinking"`
	ContextLength int    `json:"context_length"`
}

type Server struct {
	agent   *agent.Agent
	store   *session.Store
	plugins *plugin.Manager
	info    Info
}

func New(a *agent.Agent, store *session.Store, plugins *plugin.Manager, info Info) *Server {
	return &Server{agent: a, store: store, plugins: plugins, info: info}
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
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.compact)
	mux.HandleFunc("GET /api/plugins", s.listPlugins)
	mux.HandleFunc("PUT /api/plugins/{name}", s.setPlugin)
	mux.HandleFunc("PUT /api/plugins/{name}/config", s.setPluginConfig)

	return mux
}
