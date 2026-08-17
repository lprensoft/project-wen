package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-default")

	cfg, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8080 || cfg.Model.Name != "deepseek-chat" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "sk-test-default" {
		t.Errorf("api_key env expansion failed, got %q", got)
	}
}

func TestLoadYAMLWithInlineKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
server:
  port: 9999
model:
  provider: deepseek
  name: deepseek-reasoner
providers:
  deepseek:
    type: openai_compat
    base_url: https://api.deepseek.com
    api_key: sk-inline-key
`), 0o644)

	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Model.Name != "deepseek-reasoner" {
		t.Errorf("model = %q", cfg.Model.Name)
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "sk-inline-key" {
		t.Errorf("api_key = %q, want inline value", got)
	}
	// 未覆盖的字段保留默认值
	if cfg.Model.Temperature != 0.7 || cfg.Agent.MaxTurns != 20 {
		t.Errorf("unset fields lost defaults: %+v", cfg)
	}
}

func TestLoadYAMLWithEnvVarPlaceholder(t *testing.T) {
	t.Setenv("WEN_TEST_KEY", "sk-from-env")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
providers:
  deepseek:
    type: openai_compat
    base_url: https://api.deepseek.com
    api_key: ${WEN_TEST_KEY}
`), 0o644)

	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "sk-from-env" {
		t.Errorf("api_key = %q, want value from env var", got)
	}
}

func TestValidateRejectsMissingProvider(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
model:
  provider: nope
`), 0o644)
	if _, err := Load(filepath.Join(dir, "config.yaml")); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestValidateAcceptsAnthropicType(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
model:
  provider: claude
providers:
  claude:
    type: anthropic
    base_url: https://api.anthropic.com
    api_key: sk-ant-x
`), 0o644)
	if _, err := Load(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("anthropic type should be accepted: %v", err)
	}
}

func TestValidateRejectsUnknownType(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`
model:
  provider: local
providers:
  local:
    type: ollama
    base_url: http://127.0.0.1:11434
    api_key: x
`), 0o644)
	if _, err := Load(filepath.Join(dir, "config.yaml")); err == nil {
		t.Fatal("expected error for unsupported provider type")
	}
}

func TestSessionDirDefault(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-x")
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "sessions")
	if got := cfg.SessionDir(); got != want {
		t.Errorf("SessionDir = %q, want %q", got, want)
	}
}
