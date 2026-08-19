package session

import (
	"testing"
	"time"
)

func newQueryStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 一个会话都没有时返回空 id，不是错误——调用方据此决定要不要新建。
func TestQueryEmptyStore(t *testing.T) {
	s := newQueryStore(t)
	id, at, err := s.LastActive()
	if err != nil || id != "" || !at.IsZero() {
		t.Errorf("LastActive = %q, %v, %v", id, at, err)
	}
	last, err := s.LastInteraction()
	if err != nil || !last.IsZero() {
		t.Errorf("LastInteraction = %v, %v", last, err)
	}
	if s.Exists("nope") {
		t.Error("不存在的会话不该报 Exists")
	}
}

// LastActive 与 LastInteraction 问的不是同一件事：前者挑会话（缺交互时间回落创建
// 时间），后者只答「上一次有人来过是什么时候」。心跳两个都要——落点用前者，
// 空闲衰减用后者，把刚创建的空会话当成「有人来过」会让衰减永远不触发。
func TestLastActiveFallsBackToCreation(t *testing.T) {
	s := newQueryStore(t)
	old, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	chatted := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := s.SetLastActive(old.ID, chatted); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Create() // 刚建的空会话，没人聊过
	if err != nil {
		t.Fatal(err)
	}

	id, at, err := s.LastActive()
	if err != nil {
		t.Fatal(err)
	}
	if id != fresh.ID {
		t.Errorf("LastActive 应回落到创建时间最新的会话，得到 %q", id)
	}
	if !at.IsZero() {
		t.Errorf("没人聊过的会话，交互时间应为零值而不是创建时间，得到 %v", at)
	}

	last, err := s.LastInteraction()
	if err != nil {
		t.Fatal(err)
	}
	if !last.Equal(chatted) {
		t.Errorf("LastInteraction = %v, want %v", last, chatted)
	}
}

// 有真人交互的会话胜出，并把它自己的交互时间一并交出。
func TestLastActivePrefersInteraction(t *testing.T) {
	s := newQueryStore(t)
	if _, err := s.Create(); err != nil {
		t.Fatal(err)
	}
	target, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	chatted := time.Now().Add(time.Minute).Truncate(time.Second)
	if err := s.SetLastActive(target.ID, chatted); err != nil {
		t.Fatal(err)
	}

	id, at, err := s.LastActive()
	if err != nil {
		t.Fatal(err)
	}
	if id != target.ID || !at.Equal(chatted) {
		t.Errorf("LastActive = %q, %v, want %q, %v", id, at, target.ID, chatted)
	}
}

func TestExists(t *testing.T) {
	s := newQueryStore(t)
	m, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Exists(m.ID) {
		t.Error("刚创建的会话应存在")
	}
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if s.Exists(m.ID) {
		t.Error("删掉的会话不该还存在")
	}
	// 非法 id 不该被当成路径拼进去
	if s.Exists("../../etc/passwd") {
		t.Error("非法 id 应直接判为不存在")
	}
}
