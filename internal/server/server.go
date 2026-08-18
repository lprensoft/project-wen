package server

import (
	"embed"
	"io/fs"
	"net/http"

	"wen/internal/agent"
	"wen/internal/modelcfg"
	"wen/internal/plugin"
	"wen/internal/session"
)

//go:embed webui
var webuiFS embed.FS

type Server struct {
	agent    *agent.Agent
	store    *session.Store
	plugins  *plugin.Manager
	models   *modelcfg.Store
	confirms *confirmBroker
	notices  *noticeHub
}

func New(a *agent.Agent, store *session.Store, plugins *plugin.Manager, models *modelcfg.Store) *Server {
	s := &Server{
		agent: a, store: store, plugins: plugins, models: models,
		confirms: newConfirmBroker(), notices: newNoticeHub(),
	}
	// 接上会话注记的实时出口。后台工作产生的注记本来只能进日志——轮次的事件流
	// 在它跑完之前就关了。
	a.SetNoticeSink(s.notices.publish)
	return s
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
	mux.HandleFunc("POST /api/confirmations/{id}", s.confirmResolve)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.compact)
	mux.HandleFunc("GET /api/plugins", s.listPlugins)
	mux.HandleFunc("PUT /api/plugins/{name}", s.setPlugin)
	mux.HandleFunc("PUT /api/plugins/{name}/config", s.setPluginConfig)
	mux.HandleFunc("POST /api/plugins/{name}/actions/{key}", s.startPluginAction)
	mux.HandleFunc("GET /api/plugins/{name}/actions/{key}", s.pluginActionState)
	mux.HandleFunc("GET /api/models", s.listModels)
	mux.HandleFunc("PUT /api/models", s.saveModels)
	mux.HandleFunc("PUT /api/models/current", s.setCurrentModel)
	mux.HandleFunc("POST /api/models/test", s.testModel)

	return mux
}
