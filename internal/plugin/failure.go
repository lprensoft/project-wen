package plugin

import "context"

// TurnFailure 描述一轮失败的对话：模型调用没能产出结果，错误即将呈现给用户。
type TurnFailure struct {
	SessionID string
	Err       error
}

// FailureTranslator 是插件的可选能力：把一轮失败转成一句面向用户的回复。
// 返回 (文本, true) 表示接手——核心将以正常助手消息呈现并落盘该文本，
// 原始错误转入会话注记；返回 ("", false) 表示不接手，错误按原样呈现。
// 什么样的失败值得转译由插件自己判断（核心不知道「拒绝」「拦截」这些概念），
// 但配置类错误应当放行：那类问题只有让用户看到原文才修得好。
type FailureTranslator interface {
	TranslateFailure(ctx context.Context, ev TurnFailure) (string, bool)
}
