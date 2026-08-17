// Package memory 提供跨会话长期记忆的系统插件。
//
// 每条记忆是一个带 YAML frontmatter 的 Markdown 文件，可直接用编辑器查看与修改；
// 记忆索引不落盘，由文件头部信息实时生成，避免索引与正文各自漂移。
package memory

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Types 是记忆的分类取值（同时决定索引中的分组顺序）。
var Types = []string{"偏好", "约定", "事实", "踩坑"}

const (
	defaultType = "事实"
	// descMaxRunes 限制钩子长度：索引每一轮对话都会重复发送，单条必须有确定上界。
	descMaxRunes = 40
	// slugMaxRunes 远离各文件系统的长度边界（NTFS 上限为 255 个 UTF-16 单元）。
	slugMaxRunes = 60
)

// typeRank 返回分类的展示序，未知分类排在最后。
func typeRank(t string) int {
	if i := slices.Index(Types, t); i >= 0 {
		return i
	}
	return len(Types)
}

// Entry 是一条记忆。
type Entry struct {
	Slug        string // 文件名（不含扩展名）
	Name        string // 标题
	Description string // 一句话钩子，进索引
	Type        string
	Created     time.Time
	Updated     time.Time
	Content     string // 正文
}

// Store 管理记忆目录。目录内容可被用户在进程外直接修改，因此每次读取都会
// 按目录指纹校验缓存；自身的写入直接置脏，不依赖时间戳精度。
type Store struct {
	mu       sync.RWMutex
	dir      string
	cache    []Entry
	cacheKey uint64
	dirty    bool
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, dirty: true}
}

func (s *Store) Dir() string { return s.dir }

// path 返回 slug 对应的文件路径，并确认它没有逃出记忆目录。
func (s *Store) path(slug string) (string, error) {
	p := filepath.Join(s.dir, slug+".md")
	if filepath.Dir(filepath.Clean(p)) != filepath.Clean(s.dir) {
		return "", fmt.Errorf("非法的记忆名称 %q", slug)
	}
	return p, nil
}

// List 返回全部记忆，按（分类, 标题）稳定排序。目录不存在时返回空列表。
func (s *Store) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]Entry, error) {
	key, names, err := s.scan()
	if err != nil {
		return nil, err
	}
	if !s.dirty && key == s.cacheKey && s.cache != nil {
		return s.cache, nil
	}

	entries := make([]Entry, 0, len(names))
	for _, fname := range names {
		e, err := s.read(fname)
		if err != nil {
			continue // 单个文件损坏不影响其余记忆可用
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := typeRank(entries[i].Type), typeRank(entries[j].Type)
		if ri != rj {
			return ri < rj
		}
		return entries[i].Name < entries[j].Name
	})

	s.cache, s.cacheKey, s.dirty = entries, key, false
	return entries, nil
}

// scan 列出记忆文件并计算目录指纹（文件名 + 大小 + 修改时间）。
func (s *Store) scan() (uint64, []string, error) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	h := fnv.New64a()
	var names []string
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		names = append(names, name)
		fi, err := de.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s|%d|%d\n", name, fi.Size(), fi.ModTime().UnixNano())
	}
	return h.Sum64(), names, nil
}

// Get 按标题或文件名查找一条记忆（大小写不敏感）。
func (s *Store) Get(name string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.listLocked()
	if err != nil {
		return Entry{}, err
	}
	if e, ok := findEntry(entries, name); ok {
		return e, nil
	}
	return Entry{}, fmt.Errorf("没有名为 %q 的记忆", name)
}

// findEntry 依次尝试标题精确匹配与 slug 匹配，均不区分大小写。
// 索引里一条记忆显示为「分类/标题」，模型很自然会把整串当成标题传进来，故先剥掉分类前缀。
func findEntry(entries []Entry, name string) (Entry, bool) {
	name = strings.TrimSpace(name)
	if typ, rest, ok := strings.Cut(name, "/"); ok && slices.Contains(Types, strings.TrimSpace(typ)) {
		name = strings.TrimSpace(rest)
	}
	want := strings.ToLower(name)
	if want == "" {
		return Entry{}, false
	}
	for _, e := range entries {
		if strings.ToLower(e.Name) == want {
			return e, true
		}
	}
	slug, err := slugify(name)
	if err != nil {
		return Entry{}, false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Slug, slug) {
			return e, true
		}
	}
	return Entry{}, false
}

// Save 写入一条记忆。replace 为 false 时拒绝覆盖已存在的同名条目；
// 覆盖前会把原文件另存为 .bak。返回最终落盘的条目。
func (s *Store) Save(e Entry, replace bool) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return Entry{}, fmt.Errorf("记忆标题不能为空")
	}
	slug, err := slugify(e.Name)
	if err != nil {
		return Entry{}, err
	}
	e.Slug = slug
	e.Description = truncateRunes(strings.TrimSpace(collapseSpace(e.Description)), descMaxRunes)
	e.Content = strings.TrimSpace(e.Content)
	if e.Type == "" {
		e.Type = defaultType
	}
	if !slices.Contains(Types, e.Type) {
		return Entry{}, fmt.Errorf("未知的记忆分类 %q（可选：%s）", e.Type, strings.Join(Types, " / "))
	}

	entries, err := s.listLocked()
	if err != nil {
		return Entry{}, err
	}
	old, exists := findEntry(entries, e.Name)
	if exists && !replace {
		return Entry{}, fmt.Errorf("已存在同名记忆 %q，如需覆盖请把 mode 设为 replace", old.Name)
	}

	now := time.Now()
	e.Updated = now
	if exists {
		e.Slug = old.Slug // 沿用原文件名，避免标题大小写变化产生第二个文件
		e.Created = old.Created
	} else {
		e.Created = now
	}
	if e.Created.IsZero() {
		e.Created = now
	}

	p, err := s.path(e.Slug)
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("创建记忆目录失败: %w", err)
	}
	if exists {
		if raw, err := os.ReadFile(p); err == nil {
			_ = os.WriteFile(p+".bak", raw, 0o644)
		}
	}
	if err := writeFileAtomic(p, render(e)); err != nil {
		return Entry{}, err
	}
	s.dirty = true
	return e, nil
}

