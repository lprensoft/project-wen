package belongings

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
var errNotReady = fmt.Errorf("持有物清单尚未就绪")

// maxListBytes 限制 list_items 的返回长度。
const maxListBytes = 16 * 1024

// ---------- update_items ----------

type updateTool struct{ p *Plugin }

func (t *updateTool) Name() string { return "update_items" }

func (t *updateTool) Description() string {
	return "更新一个容器（冰箱、衣柜等）里的物品：add 放入、remove 取出或消耗。" +
		"物品发生增减时（买了、用掉、吃完、扔了）随手更新，同一容器的变化合并成一次调用。" +
		"容器不存在时自动新建。"
}

func (t *updateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"container": {"type": "string", "description": "容器名，如「冰箱」「衣柜」（20 字内）"},
			"add": {
				"type": "array",
				"description": "放入的物品；同名已存在时叠加数量、更新备注",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string", "description": "物品名，容器内唯一（30 字内）"},
						"qty": {"type": "integer", "description": "数量；可数的物品填（六个鸡蛋填 6），不计数的（一件外套）不填"},
						"note": {"type": "string", "description": "可选备注，如「快过期」「妈妈织的」（80 字内）；不写「刚买的」这类时间描述，入库时间会自动记录"}
					},
					"required": ["name"]
				}
			},
			"remove": {
				"type": "array",
				"description": "取出、消耗或丢弃的物品；计数物品减到 0 自动移除",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string", "description": "物品名"},
						"qty": {"type": "integer", "description": "取出的数量；不填表示整个移除"}
					},
					"required": ["name"]
				}
			},
			"reason": {"type": "string", "description": "这次变化的缘由，一句话，如「做了番茄炒蛋」"}
		},
		"required": ["container", "reason"]
	}`)
}

func (t *updateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Container string `json:"container"`
		Add       []struct {
			Name string `json:"name"`
			Qty  int    `json:"qty"`
			Note string `json:"note"`
		} `json:"add"`
		Remove []struct {
			Name string `json:"name"`
			Qty  int    `json:"qty"`
		} `json:"remove"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if len(a.Add) == 0 && len(a.Remove) == 0 {
		return "", fmt.Errorf("add 与 remove 至少要有一项")
	}
	if strings.TrimSpace(a.Reason) == "" {
		return "", fmt.Errorf("reason 不能为空：一句话说明这次增减的缘由")
	}
	s := t.p.snapshot()
	if s.base == "" {
		return "", errNotReady
	}

	// 容器已存在时写回它所在的库（同一个冰箱不因人格切换分叉成两份）；
	// 不存在时在本轮写入域新建。定位按可读域做，报错也只报可读的容器名。
	cs, err := t.p.visibleContainers(ctx)
	if err != nil {
		return "", err
	}
	store := t.p.writeStore(ctx)
	if i := containerIndex(cs, a.Container); i >= 0 {
		store = t.p.storeFor(cs[i].Domain)
	} else if len(a.Add) == 0 {
		return "", fmt.Errorf("没有名为 %q 的容器，现有：%s",
			strings.TrimSpace(a.Container), nameList(containerNames(cs)))
	}
	if store == nil {
		return "", errNotReady
	}

	add := make([]Change, 0, len(a.Add))
	for _, it := range a.Add {
		add = append(add, Change{Name: it.Name, Qty: it.Qty, Note: it.Note})
	}
	remove := make([]Change, 0, len(a.Remove))
	for _, it := range a.Remove {
		remove = append(remove, Change{Name: it.Name, Qty: it.Qty})
	}

	res, err := store.Apply(a.Container, add, remove, time.Now(),
		Limits{MaxContainers: s.maxContainers, MaxItemsPerContainer: s.maxItems})
	if err != nil {
		return "", err
	}
	log.Printf("belongings: 更新%s（%s）", res.Container, strings.TrimSpace(a.Reason))

	// 回显本次变化：本轮的清单注入在对话开始时就已定型，回执让变化立刻可见，
	// 避免同一轮里重复更新。
	var parts []string
	if len(res.Added) > 0 {
		parts = append(parts, "入 "+strings.Join(res.Added, "、"))
	}
	if len(res.Removed) > 0 {
		parts = append(parts, "出 "+strings.Join(res.Removed, "、"))
	}
	return res.Container + "：" + strings.Join(parts, "；") + "。", nil
}

// ---------- list_items ----------

type listTool struct{ p *Plugin }

func (t *listTool) Name() string { return "list_items" }

func (t *listTool) Description() string {
	return "列出持有物清单的完整内容。可按容器或关键词过滤，用于清单未完整注入、" +
		"或需要确认某件物品在不在时。"
}

func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"container": {"type": "string", "description": "只看这个容器；留空表示全部"},
			"keyword": {"type": "string", "description": "过滤关键词，对物品名与备注做包含匹配；留空表示不过滤"}
		}
	}`)
}

func (t *listTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Container string `json:"container"`
		Keyword   string `json:"keyword"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	if t.p.snapshot().base == "" {
		return "", errNotReady
	}
	// 只数、只列本轮可读的容器：条数本身也会泄漏「存在什么」
	cs, err := t.p.visibleContainers(ctx)
	if err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "还没有记录任何持有物。", nil
	}

	cname := strings.TrimSpace(a.Container)
	if cname != "" {
		i := containerIndex(cs, cname)
		if i < 0 {
			return "", fmt.Errorf("没有名为 %q 的容器，现有：%s", cname, nameList(containerNames(cs)))
		}
		cs = cs[i : i+1]
	}

	var b strings.Builder
	kw := strings.ToLower(strings.TrimSpace(a.Keyword))
	total, matched := 0, 0
	for _, c := range cs {
		var lines []string
		for _, it := range c.Items {
			total++
			if kw != "" && !strings.Contains(strings.ToLower(it.Name+"\n"+it.Note), kw) {
				continue
			}
			matched++
			line := "- " + it.Name
			if it.Qty > 0 {
				line += fmt.Sprintf(" ×%d", it.Qty)
			}
			if it.Note != "" {
				line += "（" + it.Note + "）"
			}
			if !it.Added.IsZero() {
				line += "，" + it.Added.Format("2006-01-02") + " 放入"
			}
			lines = append(lines, line)
		}
		if kw != "" && len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s（%d 项）：\n", c.Name, len(c.Items))
		if len(lines) == 0 {
			b.WriteString("（空）\n")
		} else {
			b.WriteString(strings.Join(lines, "\n"))
			b.WriteString("\n")
		}
	}
	if kw != "" && matched == 0 {
		return fmt.Sprintf("共 %d 项物品，没有符合条件的。", total), nil
	}

	out, truncated := plugin.TruncateUTF8([]byte(strings.TrimRight(b.String(), "\n")), maxListBytes)
	res := string(out)
	if truncated {
		res += fmt.Sprintf("\n...[超过 %d 字节，已截断，请用更具体的条件过滤]", maxListBytes)
	}
	return res, nil
}
