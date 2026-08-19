package wechatbot

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"wen/internal/plugin"
)

// iLink 发消息必须回带 context_token，主动推送没有「本轮入站消息」可回带——
// 只能用该用户最近一次入站消息的 token。tokenStore 按用户记住它并持久化
// （StateDir/tokens.json，0600：token 等同于向该用户发消息的通行证），
// 重启后心跳结果仍能推送出去。token 由微信侧签发，有效期未知，失效按发送失败容忍。
type tokenStore struct {
	mu   sync.Mutex
	path string
	m    map[string]string // 用户 ID → 最近一次入站消息的 context_token
}

func loadTokens(stateDir string) (*tokenStore, error) {
	s := &tokenStore{path: filepath.Join(stateDir, "tokens.json"), m: map[string]string{}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *tokenStore) remember(userID, token string) {
	if userID == "" || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[userID] == token {
		return
	}
	s.m[userID] = token
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Printf("wechat_bot: 保存推送票据失败: %v", err)
		return
	}
	raw, _ := json.MarshalIndent(s.m, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("wechat_bot: 保存推送票据失败: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("wechat_bot: 保存推送票据失败: %v", err)
	}
}

func (s *tokenStore) get(userID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[userID]
}

// OnTurnEnd 观察每轮对话的结束：后台轮次（心跳、定时任务等其他插件发起）落在
// 绑定到微信用户的会话上时，把助手的最终文本推送过去——否则结果只写进会话文件，
// 对面的人永远看不到。前台轮次与本插件自己发起的轮次不推：它们各有回复渠道。
//
// 广播发生在轮次收尾的同步路径上，发送工作放 goroutine；不能用广播的 ctx——
// 发起方（如心跳）在轮次结束后立即取消它。
func (p *Plugin) OnTurnEnd(_ context.Context, ev plugin.TurnEndEvent) {
	if ev.Origin == "" || ev.Origin == p.Name() {
		return
	}
	if strings.TrimSpace(ev.FinalText) == "" {
		return
	}

	p.mu.Lock()
	if p.cancel == nil { // 已停止
		p.mu.Unlock()
		return
	}
	core, tokens, pctx := p.core, p.tokens, p.ctx
	users := core.UsersFor(ev.SessionID)
	if len(users) == 0 {
		p.mu.Unlock()
		return
	}
	p.wg.Add(len(users))
	p.mu.Unlock()

	for _, userID := range users {
		go func(userID string) {
			defer p.wg.Done()
			token := tokens.get(userID)
			if token == "" {
				// 该用户还没发过消息（或票据文件丢失），没有可用的 context_token
				log.Printf("wechat_bot: 无法把后台轮次结果推给 %s：缺少 context_token（对方先发一条消息后即可）", userID)
				return
			}
			p.send(pctx, userID, ev.FinalText, token)
		}(userID)
	}
}
