package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"wen/internal/plugin"
)

type Config struct {
	Server    ServerConfig                   `yaml:"server"`
	Model     ModelConfig                    `yaml:"model"`
	Providers map[string]ProviderConfig      `yaml:"providers"`
	Agent     AgentConfig                    `yaml:"agent"`
	Session   SessionConfig                  `yaml:"session"`
	Plugins   map[string]plugin.PluginConfig `yaml:"plugins"`

	// BaseDir 是配置文件所在目录，用于解析相对路径（不在 YAML 中）。
	BaseDir string `yaml:"-"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type ModelConfig struct {
	Provider    string  `yaml:"provider"`
	Name        string  `yaml:"name"`
	Temperature float64 `yaml:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"`
	// Thinking 思考模式：off / low / medium / high / xhigh / max
	//（DeepSeek 服务端将 medium、xhigh 归并为 high）。思考开启时 temperature 不生效。
	Thinking string `yaml:"thinking"`
	// ContextLength 模型上下文窗口（token 数），超出预算时裁剪最旧的对话轮次
	ContextLength int `yaml:"context_length"`
}

type ProviderConfig struct {
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type AgentConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
	MaxTurns     int    `yaml:"max_turns"`
	Workdir      string `yaml:"workdir"` // 工具与环境块使用的工作目录，空 = 进程当前目录
}

type SessionConfig struct {
	Dir string `yaml:"dir"`
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8080},
		Model: ModelConfig{
			Provider:      "deepseek",
			Name:          "deepseek-chat",
			Temperature:   0.7,
			MaxTokens:     4096,
			Thinking:      "high",
			ContextLength: 1000000,
		},
		Providers: map[string]ProviderConfig{
			"deepseek": {
				Type:    "openai_compat",
				BaseURL: "https://api.deepseek.com",
				APIKey:  "${DEEPSEEK_API_KEY}",
			},
		},
		Agent: AgentConfig{
			SystemPrompt: "",
			MaxTurns:     20,
		},
		// 系统插件默认全部启用；config.yaml 的 plugins 段可覆盖（yaml 按 key 合并）
		Plugins: map[string]plugin.PluginConfig{
			"read_file":    {Enabled: true},
			"exec_command": {Enabled: true},
			"web_fetch":    {Enabled: true},
		},
	}
}

// ResolvePath 确定配置文件路径：显式 flag > ./config.yaml > ~/.wen/config.yaml。
// 返回路径可能不存在（此时 Load 使用默认配置）。
func ResolvePath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".wen", "config.yaml")
}

// Load 加载配置：读取 path 指向的 YAML（不存在则用默认值）。
// 值中的 ${VAR} 占位符会替换为进程环境变量（api_key 推荐直接写在配置里）。
func Load(path string) (*Config, error) {
	cfg := Default()

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	cfg.BaseDir = filepath.Dir(abs)

	raw, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		expandInPlace(cfg)
		return cfg, cfg.validate()
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(expandEnv(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	expandInPlace(cfg)
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	p, ok := c.Providers[c.Model.Provider]
	if !ok {
		return fmt.Errorf("model.provider %q not found in providers", c.Model.Provider)
	}
	if p.Type != "openai_compat" {
		return fmt.Errorf("provider %q: unsupported type %q (only openai_compat)", c.Model.Provider, p.Type)
	}
	if p.APIKey == "" {
		return fmt.Errorf("provider %q: api_key is empty (set it in config.yaml)", c.Model.Provider)
	}
	if c.Agent.MaxTurns <= 0 {
		c.Agent.MaxTurns = 20
	}
	switch c.Model.Thinking {
	case "":
		c.Model.Thinking = "high"
	case "off", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("model.thinking %q invalid (off/low/medium/high/xhigh/max)", c.Model.Thinking)
	}
	if c.Model.ContextLength <= 0 {
		c.Model.ContextLength = 1000000
	}
	return nil
}

// SessionDir 返回 session 存储目录，未配置时为 <BaseDir>/sessions。
func (c *Config) SessionDir() string {
	if c.Session.Dir != "" {
		if filepath.IsAbs(c.Session.Dir) {
			return c.Session.Dir
		}
		return filepath.Join(c.BaseDir, c.Session.Dir)
	}
	return filepath.Join(c.BaseDir, "sessions")
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv 将文本中的 ${VAR} 替换为环境变量值，未设置的变量替换为空串。
func expandEnv(b []byte) []byte {
	return envPattern.ReplaceAllFunc(b, func(m []byte) []byte {
		name := envPattern.FindSubmatch(m)[1]
		return []byte(os.Getenv(string(name)))
	})
}

// expandInPlace 对默认配置中残留的 ${VAR} 占位符做替换（配置文件缺失或未覆盖该字段时）。
func expandInPlace(c *Config) {
	for name, p := range c.Providers {
		p.APIKey = string(expandEnv([]byte(p.APIKey)))
		p.BaseURL = string(expandEnv([]byte(p.BaseURL)))
		c.Providers[name] = p
	}
}
