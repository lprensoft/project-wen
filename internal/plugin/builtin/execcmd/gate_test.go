package execcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wen/internal/plugin"
)

func newTestPlugin(t *testing.T, cfg map[string]any) (*Plugin, string) {
	t.Helper()
	dir := t.TempDir()
	p := New()
	if err := p.Init(plugin.InitContext{Workdir: dir}, cfg); err != nil {
		t.Fatal(err)
	}
	return p, dir
}

// runCmd 执行一条命令，返回结果与错误。
func runCmd(p *Plugin, ctx context.Context, command string) (string, error) {
	args, _ := json.Marshal(map[string]string{"command": command})
	return (&tool{p: p}).Execute(ctx, args)
}

// confirmer 返回一个总是给出 approved 的确认通道，并记录收到的请求。
func confirmer(approved bool, got *plugin.ConfirmRequest) plugin.ConfirmFunc {
	return func(_ context.Context, req plugin.ConfirmRequest) (bool, error) {
		if got != nil {
			*got = req
		}
		return approved, nil
	}
}

// harmless 是一条在两个平台都能跑通、且不会被判危险的命令。
func harmless() string {
	if runtime.GOOS == "windows" {
		return "echo ok"
	}
	return "echo ok"
}

func TestAllowRunsWithoutConfirmation(t *testing.T) {
	p, _ := newTestPlugin(t, nil)
	asked := false
	ctx := plugin.WithConfirmer(context.Background(),
		func(context.Context, plugin.ConfirmRequest) (bool, error) { asked = true; return true, nil })

	out, err := runCmd(p, ctx, harmless())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("命令未执行: %q", out)
	}
	if asked {
		t.Error("普通命令不该打扰用户")
	}
}

