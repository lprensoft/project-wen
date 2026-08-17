package scene

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("场景记忆尚未就绪")

// maxListBytes 限制 list_scenes 的返回长度。
const maxListBytes = 16 * 1024

// ---------- save_scene ----------

type saveTool struct{ p *Plugin }

func (t *saveTool) Name() string { return "save_scene" }

func (t *saveTool) Description() string {
	return "记录一处场景或地点，之后的每次对话都会看到它。" +
		"名称要短且唯一，描述写场景本身：位置、布局、陈设、氛围与显著细节。" +
		"默认不覆盖同名场景；确实要更新已有场景时把 mode 设为 replace。"
}

func (t *saveTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "场景或地点的名称，简短且唯一"},
			"detail": {"type": "string", "description": "场景描述：位置、与其它地点的关系、布局、陈设、氛围与显著细节"},
			"mode": {"type": "string", "description": "同名场景已存在时的处理方式，默认拒绝", "enum": ["create", "replace"]}
		},
		"required": ["name", "detail"]
	}`)
}

func (t *saveTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name   string `json:"name"`
		Detail string `json:"detail"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	// 写入本轮的可见域。同名检查也只在这个库内进行——若跨域检查，
	// 「已记录过场景」这条报错就会把不可读域的场景名称吐出来。
	store := t.p.writeStore(ctx)
	if store == nil {
		return "", errNotReady
	}
	sc, err := store.Save(Scene{Name: a.Name, Detail: a.Detail}, a.Mode == "replace")
	if err != nil {
		return "", err
	}
	// 本轮的场景记忆在这次对话开始时就已定型，回显条目让新场景立刻可见，
	// 避免同一轮里反复记录同一处。
	verb := "已记录场景"
	if a.Mode == "replace" {
		verb = "已更新场景"
	}
	return fmt.Sprintf("%s：%s — %s", verb, sc.Name, sc.Detail), nil
}

// ---------- list_scenes ----------

type listTool struct{ p *Plugin }

func (t *listTool) Name() string { return "list_scenes" }

func (t *listTool) Description() string {
	return "列出已记录的全部场景与地点。可按关键词过滤，用于场景记忆未完整注入、" +
		"或需要确认某处场景是否已记录时。"
}

func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"keyword": {"type": "string", "description": "过滤关键词，对名称与描述做包含匹配；留空表示不过滤"}
		}
	}`)
}

func (t *listTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Keyword string `json:"keyword"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	if t.p.snapshot().base == "" {
		return "", errNotReady
	}
	// 只数、只列本轮可读的场景：条数本身也会泄漏「存在什么」
	scenes, err := t.p.visibleScenes(ctx)
	if err != nil {
		return "", err
	}

	matched := filterScenes(scenes, a.Keyword)
	if len(matched) == 0 {
		if len(scenes) == 0 {
			return "还没有记录任何场景。", nil
		}
		return fmt.Sprintf("共 %d 处场景，没有符合条件的。", len(scenes)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 处场景", len(scenes))
	if len(matched) != len(scenes) {
		fmt.Fprintf(&b, "，其中 %d 处符合条件", len(matched))
	}
	b.WriteString("：\n")
	for _, sc := range matched {
		fmt.Fprintf(&b, "- %s（记录于 %s）：%s\n", sc.Name, sc.Created.Format("2006-01-02"), sc.Detail)
	}
	out, truncated := plugin.TruncateUTF8([]byte(strings.TrimRight(b.String(), "\n")), maxListBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[超过 %d 字节，已截断，请用更具体的关键词过滤]", maxListBytes)
	}
	return res, nil
}

// filterScenes 做大小写不敏感的包含匹配。
func filterScenes(scenes []Scene, keyword string) []Scene {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	if kw == "" {
		return scenes
	}
	var out []Scene
	for _, sc := range scenes {
		hay := strings.ToLower(sc.Name + "\n" + sc.Detail)
		if strings.Contains(hay, kw) {
			out = append(out, sc)
		}
	}
	return out
}

// ---------- delete_scene ----------

type deleteTool struct{ p *Plugin }

func (t *deleteTool) Name() string { return "delete_scene" }

func (t *deleteTool) Description() string {
	return "删除一处已记录的场景，用于该场景已不复存在或不再需要延续时。"
}

func (t *deleteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "要删除的场景名称"}
		},
		"required": ["name"]
	}`)
}

func (t *deleteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if t.p.snapshot().base == "" {
		return "", errNotReady
	}
	// 先在可读范围内定位，再删回它所在的库：不可读域的场景连删都不该删得动
	found, store, err := t.p.findVisible(ctx, a.Name)
	if err != nil {
		return "", err
	}
	sc, err := store.Delete(found.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除场景：%s", sc.Name), nil
}