// Delete 删除一条记忆。
func (s *Store) Delete(name string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.listLocked()
	if err != nil {
		return Entry{}, err
	}
	e, ok := findEntry(entries, name)
	if !ok {
		return Entry{}, fmt.Errorf("没有名为 %q 的记忆", name)
	}
	p, err := s.path(e.Slug)
	if err != nil {
		return Entry{}, err
	}
	if err := os.Remove(p); err != nil {
		return Entry{}, fmt.Errorf("删除记忆失败: %w", err)
	}
	s.dirty = true
	return e, nil
}

// ---------- 文件读写 ----------

// frontmatter 是记忆文件头部的 YAML 结构。时间用字符串承载，格式不合法时退回文件时间而不是报错。
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Created     string `yaml:"created"`
	Updated     string `yaml:"updated"`
}

// read 解析一个记忆文件。缺少或损坏 frontmatter 时按纯 Markdown 兜底，
// 让用户手工丢进目录的普通笔记也能被索引到。
func (s *Store) read(fname string) (Entry, error) {
	p := filepath.Join(s.dir, fname)
	raw, err := os.ReadFile(p)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Entry{}, err
	}

	slug := strings.TrimSuffix(fname, ".md")
	text := normalizeText(raw)
	fm, body := splitFrontmatter(text)

	e := Entry{
		Slug:    slug,
		Name:    slug,
		Type:    defaultType,
		Content: strings.TrimSpace(body),
		Created: fi.ModTime(),
		Updated: fi.ModTime(),
	}
	if fm != nil {
		if fm.Name != "" {
			e.Name = strings.TrimSpace(fm.Name)
		}
		e.Description = strings.TrimSpace(fm.Description)
		if fm.Type != "" {
			e.Type = strings.TrimSpace(fm.Type)
		}
		if t, err := time.Parse(time.RFC3339, fm.Created); err == nil {
			e.Created = t
		}
		if t, err := time.Parse(time.RFC3339, fm.Updated); err == nil {
			e.Updated = t
		}
	}
	if e.Description == "" {
		e.Description = truncateRunes(firstLine(e.Content), descMaxRunes)
	}
	e.Description = truncateRunes(collapseSpace(e.Description), descMaxRunes)
	return e, nil
}

// normalizeText 去掉 UTF-8 BOM 并把行尾统一为 LF（手工编辑的文件多为 CRLF）。
func normalizeText(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// splitFrontmatter 只把首行的 --- 当作围栏起点，正文里的分隔线不会被误判。
// 没有合法围栏时返回 nil 与原文。
func splitFrontmatter(text string) (*frontmatter, string) {
	if !strings.HasPrefix(text, "---\n") {
		return nil, text
	}
	head, body, ok := strings.Cut(text[len("---\n"):], "\n---")
	if !ok {
		return nil, text
	}
	body = strings.TrimPrefix(body, "\n")

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return nil, text
	}
	return &fm, body
}

// render 生成记忆文件内容（LF 行尾）。
func render(e Entry) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(frontmatter{
		Name:        e.Name,
		Description: e.Description,
		Type:        e.Type,
		Created:     e.Created.Format(time.RFC3339),
		Updated:     e.Updated.Format(time.RFC3339),
	})
	_ = enc.Close()
	b.WriteString("---\n\n")
	b.WriteString(e.Content)
	b.WriteString("\n")
	return []byte(b.String())
}

// writeFileAtomic 先写临时文件再改名，避免进程中断留下半截内容。
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("保存记忆失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存记忆失败: %w", err)
	}
	return nil
}

// ---------- 文件名规范化 ----------

// winReserved 是 Windows 上不能作为文件名的设备名（带扩展名同样不可用）。
var winReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// slugify 把标题折成可跨平台安全落盘的文件名：保留字母数字（含中文）与 - _，
// 其余一律折成连字符，再处理 Windows 的保留名与结尾字符限制。
func slugify(name string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	slug := b.String()
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	// Windows 会静默剥掉结尾的点与空格，留着会让两个不同标题指向同一个文件
	slug = strings.Trim(slug, "-_. ")
	slug = truncateRunes(slug, slugMaxRunes)
	slug = strings.Trim(slug, "-_. ")

	if slug == "" {
		return "", fmt.Errorf("记忆标题 %q 不含任何可用字符", name)
	}
	// 落盘时会补上 .md，而 Windows 把 CON.md 一样当成设备名
	if winReserved[strings.ToLower(slug)] {
		slug = "_" + slug
	}
	return slug, nil
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max]), " ")
}

// collapseSpace 把换行与连续空白压成单个空格，保证一条钩子只占索引里的一行。
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
