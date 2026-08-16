package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"wen/internal/agent"
	"wen/internal/agent/tools"
	"wen/internal/config"
	"wen/internal/llm"
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

	providerCfg := cfg.Providers[cfg.Model.Provider]
	provider := llm.NewOpenAICompat(providerCfg.BaseURL, providerCfg.APIKey)

	workdir := cfg.Agent.Tools.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	registry := buildRegistry(cfg, workdir)

	store, err := session.NewStore(cfg.SessionDir())
	if err != nil {
		log.Fatalf("初始化 session 存储失败: %v", err)
	}

	ag := agent.New(provider, registry, store, agent.Options{
		Model:         cfg.Model.Name,
		Temperature:   cfg.Model.Temperature,
		MaxTokens:     cfg.Model.MaxTokens,
		SystemPrompt:  cfg.Agent.SystemPrompt,
		MaxTurns:      cfg.Agent.MaxTurns,
		Workdir:       workdir,
		Thinking:      cfg.Model.Thinking,
		ContextLength: cfg.Model.ContextLength,
	})

	srv := server.New(ag, store)
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	log.Printf("配置文件: %s", path)
	log.Printf("模型: %s/%s  会话目录: %s", cfg.Model.Provider, cfg.Model.Name, cfg.SessionDir())
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

func buildRegistry(cfg *config.Config, workdir string) *tools.Registry {
	available := map[string]tools.Tool{
		"read_file": &tools.ReadFile{Workdir: workdir},
		"exec_command": &tools.ExecCommand{
			Workdir: workdir,
			Timeout: time.Duration(cfg.Agent.Tools.ExecTimeoutSeconds) * time.Second,
		},
	}
	registry := tools.NewRegistry()
	for _, name := range cfg.Agent.Tools.Enabled {
		t, ok := available[name]
		if !ok {
			log.Printf("警告: 未知工具 %q，已忽略", name)
			continue
		}
		registry.Register(t)
	}
	return registry
}
