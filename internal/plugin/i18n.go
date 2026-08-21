package plugin

import (
	"fmt"

	"wen/internal/i18n"
)

// 插件元数据的翻译。
//
// 键从插件已经有的东西推出来（插件名 + 配置项的 Key + 候选值），**不在
// ConfigField 上加字段**。加一个 LabelEN 的话，下一门语言就得再加一个
// LabelJA——那是「只有两种语言」的思路穿了件国际化的外衣。键靠推导的代价是
// 零：二十八个内置插件一行代码都不用改，加一门语言只是多一份语言包。
//
// 翻译只发生在**展示的那一刻**：Manager.List 仍返回代码里的中文，由
// Localize 按调用方（某个 HTTP 请求、某次 wen config 会话）的语言复制出一份。
// 服务端因此不存任何人的语言——同一个服务可能一台电脑一台手机各用一种语言。

// 元数据键的推导。三处调用方共用（服务端、终端配置、检查工具），
// 分头拼字符串迟早会拼出两套。
func DescKey(plugin string) string { return "plugin." + plugin + ".desc" }

func FieldLabelKey(plugin, field string) string {
	return "plugin." + plugin + ".field." + field + ".label"
}

func FieldDescKey(plugin, field string) string {
	return "plugin." + plugin + ".field." + field + ".desc"
}

func OptionKey(plugin, field, value string) string {
	return "plugin." + plugin + ".field." + field + ".option." + value
}

func ActionLabelKey(plugin, action string) string {
	return "plugin." + plugin + ".action." + action + ".label"
}

func ActionDescKey(plugin, action string) string {
	return "plugin." + plugin + ".action." + action + ".desc"
}

func CategoryTextKey(catKey string) string { return "plugin.category." + catKey }

// Localize 按 lang 复制出一份译好的插件状态。
//
// 复制而不是就地改：Status 里的 ConfigFields 与 Actions 是插件自己那份切片，
// 就地改会把翻译写回插件，下一次别的语言来问就拿到上一个人的语言了。
// UnavailableKey 是「这个候选当前用不了」那句提示的键。整句进字典而不是在外面
// 拼一个后缀：中文缀在名字后面不留空格、英文要留一个，这种差异逐处硬写必漏。
const UnavailableKey = "plugin.option.unavailable"
const unavailableZH = "%s（未启用）"

// Localize 按 lang 复制出一份译好的插件状态。
//
// 复制而不是就地改：Status 里的 ConfigFields 与 Actions 是插件自己那份切片，
// 就地改会把翻译写回插件，下一次别的语言来问就拿到上一个人的语言了。
//
// 中文也要走这一趟（不能提前返回）：候选项的「未启用」是在这里缀上去的，
// 中文只是恰好查不到译文、用回原文而已。
func Localize(lang string, sts []Status) []Status {
	out := make([]Status, len(sts))
	for i, st := range sts {
		st.Description = i18n.T(lang, DescKey(st.Name), st.Description)
		st.Category = i18n.T(lang, CategoryTextKey(st.CategoryKey), st.Category)

		if len(st.ConfigFields) > 0 {
			fields := make([]ConfigField, len(st.ConfigFields))
			for j, f := range st.ConfigFields {
				f.Label = i18n.T(lang, FieldLabelKey(st.Name, f.Key), f.Label)
				f.Description = i18n.T(lang, FieldDescKey(st.Name, f.Key), f.Description)
				if len(f.Options) > 0 {
					opts := make([]ConfigOption, len(f.Options))
					for k, o := range f.Options {
						o.Label = i18n.T(lang, OptionKey(st.Name, f.Key, o.Value), o.Label)
						if o.Unavailable {
							o.Label = fmt.Sprintf(i18n.T(lang, UnavailableKey, unavailableZH), o.Label)
						}
						opts[k] = o
					}
					f.Options = opts
				}
				fields[j] = f
			}
			st.ConfigFields = fields
		}

		if len(st.Actions) > 0 {
			acts := make([]ActionDef, len(st.Actions))
			for j, a := range st.Actions {
				a.Label = i18n.T(lang, ActionLabelKey(st.Name, a.Key), a.Label)
				a.Description = i18n.T(lang, ActionDescKey(st.Name, a.Key), a.Description)
				acts[j] = a
			}
			st.Actions = acts
		}
		out[i] = st
	}
	return out
}
