package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-runewidth"

	"wen/internal/plugin"
)

// pluginsSection 是插件列表：选一个进去改，返回后回到列表。
func pluginsSection(b backend) error {
	for {
		list, err := b.listPlugins()
		if err != nil {
			return err
		}
		// 光标停在第一个插件而不是「返回」上，见 topMenu 的说明
		choice := back
		if len(list) > 0 {
			choice = list[0].Name
		}
		opts := pluginOptions(list)
		if err := run(huh.NewSelect[string]().
			Title(tr("cli.plugins.title", "插件")).
			Description(fit(b.mode())).
			Height(listHeight(len(opts), 2)). // chrome：标题 1 行 + 说明 1 行
			Options(opts...).
			Value(&choice)); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return nil
			}
			return err
		}
		if choice == back {
			return nil
		}
		for _, st := range list {
			if st.Name == choice {
				if err := editPlugin(b, st); err != nil && !errors.Is(err, huh.ErrUserAborted) {
					note("× %v", err)
				}
				break
			}
		}
	}
}

// pluginOptions 按功能分组展示，组内保持注册顺序——注册顺序承载提示词注入顺序
// 与依赖关系，不为展示而改。分组名由后端随每个插件给出。
func pluginOptions(list []plugin.Status) []huh.Option[string] {
	var order []string
	byCat := map[string][]plugin.Status{}
	for _, p := range list {
		cat := p.Category
		if cat == "" {
			cat = tr(plugin.CategoryTextKey(plugin.CategoryKeyOther), plugin.CategoryOther)
		}
		if _, seen := byCat[cat]; !seen {
			order = append(order, cat)
		}
		byCat[cat] = append(byCat[cat], p)
	}

	// 名字与分组各自对齐成一列，描述占剩下的宽度并按需截断——
	// 完整描述在插件详情那一屏里给（见 pluginHint），列表这里只求一眼扫过去
	nameW, catW := 0, 0
	for _, p := range list {
		nameW = max(nameW, runewidth.StringWidth(p.Name))
	}
	for _, cat := range order {
		catW = max(catW, runewidth.StringWidth(groupLabel(cat)))
	}

	opts := make([]huh.Option[string], 0, len(list)+1)
	for _, cat := range order {
		for i, p := range byCat[cat] {
			mark := "○"
			if p.Enabled {
				mark = "●"
			}
			group := ""
			if i == 0 {
				group = groupLabel(cat)
			}
			desc := p.Description
			if len(p.Unmet) > 0 {
				desc = trf("cli.plugins.unmet", "（需先启用 %s）", joinList(p.Unmet)) + desc
			}
			label := fmt.Sprintf("%s %s %s  %s", pad(group, catW), mark, pad(p.Name, nameW), desc)
			opts = append(opts, huh.NewOption(fit(label), p.Name))
		}
	}
	return append(opts, huh.NewOption(tr("cli.back", "← 返回"), back))
}

// editPlugin 展示单个插件的开关、参数表单与操作入口。
//
// 表单直接由插件声明的 ConfigFields 生成，与 Web UI 同源——新增插件时两边同时
// 就有了配置界面，一处都不用改。声明了操作的插件（如微信的扫码绑定）先出一层
// 菜单：操作与配置是两条路，混进一张表单里没法表达「点一下就开始跑」。
func editPlugin(b backend, st plugin.Status) error {
	if len(st.Actions) > 0 {
		// 用不会与操作 key 撞车的取值（key 按约定是小写字母开头）
		const cfgChoice = "*config"
		choice := cfgChoice
		opts := []huh.Option[string]{huh.NewOption(tr("cli.plugins.edit", "修改开关与配置"), cfgChoice)}
		for _, a := range st.Actions {
			label := "▶ " + a.Label
			if a.Description != "" {
				label += "  " + a.Description
			}
			opts = append(opts, huh.NewOption(fit(label), a.Key))
		}
		opts = append(opts, huh.NewOption(tr("cli.back", "← 返回"), back))
		if err := run(huh.NewSelect[string]().
			Title(st.Name).
			Description(fit(st.Description)).
			Options(opts...).
			Value(&choice)); err != nil {
			return err
		}
		switch choice {
		case back:
			return nil
		case cfgChoice:
			// 落到下面的表单
		default:
			for _, a := range st.Actions {
				if a.Key == choice {
					return runAction(b, st.Name, a)
				}
			}
			return nil
		}
	}

	enabled := st.Enabled
	fields := []huh.Field{
		huh.NewConfirm().
			Title(trf("cli.plugins.enableTitle", "启用 %s", st.Name)).
			Description(pluginHint(st)).
			Affirmative(tr("cli.plugins.enable", "启用")).Negative(tr("cli.plugins.disable", "停用")).
			Value(&enabled),
	}

	binds := make([]*binding, 0, len(st.ConfigFields))
	for _, f := range st.ConfigFields {
		bd := newBinding(f, st.Config[f.Key])
		binds = append(binds, bd)
		fields = append(fields, bd.field())
	}

	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return err
	}

	// 配置先于开关：反过来的话，启用会先按旧配置初始化一次，紧接着又要重来一遍
	if len(binds) > 0 {
		cfg := make(map[string]any, len(binds))
		for _, bd := range binds {
			cfg[bd.f.Key] = bd.value()
		}
		if err := b.setPluginConfig(st.Name, cfg); err != nil {
			return fmt.Errorf(trf("cli.plugins.saveFailed", "保存 %s 的配置失败", st.Name)+": %w", err)
		}
		note(trf("cli.plugins.saved", "✓ %s 的配置已保存", st.Name))
	}
	if enabled != st.Enabled {
		if err := b.setPluginEnabled(st.Name, enabled); err != nil {
			return err
		}
		if enabled {
			note(trf("cli.plugins.enabled", "✓ %s 已启用", st.Name))
		} else {
			note(trf("cli.plugins.disabled", "✓ %s 已停用", st.Name))
		}
	}
	return nil
}

