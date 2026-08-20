package health

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"wen/internal/plugin"
)

// errNotReady 在插件未取得持久化目录时返回，正常流程下不会出现。
var errNotReady = fmt.Errorf("身体状况尚未就绪")

// ---------- set_condition ----------

type setTool struct{ p *Plugin }

func (t *setTool) Name() string { return "set_condition" }

func (t *setTool) Description() string {
	return "记下角色新添的一条身体小恙：叫什么、最重时多重、几小时后发作、大概几天好、怎么处理。" +
		"记下之后病程由时间自动推进，不需要再手动加重或减轻；要改处理方式、严重度或标记痊愈用 update_condition。" +
		"痊愈后一段时间内、或同时记着的状况已到上限时会被拒绝并说明规则。"
}

func (t *setTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "状况名，不超过 12 字，如「着凉」「肠胃不舒服」「没睡好的倦怠」"},
			"severity": {"type": "string", "enum": ["有点不舒服", "难受", "病倒了"], "description": "最重的时候有多重"},
			"onset_hours": {"type": "number", "description": "几小时后开始发作；0 或不填表示此刻就有感觉。下午淋的雨晚上才头疼，就填几个小时。最多 48"},
			"days": {"type": "integer", "description": "硬扛着大概几天好，1 到 14。着凉三到七天，发烧一到三天，头疼、肠胃不适一两天"},
			"care": {"type": "string", "enum": ["硬扛", "吃药", "就医"], "description": "眼下怎么处理，不填为硬扛。吃药好得快些，就医最快"}
		},
		"required": ["name", "severity", "days"]
	}`)
}

func (t *setTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name       string  `json:"name"`
		Severity   string  `json:"severity"`
		OnsetHours float64 `json:"onset_hours"`
		Days       int     `json:"days"`
		Care       string  `json:"care"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if a.Care == "" {
		a.Care = careTough
	}
	if a.OnsetHours < 0 || math.IsNaN(a.OnsetHours) {
		a.OnsetHours = 0
	}
	clippedOnset := a.OnsetHours > maxOnsetHours
	if clippedOnset {
		a.OnsetHours = maxOnsetHours
	}

	s := t.p.snapshot()
	tag := plugin.ScopeFrom(ctx).Write
	store := t.p.storeFor(tag)
	if store == nil {
		return "", errNotReady
	}
	now := time.Now()
	onset := now.Add(time.Duration(a.OnsetHours * float64(time.Hour)))
	c, err := store.Add(a.Name, a.Severity, onset, a.Days, a.Care, now, s.limits())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "已记下：%s，", c.Name)
	if c.Onset.After(now) {
		fmt.Fprintf(&b, "预计%s发作，到时", untilText(c.Onset.Sub(now)))
	} else {
		b.WriteString("此刻开始有感觉，")
	}
	fmt.Fprintf(&b, "最重时%s，硬扛着约 %d 天好（眼下%s）。", band(c.Peak), c.Days, c.Care)
	// 拦截生效时要把规则告诉模型，否则它只会换个说法再报一次
	if got := band(c.Peak); got != a.Severity {
		fmt.Fprintf(&b, "严重度按上限收成「%s」（原报「%s」），只演绎日常小恙。", got, a.Severity)
	}
	if clippedOnset {
		fmt.Fprintf(&b, "发作时刻最多延后 %d 小时，已按上限收。", maxOnsetHours)
	}
	return b.String(), nil
}

// ---------- update_condition ----------

type updateTool struct{ p *Plugin }

func (t *updateTool) Name() string { return "update_condition" }

func (t *updateTool) Description() string {
	return "修改一条已记下的身体状况：换处理方式（硬扛 / 吃药 / 就医，决定好得快慢）、改严重度（明显加重或好转）、或标记痊愈。" +
		"只传要改的项；只有一条状况时可以不传名字。"
}

func (t *updateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "要改哪条状况，与记下时的名字一致；只有一条时可不填"},
			"care": {"type": "string", "enum": ["硬扛", "吃药", "就医"], "description": "新的处理方式"},
			"severity": {"type": "string", "enum": ["有点不舒服", "难受", "病倒了"], "description": "此刻的严重度，明显加重或好转时才改"},
			"recovered": {"type": "boolean", "description": "true 表示已经好了，状况从身上移除"}
		}
	}`)
}

func (t *updateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name      string  `json:"name"`
		Care      *string `json:"care"`
		Severity  *string `json:"severity"`
		Recovered bool    `json:"recovered"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}

	s := t.p.snapshot()
	tag := plugin.ScopeFrom(ctx).Write
	store := t.p.storeFor(tag)
	if store == nil {
		return "", errNotReady
	}
	now := time.Now()
	c, changes, err := store.Apply(a.Name, Update{Care: a.Care, Severity: a.Severity, Recovered: a.Recovered}, now, s.limits())
	if err != nil {
		return "", err
	}
	if a.Recovered {
		dropCue(tag, c.ID) // 还没说出口的「开始发作了」不该再说
		return fmt.Sprintf("%s：已痊愈。", c.Name), nil
	}

	var b strings.Builder
	b.WriteString(c.Name + "：")
	if len(changes) == 0 {
		b.WriteString("没有变化。")
	} else {
		b.WriteString(strings.Join(changes, "；") + "。")
	}
	v := c.view(now)
	if v.Phase == phasePending {
		fmt.Fprintf(&b, "还没发作，预计%s发作。", untilText(c.Onset.Sub(now)))
	} else {
		fmt.Fprintf(&b, "此刻%s，%s。", band(v.Severity), recoverText(v.Remaining))
	}
	return b.String(), nil
}
