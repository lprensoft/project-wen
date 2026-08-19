package session

import (
	"os"
	"time"
)

// 本文件是会话的只读查询。后台类插件需要的只有「把活儿落在哪个会话上」和
// 「我记下的会话还在不在」，为此下发整个会话目录，换来的是全部对话的读写权限。
// 这几个方法让 *Store 直接满足 plugin.SessionQuery，插件不必再拿路径。

// LastActive 返回最近活跃的会话 id：按最近一次真人交互的时间排序，旧会话没有
// 该字段时回落创建时间。一个会话都没有时返回空 id，不算错误。
//
// 第二个返回值是该会话最近一次**真人交互**的时间，零值表示未知。这里只认
// LastActiveAt，不拿 CreatedAt 充数：说不知道，好过把「会话建于何时」当成
// 「上次聊天于何时」交出去。
func (s *Store) LastActive() (string, time.Time, error) {
	metas, err := s.List()
	if err != nil {
		return "", time.Time{}, err
	}
	bestID := ""
	var bestAt, bestActive time.Time
	for _, m := range metas {
		at := m.CreatedAt
		if m.LastActiveAt != nil {
			at = *m.LastActiveAt
		}
		if bestID != "" && !at.After(bestAt) {
			continue
		}
		bestID, bestAt = m.ID, at
		bestActive = time.Time{}
		if m.LastActiveAt != nil {
			bestActive = *m.LastActiveAt
		}
	}
	return bestID, bestActive, nil
}

// LastInteraction 返回所有会话中最近一次真人交互的时间，零值表示从未有过。
// 与 LastActive 的区别是它不挑会话，只回答「上一次有人来过是什么时候」——
// 空闲判定要的是这个，而一个刚被创建的会话并不代表有人来过。
func (s *Store) LastInteraction() (time.Time, error) {
	metas, err := s.List()
	if err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, m := range metas {
		if m.LastActiveAt != nil && m.LastActiveAt.After(last) {
			last = *m.LastActiveAt
		}
	}
	return last, nil
}

// Exists 判断会话是否仍然存在。只看文件在不在，不解析内容——问的是
// 「我记着的这个会话还在吗」，为一个布尔值把整个 jsonl 读一遍是白费。
func (s *Store) Exists(id string) bool {
	p, err := s.path(id)
	if err != nil {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