// pluginHint 把依赖与冲突讲清楚——它们是开关会失败的唯一原因。
func pluginHint(st plugin.Status) string {
	var parts []string
	parts = append(parts, st.Description)
	if len(st.Requires) > 0 {
		parts = append(parts, trf("cli.plugins.requires", "依赖：%s", joinList(st.Requires)))
	}
	if len(st.Conflicting) > 0 {
		parts = append(parts, trf("cli.plugins.conflicts", "与已启用的 %s 冲突（仅提示，不阻止）", joinList(st.Conflicting)))
	}
	return strings.Join(parts, "\n")
}

// binding 把一个配置项绑到 huh 的输入上。
//
// 值一律以字符串携带（布尔除外），提交时原样交给服务端的 NormalizeConfig 校验，
// 不在这里自己判定取值范围——两处各写一套迟早会分叉。
type binding struct {
	f plugin.ConfigField
	s string
	b bool
}

func newBinding(f plugin.ConfigField, cur any) *binding {
	bd := &binding{f: f}
	switch f.Type {
	case plugin.FieldBool:
		bd.b, _ = cur.(bool)
	default:
		bd.s = scalarString(cur)
	}
	return bd
}

// scalarString 把当前生效值转成可编辑的文本。JSON 往返会把整数变成 float64，
// 在线模式下拿到的就是它，直接 %v 会显示成 3600 变 3600（尚可）或 1e+06（不可读）。
func scalarString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (bd *binding) field() huh.Field {
	f := bd.f
	title := f.Label
	if title == "" {
		title = f.Key
	}
	desc := f.Description
	if f.Min != nil || f.Max != nil {
		desc = strings.TrimSpace(desc + "\n" + trf("cli.field.range", "范围 %s ~ %s", limit(f.Min), limit(f.Max)))
	}

	switch f.Type {
	case plugin.FieldBool:
		return huh.NewConfirm().Title(title).Description(desc).
			Affirmative(tr("cli.on", "开")).Negative(tr("cli.off", "关")).Value(&bd.b)
	case plugin.FieldSelect:
		opts := make([]huh.Option[string], 0, len(f.Options))
		for _, o := range f.Options {
			opts = append(opts, huh.NewOption(o.Label, o.Value))
		}
		return huh.NewSelect[string]().Title(title).Description(desc).
			Options(opts...).Value(&bd.s)
	case plugin.FieldText:
		return huh.NewText().Title(title).Description(desc).Lines(6).Value(&bd.s)
	case plugin.FieldInt:
		return huh.NewInput().Title(title).Description(desc).Value(&bd.s).
			Validate(func(s string) error {
				// 空串按「用默认值」处理，与 Web UI 清空数字框的语义一致
				if strings.TrimSpace(s) == "" {
					return nil
				}
				if _, err := strconv.Atoi(strings.TrimSpace(s)); err != nil {
					return errors.New(tr("cli.field.needInt", "请输入整数"))
				}
				return nil
			})
	default:
		return huh.NewInput().Title(title).Description(desc).Value(&bd.s)
	}
}

func (bd *binding) value() any {
	if bd.f.Type == plugin.FieldBool {
		return bd.b
	}
	if bd.f.Type == plugin.FieldInt || bd.f.Type == plugin.FieldSelect {
		return strings.TrimSpace(bd.s)
	}
	return bd.s
}

func limit(p *int) string {
	if p == nil {
		return tr("cli.field.unlimited", "不限")
	}
	return strconv.Itoa(*p)
}
