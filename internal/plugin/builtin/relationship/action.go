package relationship

import (
	"context"
	"fmt"
	"strings"

	"wen/internal/plugin"
)

// actionReset 是设置页上的「重置关系状态」按钮。
//
// 模型能逐项清除，但「这段关系整份不作数了」是换角色设定、换故事线时人的判断——
// 这件事不给模型做，工具层不该有入口。
const actionReset = "reset"

func (p *Plugin) Actions() []plugin.ActionDef {
	return []plugin.ActionDef{{
		Key:         actionReset,
		Label:       "重置关系状态",
		Description: "抹掉全部关系快照，包括各人格分开保存的那几份。不可撤销。",
	}}
}

// StartAction 清空全部可见域的快照。用户拥有全部域，这里不做可见域过滤——
// 界面上的操作不是模型的读取路径，不存在泄漏问题。
func (p *Plugin) StartAction(_ context.Context, key string) error {
	if key != actionReset {
		return fmt.Errorf("未知的操作 %q", key)
	}
	p.mu.RLock()
	base := p.base
	p.mu.RUnlock()
	if base == "" {
		return errNotReady
	}

	// Scope 取零值：ReadDomains 因此枚举磁盘上已存在的全部域，正好是「全部重置」的范围。
	var failed []string
	reset := 0
	for _, tag := range plugin.ReadDomains(base, plugin.Scope{}) {
		store := p.storeFor(tag)
		if store == nil {
			continue
		}
		if err := store.Clear(); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		reset++
	}

	p.actMu.Lock()
	defer p.actMu.Unlock()
	if len(failed) > 0 {
		p.actState = plugin.ActionState{
			Status:  plugin.ActionError,
			Message: "重置失败：" + strings.Join(failed, "; "),
		}
		return fmt.Errorf("重置关系状态失败: %s", strings.Join(failed, "; "))
	}
	p.actState = plugin.ActionState{
		Status:  plugin.ActionDone,
		Message: fmt.Sprintf("已重置 %d 份关系状态。", reset),
	}
	return nil
}

func (p *Plugin) ActionState(key string) (plugin.ActionState, error) {
	if key != actionReset {
		return plugin.ActionState{}, fmt.Errorf("未知的操作 %q", key)
	}
	p.actMu.Lock()
	defer p.actMu.Unlock()
	if p.actState.Status == "" {
		return plugin.ActionState{Status: plugin.ActionIdle}, nil
	}
	return p.actState, nil
}
