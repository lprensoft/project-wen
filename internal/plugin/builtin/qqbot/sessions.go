package qqbot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// sessionBinding 维护 openid → 当前会话 ID 的映射，持久化到 StateDir/sessions.json，
// 重启后同一 QQ 用户继续原会话；/new 换绑到新会话。
type sessionBinding struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

func loadBinding(stateDir string) (*sessionBinding, error) {
	b := &sessionBinding{path: filepath.Join(stateDir, "sessions.json"), m: map[string]string{}}
	raw, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &b.m); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *sessionBinding) get(openid string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.m[openid]
}

// usersFor 反查绑定到某会话的全部用户（后台轮次落到该会话时要把结果推给谁）。
func (b *sessionBinding) usersFor(sessionID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for openid, sid := range b.m {
		if sid == sessionID {
			out = append(out, openid)
		}
	}
	return out
}

func (b *sessionBinding) set(openid, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[openid] = sessionID
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(b.m, "", "  ")
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}

// sessionFor 返回该 openid 的当前会话，没有（或已被删除）时新建并落映射。
func (p *Plugin) sessionFor(openid string) (string, error) {
	p.mu.Lock()
	binding, sessions, newSession := p.binding, p.sessions, p.newSession
	p.mu.Unlock()

	if sid := binding.get(openid); sid != "" {
		if _, _, err := sessions.Get(sid); err == nil {
			return sid, nil
		}
		// 会话文件已被删除，重建
	}
	sid, err := newSession()
	if err != nil {
		return "", err
	}
	return sid, binding.set(openid, sid)
}
