package main

import (
	"errors"

	"github.com/charmbracelet/huh"
)

// accessSection 是访问控制：设置、修改或清除远程访问口令。
//
// 这一屏是新装的远程机器上补口令的主路径。判定策略（长度下限、对外监听时不许清除、
// 环境变量托管时不许改）全在 server.AuthStore.Change 里，这里只负责问和报。
func accessSection(b backend) error {
	for {
		st, err := b.loadAuth()
		if err != nil {
			return err
		}

		opts := []huh.Option[string]{}
		if st.EnvManaged {
			opts = append(opts, huh.NewOption("（口令由环境变量提供，此处不可修改）", back))
		} else if st.HasPassword {
			opts = append(opts,
				huh.NewOption("修改口令", "set"),
				huh.NewOption("清除口令", "clear"),
			)
		} else {
			opts = append(opts, huh.NewOption("设置口令", "set"))
		}
		opts = append(opts, huh.NewOption("← 返回", back))

		choice := opts[0].Value // 光标停在第一项，见 topMenu 的说明
		if err := run(huh.NewSelect[string]().
			Title("访问控制").
			Description(describeAuth(st) + "\n口令存于 <配置目录>/auth.json，不写入 config.yaml。").
			Options(opts...).
			Value(&choice)); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return nil
			}
			return err
		}

		switch choice {
		case back:
			return nil
		case "set":
			if err := setPasswordForm(b, st.HasPassword); err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					continue
				}
				note("× %v", err)
				continue
			}
			note("✓ 口令已保存")
			if !st.Exposed {
				note("  当前只监听本机；要对外提供服务，把 config.yaml 的 server.host 改成 0.0.0.0 后重启。")
			}
		case "clear":
			if err := b.changePassword(askCurrent(), ""); err != nil {
				note("× %v", err)
				continue
			}
			note("✓ 口令已清除，此后只能本机访问")
		}
	}
}

func setPasswordForm(b backend, needCurrent bool) error {
	var current, next, confirm string

	fields := []huh.Field{}
	if needCurrent {
		fields = append(fields, huh.NewInput().Title("当前口令").
			EchoMode(huh.EchoModePassword).Value(&current))
	}
	fields = append(fields,
		huh.NewInput().Title("新口令").Description("至少 8 位").
			EchoMode(huh.EchoModePassword).Value(&next),
		huh.NewInput().Title("确认新口令").
			EchoMode(huh.EchoModePassword).Value(&confirm).
			Validate(func(s string) error {
				if s != next {
					return errors.New("两次输入不一致")
				}
				return nil
			}),
	)
	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return err
	}
	return b.changePassword(current, next)
}

// askCurrent 单独问一次当前口令（清除口令时用）。
// 取不到就交空串，让 Change 去拒绝——校验只应有一处。
func askCurrent() string {
	var s string
	if err := run(huh.NewInput().Title("当前口令").
		EchoMode(huh.EchoModePassword).Value(&s)); err != nil {
		return ""
	}
	return s
}
