package bodysense

import (
	"context"
	"fmt"
	"strings"

	"wen/internal/plugin"
)

// actionClear 是设置页上的「清空接触记录」按钮。
//
// 这件事不给模型做：角色设定一改，计数还挂在旧角色身上，而系统里没有「角色身份」
// 这个概念，只能由人来判断该不该清。这是不可撤销的破坏性操作，工具层不该有入口。
const actionClear = "clear"

func (p *Plugin) Actions() []plugin.ActionDef {
	return []plugin.ActionDef{{
		Key:         actionClear,
		Label:       "清空接触记录",
		Description: "把全部部位的累计次数清零，包括各人格分开保存的那几份。不可撤销。",
	}}
}

// StartAction 清空全部可见域的记录。用户拥有全部域，这里不做可见域过滤——
// 界面上的操作不是模型的读取路径，不存在泄漏问题。
func (p *Plugin) StartAction(_ context.Context, key string) error {
	if key != actionClear {
		return fmt.Errorf("未知的操作 %q", key)
	}
	p.mu.RLock()
	base := p.base
	p.mu.RUnlock()
	if base == "" {
		return errNotReady
	}

	// Scope 取零值：Write 为空、Read 为 nil，ReadDomains 因此枚举磁盘上已存在的
	// 全部域，正好是「清空所有」需要的范围。
	var failed []string
	cleared := 0
	for _, tag := range plugin.ReadDomains(base, plugin.Scope{}) {
		store := p.storeFor(tag)
		if store == nil {
			continue
		}
		if err := store.Clear(); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		cleared++
	}

	p.actMu.Lock()
	defer p.actMu.Unlock()
	if len(failed) > 0 {
		p.actState = plugin.ActionState{
			Status:  plugin.ActionError,
			Message: "清空失败：" + strings.Join(failed, "; "),
		}
		return fmt.Errorf("清空接触记录失败: %s", strings.Join(failed, "; "))
	}
	p.actState = plugin.ActionState{
		Status:  plugin.ActionDone,
		Message: fmt.Sprintf("已清空 %d 份接触记录。", cleared),
	}
	return nil
}

func (p *Plugin) ActionState(key string) (plugin.ActionState, error) {
	if key != actionClear {
		return plugin.ActionState{}, fmt.Errorf("未知的操作 %q", key)
	}
	p.actMu.Lock()
	defer p.actMu.Unlock()
	if p.actState.Status == "" {
		return plugin.ActionState{Status: plugin.ActionIdle}, nil
	}
	return p.actState, nil
}
