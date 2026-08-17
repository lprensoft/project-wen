package memory

import (
	"context"
	"fmt"
	"log"
	"strings"

	"wen/internal/plugin"
)

// OnCompact 在会话历史被摘要替换之前，用一次独立的模型调用把其中值得长期保留的
// 结论提炼成记忆条目。
//
// 这里必须是真正的提取而不是提示模型稍后自己去做：自动压缩是无人值守触发的，
// 历史随即被物理删除，而"稍后"这个时机可能永远不会到来——会话可能就此结束。
// 代价是每次压缩多一次模型调用，因此只在压缩这个低频且信息即将丢失的时刻做。
//
// 原始历史的保全由 session_search 插件的归档负责，两个插件各订阅同一个钩子。
func (p *Plugin) OnCompact(ctx context.Context, ev plugin.CompactEvent) (string, error) {
	s := p.snapshot()
	if s.store == nil || !s.autoExtract || len(ev.History) == 0 {
		return "", nil
	}
	complete := p.completeFunc()
	if complete == nil {
		return "", fmt.Errorf("当前没有可用的模型，跳过记忆提炼")
	}

	saved, err := p.extractMemories(ctx, s, complete, ev.History)
	if err != nil {
		return "", err
	}
	if len(saved) == 0 {
		log.Printf("记忆提炼：本次压缩没有找出值得长期保留的内容")
		return "", nil
	}

	names := make([]string, 0, len(saved))
	for _, e := range saved {
		names = append(names, e.Type+"/"+e.Name)
	}
	log.Printf("记忆提炼：新增 %d 条（%s）", len(saved), strings.Join(names, "、"))
	return fmt.Sprintf("（压缩前已从上文提炼并保存 %d 条长期记忆：%s）",
		len(saved), strings.Join(names, "、")), nil
}