func TestDeniedCommandNeverAsksAndNeverRuns(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	target := filepath.Join(dir, "sentinel.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	asked := false
	ctx := plugin.WithConfirmer(context.Background(),
		func(context.Context, plugin.ConfirmRequest) (bool, error) { asked = true; return true, nil })

	_, err := runCmd(p, ctx, "format c:")
	if err == nil {
		t.Fatal("极高危命令应被直接拒绝")
	}
	if asked {
		t.Error("这一档不该问用户——把这种问题摆到人面前本身就是一次误点击的机会")
	}
	// 错误文本会回给模型，要说清别绕过
	if !strings.Contains(err.Error(), "不要尝试绕过") {
		t.Errorf("错误文本应劝阻绕过: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Error("被拒绝的命令不该产生任何副作用")
	}
}

func TestConfirmApprovedRuns(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got plugin.ConfirmRequest
	ctx := plugin.WithConfirmer(context.Background(), confirmer(true, &got))
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	if _, err := runCmd(p, ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Error("同意后命令应真的执行")
	}
	// 卡片上要看到命令原文与原因，否则用户没法判断
	if got.Detail != cmd || got.Reason == "" || got.Source != "exec_command" {
		t.Errorf("确认请求内容不全: %+v", got)
	}
}

func TestConfirmDeniedDoesNotRun(t *testing.T) {
	p, dir := newTestPlugin(t, nil)
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := plugin.WithConfirmer(context.Background(), confirmer(false, nil))
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	_, err := runCmd(p, ctx, cmd)
	if err == nil {
		t.Fatal("被拒绝时应返回错误")
	}
	if !strings.Contains(err.Error(), "不要改写命令重试") {
		t.Errorf("错误文本应劝阻重试: %v", err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Error("被拒绝的命令不该执行")
	}
}

func TestConfirmErrorTreatedAsDenial(t *testing.T) {
	// 拿不到答复不等于得到许可
	p, dir := newTestPlugin(t, nil)
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o644)

	ctx := plugin.WithConfirmer(context.Background(),
		func(context.Context, plugin.ConfirmRequest) (bool, error) {
			return true, fmt.Errorf("连接断开") // 即使 approved 为 true，有 error 就不算同意
		})
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	if _, err := runCmd(p, ctx, cmd); err == nil {
		t.Fatal("确认未完成时应按拒绝处理")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("确认未完成时命令不该执行")
	}
}

func TestUnattendedDefaultsToDeny(t *testing.T) {
	p, dir := newTestPlugin(t, nil) // 没有 confirmer 的 ctx
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o644)

	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	_, err := runCmd(p, context.Background(), cmd)
	if err == nil {
		t.Fatal("无人值守时需确认的命令默认应拒绝")
	}
	if !strings.Contains(err.Error(), "没有可交互的界面") {
		t.Errorf("错误文本应说明原因: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("命令不该执行")
	}
}

func TestNoUnattendedEscapeHatch(t *testing.T) {
	// 刻意不提供「无人值守就放行」的开关：拿不到答复不等于得到许可。
	// 唯一的放行方式是把 guard 关掉，那是一个看得见的选择。
	for _, f := range New().ConfigFields() {
		if f.Key == "on_unattended" {
			t.Error("不该有无人值守放行开关")
		}
	}
	p, dir := newTestPlugin(t, nil)
	os.WriteFile(filepath.Join(dir, "victim.txt"), []byte("x"), 0o644)
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	if _, err := runCmd(p, context.Background(), cmd); err == nil {
		t.Error("无 confirmer 时应拒绝")
	}
}

func TestGuardOffSkipsConfirmation(t *testing.T) {
	p, dir := newTestPlugin(t, map[string]any{"guard": guardOff})
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o644)

	asked := false
	ctx := plugin.WithConfirmer(context.Background(),
		func(context.Context, plugin.ConfirmRequest) (bool, error) { asked = true; return false, nil })
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	if _, err := runCmd(p, ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Error("关闭拦截后不该请求确认")
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Error("关闭拦截后命令应直接执行")
	}
}

func TestConfirmTimeoutIsBounded(t *testing.T) {
	p, _ := newTestPlugin(t, map[string]any{"confirm_timeout_seconds": 10})
	if got := p.snapshot().confirmTimeout; got != 10*time.Second {
		t.Errorf("确认超时 = %v, want 10s", got)
	}

	// 用一个永不回答的 confirmer 验证超时真的会解除阻塞
	q, dir := newTestPlugin(t, nil)
	q.mu.Lock()
	q.confirmTimeout = 50 * time.Millisecond
	q.mu.Unlock()
	os.WriteFile(filepath.Join(dir, "victim.txt"), []byte("x"), 0o644)

	ctx := plugin.WithConfirmer(context.Background(),
		func(c context.Context, _ plugin.ConfirmRequest) (bool, error) {
			<-c.Done() // 模拟用户一直不回应
			return false, c.Err()
		})
	cmd := "del victim.txt"
	if runtime.GOOS != "windows" {
		cmd = "rm victim.txt"
	}
	done := make(chan error, 1)
	go func() { _, err := runCmd(q, ctx, cmd); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("超时应按拒绝处理")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时没有解除阻塞，工具永久挂住了")
	}
}

func TestSystemPromptOnlyWhenGuarded(t *testing.T) {
	// 不告诉模型这条规则的话，被拒绝的模型会去改写命令重试
	p, _ := newTestPlugin(t, nil)
	if !strings.Contains(p.SystemPrompt(), "不要改写命令绕过") {
		t.Errorf("开启拦截时应注入规则:\n%s", p.SystemPrompt())
	}
	q, _ := newTestPlugin(t, map[string]any{"guard": guardOff})
	if q.SystemPrompt() != "" {
		t.Errorf("关闭拦截后不该注入:\n%s", q.SystemPrompt())
	}
}

func TestLongCommandTruncatedInRequest(t *testing.T) {
	p, _ := newTestPlugin(t, nil)
	long := "rm -rf " + strings.Repeat("路径", confirmDetailMaxRunes)
	var got plugin.ConfirmRequest
	ctx := plugin.WithConfirmer(context.Background(), confirmer(false, &got))
	runCmd(p, ctx, long)

	if n := len([]rune(got.Detail)); n > confirmDetailMaxRunes+40 {
		t.Errorf("确认卡片里的命令原文过长: %d 字", n)
	}
	if !strings.Contains(got.Detail, "已截断") {
		t.Error("截断应有说明")
	}
}

func TestConfigFieldsValidate(t *testing.T) {
	fields := New().ConfigFields()
	if _, err := plugin.NormalizeConfig(fields, nil); err != nil {
		t.Fatalf("默认配置无法通过校验: %v", err)
	}
	values, _ := plugin.NormalizeConfig(fields, nil)
	if values["guard"] != guardDangerous {
		t.Errorf("默认应为「危险命令需确认」，得到 %v", values["guard"])
	}
}
