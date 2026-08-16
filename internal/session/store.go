package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store 以 JSONL 文件形式管理 session：<dir>/<id>.jsonl，
// 第一行为 Meta，其后每行一条 StoredMessage。
type Store struct {
	dir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // 每 session 一把锁，防并发写
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

func (s *Store) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

func (s *Store) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

// Create 新建 session，返回其 Meta。ID 为时间戳+随机后缀，天然按时间排序。
func (s *Store) Create() (Meta, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return Meta{}, err
	}
	now := time.Now()
	meta := Meta{
		Type:      "meta",
		ID:        now.Format("20060102-150405") + "-" + hex.EncodeToString(buf),
		CreatedAt: now,
	}
	p, err := s.path(meta.ID)
	if err != nil {
		return Meta{}, err
	}
	line, _ := json.Marshal(meta)
	if err := os.WriteFile(p, append(line, '\n'), 0o644); err != nil {
		return Meta{}, fmt.Errorf("create session: %w", err)
	}
	return meta, nil
}

// List 返回全部 session 的 Meta，按创建时间倒序（新的在前）。
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		m, err := s.readMeta(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // 跳过损坏文件，不让单个坏文件拖垮列表
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID > metas[j].ID })
	return metas, nil
}

func (s *Store) readMeta(p string) (Meta, error) {
	f, err := os.Open(p)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return Meta{}, fmt.Errorf("empty session file %s", p)
	}
	var m Meta
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m.Type != "meta" {
		return Meta{}, fmt.Errorf("bad meta line in %s", p)
	}
	return m, nil
}

// Get 返回指定 session 的 Meta 与全部消息。
func (s *Store) Get(id string) (Meta, []StoredMessage, error) {
	p, err := s.path(id)
	if err != nil {
		return Meta{}, nil, err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()

	f, err := os.Open(p)
	if err != nil {
		return Meta{}, nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var meta Meta
	var msgs []StoredMessage
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if first {
			first = false
			if err := json.Unmarshal(line, &meta); err != nil {
				return Meta{}, nil, fmt.Errorf("bad meta line: %w", err)
			}
			continue
		}
		var m StoredMessage
		if err := json.Unmarshal(line, &m); err != nil {
			continue // 跳过损坏行
		}
		msgs = append(msgs, m)
	}
	return meta, msgs, sc.Err()
}

// Append 追加一条消息。
func (s *Store) Append(id string, msg StoredMessage) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()

	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("session %s: %w", id, err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// SetTitle 更新 meta 行的标题（整体重写文件，原子替换）。
func (s *Store) SetTitle(id, title string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()

	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	idx := strings.IndexByte(string(raw), '\n')
	if idx < 0 {
		idx = len(raw)
	}
	var meta Meta
	if err := json.Unmarshal(raw[:idx], &meta); err != nil {
		return fmt.Errorf("bad meta line: %w", err)
	}
	meta.Title = title
	line, _ := json.Marshal(meta)

	tmp := p + ".tmp"
	out := append(line, raw[idx:]...)
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Replace 用给定消息整体替换 session 内容（meta 行保留，原子写入）。
func (s *Store) Replace(id string, msgs []StoredMessage) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(p)
	if err != nil {
		return err
	}
	var buf []byte
	line, _ := json.Marshal(meta)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	for _, m := range msgs {
		line, err := json.Marshal(m)
		if err != nil {
			return err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete 删除 session 文件。
func (s *Store) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	if err := os.Remove(p); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.locks, id)
	s.mu.Unlock()
	return nil
}
