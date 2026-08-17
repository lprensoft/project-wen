package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("记忆功能尚未就绪")

// ---------- list_memories ----------

type listTool struct{ p *Plugin }

func (t *listTool) Name() string { return "list_memories" }

func (t *listTool) Description() string {
	return "列出已保存的记忆条目（标题、分类、创建日期与摘要）。" +
		"可按关键词或分类过滤，用于索引未完整列出、或需要按时间翻找早期记忆时。"
}

func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"keyword": {"type": "string", "description": "过滤关键词，对标题、摘要与正文做包含匹配；留空表示不过滤"},
			"type": {"type": "string", "description": "按分类过滤", "enum": ["偏好", "约定", "事实", "踩坑"]}
		}
	}`)
}

func (t *listTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Keyword string `json:"keyword"`
		Type    string `json:"type"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}
	entries, err := s.store.List()
	if err != nil {
		return "", err
	}

	matched := filterEntries(entries, a.Keyword, a.Type)
	if len(matched) == 0 {
		if len(entries) == 0 {
			return "还没有保存任何记忆。", nil
		}
		return fmt.Sprintf("共 %d 条记忆，没有符合条件的。", len(entries)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 条记忆", len(entries))
	if len(matched) != len(entries) {
		fmt.Fprintf(&b, "，其中 %d 条符合条件", len(matched))
	}
	b.WriteString("：\n")
	for _, e := range matched {
		fmt.Fprintf(&b, "- %s %s/%s", e.Created.Format("2006-01-02"), e.Type, e.Name)
		if e.Description != "" {
			b.WriteString(" — ")
			b.WriteString(e.Description)
		}
		b.WriteString("\n")
	}
	out, truncated := plugin.TruncateUTF8([]byte(strings.TrimRight(b.String(), "\n")), s.maxEntryBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[超过 %d 字节，已截断，请用更具体的关键词过滤]", s.maxEntryBytes)
	}
	return res, nil
}

// filterEntries 做大小写不敏感的包含匹配。这里刻意不做相关度打分：
// 打分在中英混排的短文本上噪音很大，而包含与否是确定的。
func filterEntries(entries []Entry, keyword, typ string) []Entry {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	typ = strings.TrimSpace(typ)
	if kw == "" && typ == "" {
		return entries
	}
	var out []Entry
	for _, e := range entries {
		if typ != "" && e.Type != typ {
			continue
		}
		if kw != "" {
			hay := strings.ToLower(e.Name + "\n" + e.Description + "\n" + e.Content)
			if !strings.Contains(hay, kw) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// ---------- save_memory ----------

type saveTool struct{ p *Plugin }

func (t *saveTool) Name() string { return "save_memory" }

func (t *saveTool) Description() string {
	return "保存一条长期记忆，之后的每次对话都会看到它的标题与摘要。" +
		"标题要短且唯一，摘要一句话说清这条记忆讲什么，正文写完整内容。" +
		"默认不覆盖同名记忆；确实要更新已有记忆时把 mode 设为 replace。"
}

func (t *saveTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "标题，简短且唯一"},
			"description": {"type": "string", "description": "一句话摘要，会出现在记忆索引里，过长会被截断"},
			"type": {"type": "string", "description": "分类", "enum": ["偏好", "约定", "事实", "踩坑"]},
			"content": {"type": "string", "description": "完整内容"},
			"mode": {"type": "string", "description": "同名记忆已存在时的处理方式，默认拒绝", "enum": ["create", "replace"]}
		},
		"required": ["name", "description", "type", "content"]
	}`)
}

func (t *saveTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Content     string `json:"content"`
		Mode        string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(a.Content) == "" {
		return "", fmt.Errorf("记忆内容不能为空")
	}
	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}

	e, err := s.store.Save(Entry{
		Name:        a.Name,
		Description: a.Description,
		Type:        a.Type,
		Content:     a.Content,
	}, a.Mode == "replace")
	if err != nil {
		return "", err
	}

	// 本轮的记忆索引在这次对话开始时就已定型，回显条目让新记忆立刻可见，
	// 避免同一轮里反复保存同一条。
	out := fmt.Sprintf("已保存记忆：%s/%s", e.Type, e.Name)
	if e.Description != "" {
		out += " — " + e.Description
	}
	return out, nil
}

// ---------- delete_memory ----------

type deleteTool struct{ p *Plugin }

func (t *deleteTool) Name() string { return "delete_memory" }

func (t *deleteTool) Description() string {
	return "删除一条已保存的记忆，用于内容已经失效或不再需要长期保留时。"
}

func (t *deleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "要删除的记忆标题"}
		},
		"required": ["name"]
	}`)
}

func (t *deleteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}
	e, err := s.store.Delete(a.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除记忆：%s/%s", e.Type, e.Name), nil
}

// ---------- recall_memory ----------

type recallTool struct{ p *Plugin }

func (t *recallTool) Name() string { return "recall_memory" }

func (t *recallTool) Description() string {
	return "按标题读取一条记忆的完整内容。标题取自记忆索引或 list_memories 的结果。"
}

func (t *recallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "记忆的标题"}
		},
		"required": ["name"]
	}`)
}

func (t *recallTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return "", fmt.Errorf("需要提供记忆的标题")
	}
	s := t.p.snapshot()
	if s.store == nil {
		return "", errNotReady
	}
	e, err := s.store.Get(a.Name)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s（创建于 %s，更新于 %s）\n",
		e.Type, e.Name, e.Created.Format("2006-01-02"), e.Updated.Format("2006-01-02"))
	if e.Description != "" {
		b.WriteString(e.Description)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(e.Content)

	out, truncated := plugin.TruncateUTF8([]byte(b.String()), s.maxEntryBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[内容超过 %d 字节，已截断]", s.maxEntryBytes)
	}
	return res, nil
}
