// 版本资源与程序图标：改动 internal/version 或 logo 图源后执行 go generate ./cmd/wen
// 重新生成 .syso（生成器说明见 tools/genwinres 与 tools/genicon），go build 会自动
// 链入本目录的 .syso。两条指令有先后依赖——genicon 先出 .ico，genwinres 才编得进去。
//go:generate go run wen/tools/genicon
//go:generate go run wen/tools/genwinres

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
	"wen/internal/plugin/builtin/bodysense"
	"wen/internal/plugin/builtin/dualpersona"
	"wen/internal/plugin/builtin/execcmd"
	"wen/internal/plugin/builtin/heartbeat"
	"wen/internal/plugin/builtin/larkbot"
	"wen/internal/plugin/builtin/memory"
	"wen/internal/plugin/builtin/mood"
	"wen/internal/plugin/builtin/qqbot"
	"wen/internal/plugin/builtin/readfile"
	"wen/internal/plugin/builtin/roleplay"
	"wen/internal/plugin/builtin/scene"
	"wen/internal/plugin/builtin/scheduler"
	"wen/internal/plugin/builtin/sessionsearch"
	"wen/internal/plugin/builtin/skills"
	"wen/internal/plugin/builtin/telegrambot"
	"wen/internal/plugin/builtin/weather"
	"wen/internal/plugin/builtin/webfetch"
	"wen/internal/plugin/builtin/wechatbot"
	"wen/internal/runlock"
	"wen/internal/server"
	"wen/internal/session"
	"wen/internal/version"
)

