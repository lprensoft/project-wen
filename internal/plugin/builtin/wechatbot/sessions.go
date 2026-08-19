package wechatbot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionBinding 维护微信用户 ID → 当前会话 ID 的映射，持久化到 StateDir/sessions.json，
// 重启后同一用户继续原会话；/new 换绑到新会话。
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

func (b *sessionBinding) get(userID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.m[userID]
}

// usersFor 反查绑定到某会话的全部用户（后台轮次落到该会话时要把结果推给谁）。
func (b *sessionBinding) usersFor(sessionID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for userID, sid := range b.m {
		if sid == sessionID {
			out = append(out, userID)
		}
	}
	return out
}

func (b *sessionBinding) set(userID, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[userID] = sessionID
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

// sessionFor 返回该用户的当前会话，没有（或已被删除）时新建并落映射。
func (p *Plugin) sessionFor(userID string) (string, error) {
	p.mu.Lock()
	binding, sessions, newSession := p.binding, p.sessions, p.newSession
	p.mu.Unlock()

	if sid := binding.get(userID); sid != "" {
		if sessions.Exists(sid) {
			return sid, nil
		}
		// 会话文件已被删除，重建
	}
	sid, err := newSession()
	if err != nil {
		return "", err
	}
	return sid, binding.set(userID, sid)
}

// deduper 按消息 ID 去重（5 分钟窗口），防止长轮询重投或游标回绕导致重复处理。
type deduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

const (
	dedupWindow  = 5 * time.Minute
	dedupMaxSize = 1000
)

func newDeduper() *deduper { return &deduper{seen: map[string]time.Time{}} }

// isDuplicate 判定并记录一条消息；重复返回 true。
func (d *deduper) isDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if len(d.seen) > dedupMaxSize {
		for id, ts := range d.seen {
			if now.Sub(ts) > dedupWindow {
				delete(d.seen, id)
			}
		}
	}
	if ts, ok := d.seen[msgID]; ok && now.Sub(ts) <= dedupWindow {
		return true
	}
	d.seen[msgID] = now
	return false
}
