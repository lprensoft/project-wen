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
	"wen/internal/plugin/builtin/dualpersona"
	"wen/internal/plugin/builtin/execcmd"
	"wen/internal/plugin/builtin/memory"
	"wen/internal/plugin/builtin/readfile"
	"wen/internal/plugin/builtin/roleplay"
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

// needsSetupPlugins 是默认不启用的插件：它们不配置就没法工作——roleplay 没有角色设定
// 就不成其为角色扮演，dual_persona 没有触发词就永远切不过去。默认打开一个空壳只会让人
// 以为功能坏了，所以留给用户配好再开。
//
// 其余插件用声明的默认参数就能直接工作，因此默认启用。
var needsSetupPlugins = map[string]bool{
	"roleplay":     true,
	"dual_persona": true,
}

// buildPlugins 注册全部内置系统插件，注册顺序即提示词拼接顺序。
//
// 开关与配置的唯一来源是 <配置目录>/plugins.state.json（由设置页维护）；这里给出的只是
// 首次安装、状态文件还不存在时的初值。
// complete 供插件发起辅助模型调用；它在 Agent 建好之前就要传进来，故用闭包延迟取值。
func buildPlugins(cfg *config.Config, workdir string, complete plugin.CompleteFunc) *plugin.Manager {
	m := plugin.NewManager(
		plugin.InitContext{Workdir: workdir, SessionDir: cfg.SessionDir(), Complete: complete},
		filepath.Join(cfg.BaseDir, "plugins.state.json"),
	)
	builtins := []plugin.Plugin{
		readfile.New(), execcmd.New(), webfetch.New(), memory.New(), sessionsearch.New(),
		// roleplay 必须在 dualpersona 之前：表人格设定要排在里人格设定前面，
		// 后者才能形成追加与覆盖的语义
		roleplay.New(), dualpersona.New(),
	}
	for _, p := range builtins {
		// Config 留空：插件自己声明的默认值就是初值，不需要在这里重复一遍
		init := plugin.PluginConfig{Enabled: !needsSetupPlugins[p.Name()]}
		if err := m.Register(p, init); err != nil {
			log.Printf("警告: 注册插件 %q 失败: %v", p.Name(), err)
		}
	}
	// 依赖校验必须等全部注册完：register 是逐个进行的，依赖方可能先于被依赖方注册
	m.Resolve()
	return m
}