// runServe 启动服务。这是没有子命令时的默认动作，也是 wen serve 的实现。
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("c", "", "配置文件路径（默认 ./config.yaml 或 ~/.wen/config.yaml）")
	port := fs.Int("p", 0, "监听端口（覆盖配置文件）")
	_ = fs.Parse(args)

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
	// store 是例外，它只依赖配置，因此提前建好直接交给插件——插件的 Init 就要用它
	// （心跳启动时要读上次真人交互的时间）。整个进程共用这一个实例：两个 Store 的
	// 会话锁互不相识。
	var (
		ag      *agent.Agent
		models  *modelcfg.Store
		plugins *plugin.Manager
	)
	store, err := session.NewStore(cfg.SessionDir())
	if err != nil {
		log.Fatalf("初始化 session 存储失败: %v", err)
	}

	ictx := plugin.InitContext{
		Workdir:    workdir,
		Sessions:   store,
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
		Notice: func(ctx context.Context, sessionID, text string) error {
			if ag == nil {
				return fmt.Errorf("会话尚未就绪")
			}
			return ag.AppendNotice(ctx, sessionID, text)
		},
		NewSession: func() (string, error) {
			m, err := store.Create()
			return m.ID, err
		},
		Compact: func(ctx context.Context, sessionID string) error {
			if ag == nil {
				return fmt.Errorf("模型尚未就绪")
			}
			return ag.CompactTurn(ctx, sessionID)
		},
		// 与 server 的 GET /api/status 同源，保证远端界面与 Web UI 的状态输出一致
		Status: func(sessionID string) (plugin.StatusInfo, error) {
			if models == nil {
				return plugin.StatusInfo{}, fmt.Errorf("状态尚未就绪")
			}
			provider, model, thinking, contextLength := models.Status()
			info := plugin.StatusInfo{
				Version:  version.Version,
				Provider: provider, Model: model, Thinking: thinking,
				ContextLength: contextLength, MeasuredTokens: -1,
			}
			if plugins != nil {
				info.PluginLines = plugins.StatusLines()
			}
			if sessionID == "" {
				return info, nil
			}
			meta, msgs, err := store.Get(sessionID)
			if err != nil {
				return info, nil // 会话不存在只影响会话部分
			}
			info.HasSession = true
			info.MessageCount = len(msgs)
			info.EstTokens = agent.EstimateStoredTokens(msgs)
			if meta.LastUsage != nil {
				info.MeasuredTokens = meta.LastUsage.PromptTokens + meta.LastUsage.CompletionTokens
				info.CachedTokens = meta.LastUsage.CachedTokens
				info.CacheWriteTokens = meta.LastUsage.CacheWriteTokens
				info.PromptTokens = meta.LastUsage.PromptTokens
			}
			return info, nil
		},
	}
	plugins = buildPlugins(cfg, ictx)

	// 模型配置：config.yaml 提供初始值，界面上的改动存 models.json 并优先生效
	models, err = modelcfg.NewStore(filepath.Join(cfg.BaseDir, "models.json"), cfg)
	if err != nil {
		log.Fatalf("加载模型配置失败: %v", err)
	}
	cur, err := models.Resolve()
	if err != nil {
		log.Fatalf("模型配置无效: %v", err)
	}
	provider, err := llm.New(llm.Config{
		Type: cur.Type, BaseURL: cur.BaseURL, APIKey: cur.APIKey, Dialect: cur.Dialect,
		PromptCache: cur.PromptCache,
	})
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

	auth, err := server.NewAuthStore(cfg.BaseDir)
	if err != nil {
		log.Fatalf("加载访问口令失败: %v", err)
	}

	// 启动守卫：配了对外监听却没设口令时，降级为只监听回环而不是拒绝启动。
	// 拒绝启动在新装的远程机器上是个死局——口令要在界面里设，而界面起不来。
	// 降级同样一次都没暴露过，且 SSH 隧道进来就能把口令补上。
	exposed := !server.IsLoopbackHost(cfg.Server.Host)
	if exposed && !auth.HasPassword() {
		log.Printf("⚠ 配置的监听地址是 %s，但尚未设置访问口令，已降级为只监听 127.0.0.1。", cfg.Server.Host)
		log.Printf("  设置口令后重启即可对外提供服务：在本机执行 wen config server，")
		log.Printf("  或用 ssh -L %d:127.0.0.1:%d <用户>@<服务器> 建隧道后在设置页的「访问控制」里设置。",
			cfg.Server.Port, cfg.Server.Port)
		cfg.Server.Host = "127.0.0.1"
		exposed = false
	}

	srv := server.New(ag, store, plugins, models, server.Options{
		Auth:          auth,
		TrustLoopback: cfg.Server.TrustLoopbackOrDefault(),
		Exposed:       exposed,
	})
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
	log.Printf("访问控制: %s", authSummary(auth, exposed, cfg.Server.TrustLoopbackOrDefault()))
	log.Printf("Wen Agent %s 已启动: http://%s", version.Version, addr)

	// 登记运行中的实例，供 wen config 判定该走在线还是离线模式。
	// 失败只是让配置工具退回离线模式，不该拦住服务启动。
	if prev, ok := runlock.Read(cfg.BaseDir); ok && reachable(prev.Addr) {
		log.Printf("警告: 同一配置目录下已有实例在 %s 运行。两个实例共用一份状态文件会互相覆盖配置。", prev.Addr)
	}
	releaseLock, err := runlock.Acquire(cfg.BaseDir, runlock.Info{
		PID: os.Getpid(), Addr: addr, Version: version.Version,
		Started: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		log.Printf("提示: 无法登记运行状态（%v），wen config 将只能离线修改配置", err)
	}
	defer releaseLock()

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

// authSummary 把访问控制的实际状态写成一行，供启动日志使用。
// 「配了什么」和「实际生效什么」可能不一致（降级、反代关掉回环免认证），
// 所以这里说的是生效结果。
func authSummary(auth *server.AuthStore, exposed, trustLoopback bool) string {
	switch {
	case !auth.HasPassword():
		return "未设置口令，仅本机可访问"
	case !trustLoopback:
		return "已设置口令，所有来源（含本机）都需登录"
	case exposed:
		return "已设置口令，本机免登录、其它来源需登录"
	default:
		return "已设置口令，但当前只监听本机，不会被外部访问"
	}
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
	"scene":        true, // 不配置也能工作，但它依赖默认关闭的 roleplay，默认启用只会在启动时被强制关闭
	"body_sense":   true, // 同上：自带默认部位表，开箱即用，进这张表只因为它依赖默认关闭的 roleplay
	"mood":         true, // 同上：默认参数就能工作，进这张表只因为它依赖默认关闭的 roleplay
	"weather":      true, // 两个理由都占：依赖默认关闭的 roleplay，且不填城市就查不了天气

	"heartbeat":    true,
	"skills":       true, // 技能目录是空的，开着只会多两个用不上的工具
	"qq_bot":       true, // 不填 AppID/AppSecret 没法工作
	"wechat_bot":   true, // 不扫码绑定没法工作
	"feishu_bot":   true, // 不填 App ID/Secret 没法工作
	"lark_bot":     true, // 同上
	"telegram_bot": true, // 不填 Bot Token 没法工作
}

// buildPlugins 注册全部内置系统插件，注册顺序即提示词拼接顺序。
//
// 开关与配置的唯一来源是 <配置目录>/plugins.state.json（由设置页维护）；这里给出的只是
// 首次安装、状态文件还不存在时的初值。
// ictx 中的模型与会话能力在 Agent 建好之前就要传进来，全部是闭包延迟取值。
func buildPlugins(cfg *config.Config, ictx plugin.InitContext, opts ...plugin.Option) *plugin.Manager {
	m := plugin.NewManager(ictx, filepath.Join(cfg.BaseDir, "plugins.state.json"), opts...)
	builtins := []plugin.Plugin{
		readfile.New(), execcmd.New(), webfetch.New(),
		// roleplay 必须在 dualpersona 之前：表人格设定要排在里人格设定前面，
		// 后者才能形成追加与覆盖的语义；scene 的舞台设定排在人格设定之后——先立角色，再立舞台，
		// weather 紧跟 scene：它讲的是舞台之外那个现实地方的天气，与舞台冲突时以舞台为准，
		// 这条规则得排在舞台设定之后才读得通；
		// body_sense 再排在其后：身体感知要有角色与场景在先，才有作用对象
		roleplay.New(), dualpersona.New(), scene.New(), weather.New(), bodysense.New(), mood.New(),
		// memory 与 session_search 排在角色演绎那组之后。它们注入的是「什么该记下来」
		// 这类能力判据，与 scene / mood / weather 的判据同一类，挨在一起模型才会同等对待。
		// 早先它们排在最前面，落在 [角色设定 · 最高优先级] 声明之前，那句「以下设定优先于
		// 其它一般性指令」正好把记忆判据归进了被压过的那一类——实测同一批对话里
		// adjust_mood 与 record_touch 都被调用过，save_memory 一次也没有。
		// 顺带的可见后果：设置页上「角色演绎」这一节因此排到了「记忆与检索」之前。
		memory.New(), sessionsearch.New(),
		// skills 同理排在角色演绎那组之后：它注入的「遇到相符的任务先读手册」也是一条
		// 能力判据，落在 [角色设定 · 最高优先级] 之前会被那句「以下设定优先于其它一般性
		// 指令」一并压过去，成为可读可不读的建议。
		skills.New(),
		heartbeat.New(), scheduler.New(),
		// 飞书与 Lark 是同一套实现的两次实例化（见 larkbot 包注释）：同一套 API 的两个
		// 租户域，凭证互不通用，分成两个插件才能同时连两边
		qqbot.New(), wechatbot.New(),
		larkbot.NewFeishu(), larkbot.NewLark(), telegrambot.New(),
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
