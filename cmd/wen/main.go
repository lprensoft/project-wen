package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wen/internal/agent"
	"wen/internal/config"
	"wen/internal/llm"
	"wen/internal/modelcfg"
	"wen/internal/plugin"
	"wen/internal/plugin/builtin/execcmd"
	"wen/internal/plugin/builtin/memory"
	"wen/internal/plugin/builtin/readfile"
	"wen/internal/plugin/builtin/sessionsearch"
	"wen/internal/plugin/builtin/webfetch"
	"wen/internal/server"
	"wen/internal/session"
)

func main() {
	var (
		configPath = flag.String("c", "", "配置文件路径（默认 ./config.yaml 或 ~/.wen/config.yaml）")
		port       = flag.Int("p", 0, "监听端口（覆盖配置文件）")
	)
	flag.Parse()

	path := config.ResolvePath(*configPath)
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if *port != 0 {
		cfg.Server.Port = *port
	}

	workdir := cfg.Agent.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	// Agent 与插件互相需要：插件在 Agent 之前构造，故辅助调用用闭包延迟到实际使用时取值
	var ag *agent.Agent
	plugins := buildPlugins(cfg, workdir, func(ctx context.Context, prompt string) (string, error) {
		if ag == nil {
			return "", fmt.Errorf("模型尚未就绪")
		}
		return ag.Complete(ctx, prompt)
	})

	store, err := session.NewStore(cfg.SessionDir())
	if err != nil {
		log.Fatalf("初始化 session 存储失败: %v", err)
	}

	// 模型配置：config.yaml 提供初始值，界面上的改动存 models.json 并优先生效
	models, err := modelcfg.NewStore(filepath.Join(cfg.BaseDir, "models.json"), cfg)
	if err != nil {
		log.Fatalf("加载模型配置失败: %v", err)
	}
	cur, err := models.Resolve()
	if err != nil {
		log.Fatalf("模型配置无效: %v", err)
	}
	provider, err := llm.New(cur.Type, cur.BaseURL, cur.APIKey)
	if err != nil {
		log.Fatalf("%v", err)
	}

	ag = agent.New(provider, plugins, store, agent.Options{
		Model:         cur.ModelID,
		Temperature:   cur.Temperature,
		MaxTokens:     cur.MaxTokens,
		SystemPrompt:  cfg.Agent.SystemPrompt,
		MaxTurns:      cfg.Agent.MaxTurns,
		Workdir:       workdir,
		Thinking:      cur.Thinking,
		ContextLength: cur.ContextLength,
	})

	srv := server.New(ag, store, plugins, models)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	log.Printf("配置文件: %s", path)
	log.Printf("模型: %s/%s  会话目录: %s", cur.ProviderName, cur.ModelID, cfg.SessionDir())
	for _, st := range plugins.List() {
		state := "禁用"
		if st.Enabled {
			state = "启用"
		}
		log.Printf("插件: %-12s [%s] %s", st.Name, state, st.Description)
	}
	log.Printf("Wen Agent 已启动: http://%s", addr)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// buildPlugins 注册全部内置系统插件，配置缺省时默认启用。
// complete 供插件发起辅助模型调用；它在 Agent 建好之前就要传进来，故用闭包延迟取值。
func buildPlugins(cfg *config.Config, workdir string, complete plugin.CompleteFunc) *plugin.Manager {
	m := plugin.NewManager(
		plugin.InitContext{Workdir: workdir, SessionDir: cfg.SessionDir(), Complete: complete},
		filepath.Join(cfg.BaseDir, "plugins.state.json"),
	)
	builtins := []plugin.Plugin{
		readfile.New(), execcmd.New(), webfetch.New(), memory.New(), sessionsearch.New(),
	}
	for _, p := range builtins {
		pc, ok := cfg.Plugins[p.Name()]
		if !ok {
			pc = plugin.PluginConfig{Enabled: true}
		}
		if err := m.Register(p, pc); err != nil {
			log.Printf("警告: 注册插件 %q 失败: %v", p.Name(), err)
		}
	}
	// 依赖校验必须等全部注册完：register 是逐个进行的，依赖方可能先于被依赖方注册
	m.Resolve()
	return m
}
