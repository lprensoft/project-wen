package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"wen/internal/agent"
	"wen/internal/config"
	"wen/internal/llm"
	"wen/internal/modelcfg"
	"wen/internal/plugin"
	"wen/internal/plugin/builtin/dualpersona"
	"wen/internal/plugin/builtin/execcmd"
	"wen/internal/plugin/builtin/heartbeat"
	"wen/internal/plugin/builtin/memory"
	"wen/internal/plugin/builtin/qqbot"
	"wen/internal/plugin/builtin/readfile"
	"wen/internal/plugin/builtin/roleplay"
	"wen/internal/plugin/builtin/scheduler"
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

	// Agent 与插件互相需要：插件在 Agent 之前构造，故各能力都用闭包延迟到实际使用时取值。
	// store 也在其后创建，但必须与 Agent 复用同一个实例——两个 Store 的会话锁互不相识。
	var (
		ag    *agent.Agent
		store *session.Store
	)
	ictx := plugin.InitContext{
		Workdir:    workdir,
		SessionDir: cfg.SessionDir(),
		Complete: func(ctx context.Context, prompt string) (string, error) {
			if ag == nil {
				return "", fmt.Errorf("模型尚未就绪")
			}
			return ag.Complete(ctx, prompt)
		},
		RunTurn: func(ctx context.Context, sessionID, input string) (string, error) {
			if ag == nil {
				return "", fmt.Errorf("模型尚未就绪")
			}
			return ag.RunTurn(ctx, sessionID, input)
		},
		NewSession: func() (string, error) {
			if store == nil {
				return "", fmt.Errorf("会话存储尚未就绪")
			}
			m, err := store.Create()
			return m.ID, err
		},
		Compact: func(ctx context.Context, sessionID string) error {
			if ag == nil {
				return fmt.Errorf("模型尚未就绪")
			}
			return ag.CompactTurn(ctx, sessionID)
		},
	}
	plugins := buildPlugins(cfg, ictx)

	store, err = session.NewStore(cfg.SessionDir())
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

	// 优雅退出：收到中断信号后先关 HTTP，再停掉插件的后台活动（定时器、长连接）
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-rootCtx.Done()
	log.Printf("收到退出信号，正在关闭…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutCtx)
	plugins.StopAll()
}

// needsSetupPlugins 是默认不启用的插件：它们不配置就没法工作——roleplay 没有角色设定
// 就不成其为角色扮演，dual_persona 没有触发词就永远切不过去。默认打开一个空壳只会让人
// 以为功能坏了，所以留给用户配好再开。heartbeat 属于另一类理由：无人值守持续消耗模型
// 额度的功能应当由用户显式打开，而不是装上就开始花钱。
//
// 其余插件用声明的默认参数就能直接工作，因此默认启用。
var needsSetupPlugins = map[string]bool{
	"roleplay":     true,
	"dual_persona": true,
	"heartbeat":    true,
	"qq_bot":       true, // 不填 AppID/AppSecret 没法工作
}

// buildPlugins 注册全部内置系统插件，注册顺序即提示词拼接顺序。
//
// 开关与配置的唯一来源是 <配置目录>/plugins.state.json（由设置页维护）；这里给出的只是
// 首次安装、状态文件还不存在时的初值。
// ictx 中的模型与会话能力在 Agent 建好之前就要传进来，全部是闭包延迟取值。
func buildPlugins(cfg *config.Config, ictx plugin.InitContext) *plugin.Manager {
	m := plugin.NewManager(ictx, filepath.Join(cfg.BaseDir, "plugins.state.json"))
	builtins := []plugin.Plugin{
		readfile.New(), execcmd.New(), webfetch.New(), memory.New(), sessionsearch.New(),
		// roleplay 必须在 dualpersona 之前：表人格设定要排在里人格设定前面，
		// 后者才能形成追加与覆盖的语义
		roleplay.New(), dualpersona.New(),
		heartbeat.New(), scheduler.New(), qqbot.New(),
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
