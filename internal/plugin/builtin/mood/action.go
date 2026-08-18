package mood

import (
	"context"
	"fmt"
	"strings"

	"wen/internal/plugin"
)

// actionReset 是设置页上的「重置为平静」按钮。
//
// 心情本来就能被模型两个方向地调、也会自己回落，所以这个按钮不是必需的——
// 它管的是另一种情况：换了角色设定，旧角色的心情还挂在那里；或者把回落速率设成
// 了 0，心情从此停在某个值上下不来。这两种都该由人来判断，不给模型。
const actionReset = "reset"

func (p *Plugin) Actions() []plugin.ActionDef {
	return []plugin.ActionDef{{
		Key:         actionReset,
		Label:       "重置为平静",
		Description: "把心情清回平静，包括各人格分开保存的那几份。不可撤销。",
	}}
}

// StartAction 重置全部可见域的心情。用户拥有全部域，这里不做可见域过滤——
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

	// Scope 取零值：Write 为空、Read 为 nil，ReadDomains 因此枚举磁盘上已存在的
	// 全部域，正好是「全部重置」需要的范围。
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
		return fmt.Errorf("重置心情失败: %s", strings.Join(failed, "; "))
	}
	p.actState = plugin.ActionState{
		Status:  plugin.ActionDone,
		Message: fmt.Sprintf("已把 %d 份心情重置为平静。", reset),
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
