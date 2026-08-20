package people

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("人物库尚未就绪")

// maxListBytes 限制 list_people 的返回长度。
const maxListBytes = 16 * 1024

// ---------- upsert_person ----------

type upsertTool struct{ p *Plugin }

func (t *upsertTool) Name() string { return "upsert_person" }

func (t *upsertTool) Description() string {
	return "登记或更新角色生活里的一个人物（朋友、同事、家人、常去店里的老板）。" +
		"新人物必须先登记，之后才能出现在故事与日程里；更新时只传要改的字段。" +
		"与某人有过一次实际来往后传 met_now 记下这次来往。对方本人不登记在这里。"
}

func (t *upsertTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "名字或称呼，库内唯一（20 字内）"},
			"relation": {"type": "string", "description": "关系，一句话，如「高中同学」「楼下咖啡店老板」（40 字内）；新人物必填"},
			"profile": {"type": "string", "description": "几句设定：性格、近况、与角色相处的方式（200 字内）。有变化时传完整的新版本"},
			"closeness": {"type": "string", "enum": ["点头之交", "认识", "熟", "亲近"], "description": "亲近度；新人物不填默认「认识」"},
			"met_now": {"type": "boolean", "description": "这次调用是否代表刚与此人有过一次来往（见面、通话、一起做了件事）；为 true 时把此刻记为上次来往"},
			"last_note": {"type": "string", "description": "上次来往的一句摘要（60 字内），与 met_now 一起传"}
		},
		"required": ["name"]
	}`)
}

func (t *upsertTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name      string  `json:"name"`
		Relation  *string `json:"relation"`
		Profile   *string `json:"profile"`
		Closeness *string `json:"closeness"`
		MetNow    bool    `json:"met_now"`
		LastNote  *string `json:"last_note"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	s := t.p.snapshot()
	if s.base == "" {
		return "", errNotReady
	}

	// 已存在的人写回他所在的库（同一个人不因人格切换分叉成两份）；
	// 不存在时在本轮写入域新建。定位按可读域做，报错也只报可读的人。
	ps, err := t.p.visiblePeople(ctx)
	if err != nil {
		return "", err
	}
	store := t.p.writeStore(ctx)
	if i := personIndex(ps, a.Name); i >= 0 {
		store = t.p.storeFor(ps[i].Domain)
	}
	if store == nil {
		return "", errNotReady
	}

	res, err := store.Upsert(Update{
		Name: a.Name, Relation: a.Relation, Profile: a.Profile,
		Closeness: a.Closeness, MetNow: a.MetNow, LastNote: a.LastNote,
	}, time.Now(), Limits{MaxPeople: s.maxPeople})
	if err != nil {
		return "", err
	}

	// 回显本次变化：本轮的清单注入在对话开始时就已定型，回执让变化立刻可见。
	if res.Created {
		log.Printf("people: 登记 %s", res.Name)
		line := fmt.Sprintf("已登记 %s（%s，%s）。", res.Name, closenessOf(a.Closeness), trimmed(a.Relation))
		if a.MetNow {
			line += "上次来往记为此刻。"
		}
		return line, nil
	}
	if len(res.Changes) == 0 {
		return fmt.Sprintf("%s 的记录没有变化。", res.Name), nil
	}
	log.Printf("people: 更新 %s（%s）", res.Name, strings.Join(res.Changes, "；"))
	return fmt.Sprintf("已更新 %s：%s。", res.Name, strings.Join(res.Changes, "；")), nil
}

func closenessOf(v *string) string {
	if v == nil || *v == "" {
		return defaultCloseness
	}
	return *v
}

// ---------- list_people ----------

type listTool struct{ p *Plugin }

func (t *listTool) Name() string { return "list_people" }

func (t *listTool) Description() string {
	return "列出人物库的完整内容（含每个人的设定与上次来往）。可按关键词过滤，" +
		"用于清单未完整注入、或要看某人的设定与近况时。"
}

func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"keyword": {"type": "string", "description": "过滤关键词，对名字、关系、设定与上次来往做包含匹配；留空表示不过滤"}
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
	// 只数、只列本轮可读的人：人数本身也会泄漏「存在谁」
	ps, err := t.p.visiblePeople(ctx)
	if err != nil {
		return "", err
	}
	if len(ps) == 0 {
		return "人物库还是空的，还没有登记任何人。", nil
	}

	kw := strings.ToLower(strings.TrimSpace(a.Keyword))
	now := time.Now()
	var b strings.Builder
	matched := 0
	for _, p := range ps {
		if kw != "" && !strings.Contains(strings.ToLower(p.Name+"\n"+p.Relation+"\n"+p.Profile+"\n"+p.LastNote), kw) {
			continue
		}
		matched++
		fmt.Fprintf(&b, "- %s｜%s｜%s", p.Name, p.Closeness, p.Relation)
		if a := agoText(p.LastMet, now); a != "" {
			fmt.Fprintf(&b, "｜上次来往 %s", a)
			if p.LastNote != "" {
				b.WriteString("：" + p.LastNote)
			}
		}
		b.WriteString("\n")
		if p.Profile != "" {
			b.WriteString("  " + p.Profile + "\n")
		}
	}
	if matched == 0 {
		return fmt.Sprintf("共 %d 人，没有符合条件的。", len(ps)), nil
	}
	head := fmt.Sprintf("共 %d 人：\n", len(ps))
	if kw != "" {
		head = fmt.Sprintf("共 %d 人，符合条件的 %d 人：\n", len(ps), matched)
	}
	out, truncated := plugin.TruncateUTF8([]byte(head+strings.TrimRight(b.String(), "\n")), maxListBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[超过 %d 字节，已截断，请用关键词过滤]", maxListBytes)
	}
	return res, nil
}

// ---------- remove_person ----------

type removeTool struct{ p *Plugin }

func (t *removeTool) Name() string { return "remove_person" }

func (t *removeTool) Description() string {
	return "把一个人从人物库移除：搬走了、断了联系、再也不会出现。只是关系变淡不移除，改亲近度即可。"
}

func (t *removeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "要移除的人"},
			"reason": {"type": "string", "description": "移除的缘由，一句话"}
		},
		"required": ["name", "reason"]
	}`)
}

func (t *removeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(a.Reason) == "" {
		return "", fmt.Errorf("reason 不能为空：一句话说明为什么移除")
	}
	if t.p.snapshot().base == "" {
		return "", errNotReady
	}
	ps, err := t.p.visiblePeople(ctx)
	if err != nil {
		return "", err
	}
	i := personIndex(ps, a.Name)
	if i < 0 {
		return "", fmt.Errorf("没有叫 %q 的人物，现有：%s", strings.TrimSpace(a.Name), nameList(names(ps)))
	}
	store := t.p.storeFor(ps[i].Domain)
	if store == nil {
		return "", errNotReady
	}
	removed, err := store.Remove(a.Name)
	if err != nil {
		return "", err
	}
	log.Printf("people: 移除 %s（%s）", removed, strings.TrimSpace(a.Reason))
	return fmt.Sprintf("已移除 %s。", removed), nil
}
