# Wen Agent 项目约定

## 提示词规范（硬性要求）

1. **所有写入本项目的提示词一律使用中文**——包括工具的 Description、参数 Schema 描述、环境块、默认 system 提示词等，不使用英文提示词。
2. **严禁注入身份信息**——除 session 管理与工具调用所必需的功能性内容外，不得向模型上下文注入任何公司名、模型名等身份类提示词（避免污染底层模型的真实输出）。`agent.system_prompt` 默认留空，由用户在配置中自行决定。
3. **横切信息不进工具描述**——操作系统、Shell、工作目录、区域语言等环境信息统一由 `internal/agent/env.go` 的 `[系统环境]` 块注入 system 消息开头，新增工具时描述只写功能本身。时间不在其中，它归下面的「上下文分层约定」。

## 插件架构约定

核心（agent / session / server / llm）不包含具体工具；工具能力一律通过 `internal/plugin` 的 `Plugin` 接口以插件形式提供，内置插件放 `internal/plugin/builtin/<name>/`，在 `cmd/wen/main.go` 的 `buildPlugins` 注册。插件名限小写字母、数字与下划线（`Register` 强制校验，因为它会被用来拼持久化目录）。`Register` 注册的插件来源为 `builtin`，`RegisterExternal` 为 `external`，界面上以「内置 / 外源」标签区分。插件可通过 `SystemPrompt()` 注入提示词片段（可返回空串不注入），注入位置在环境块之后、用户配置提示词之前。插件可选实现 `Configurable`（`ConfigFields() []ConfigField`）声明可配置项，Web UI 设置页据此在插件卡片上显示齿轮按钮并生成配置表单；字段类型有 `int` / `bool` / `string` / `select` / `text`（多行，渲染成 textarea）；保存时由 `Manager.SetConfig` 校验、重新 `Init` 使其立即生效。

**插件的开关与配置只有一个来源**：`<配置目录>/plugins.state.json`（0600，因为里面含插件配置），由设置页维护。config.yaml **不再**支持 `plugins:` 段——两处都能配的时候，哪一份在生效需要记住一条优先级规则，而界面的改动又不回写配置文件，于是文件里的内容会慢慢变成误导；残留的该段由 `config.warnLegacyPlugins` 提示一句后忽略，不报错。`main.go` 的 `buildPlugins` 只给出「状态文件还不存在时」的初值：默认启用，`needsSetupPlugins` 里的除外——不配置就没法工作的插件（roleplay 没角色设定、dual_persona 没触发词）默认打开只会让人以为功能坏了。初值的 `Config` 一律留空，插件自己声明的默认值就是初值，不在注册处重复一遍。

`NormalizeConfig` 的空串语义按类型区分：数值/开关/单选的空输入表示「用默认值」（界面对清空的 number input 提交的就是空串），而 `string` / `text` 的空串是合法取值——否则用户清空文本框后保存会看到默认值又长回来，字段永远清不掉。

插件可选实现 `Categorized`（`Category() string`，`internal/plugin/category.go`）声明功能分组（基础工具 / 记忆与检索 / 角色演绎 / 后台任务 / 消息通道），设置页据此分节展示；未声明归「其他」。分组只影响展示，组序与组内顺序仍由注册顺序决定——注册顺序承载提示词注入顺序与依赖关系，不为展示而改。程序版本号唯一来源是 `internal/version`（界面左下角、/status 首行、启动日志共用），只用于面向用户的展示，不注入模型上下文。版本号同时编进发布产物：`tools/genwinres` 从它生成 Windows 版本资源（`cmd/wen/resource_windows_*.syso`，随库提交，`go build` 自动链入，exe 属性里可见），同一份资源里还编入程序图标。**品牌图标只有一处图源**：`internal/server/webui/assets/logo-512.png`，`tools/genicon` 由它生成多尺寸 `internal/server/webui/favicon.ico`（随库提交），浏览器标签页与 exe 图标共用这一个文件——分开维护迟早会变成两个不一样的图标。侧栏顶部的横版 Logo 同目录另有明暗两版，靠 CSS 的 `[data-theme]` 切换而非 JS。**发布改版本的流程**（顺序是硬的）：功能分支合并进 `main` → 改 `internal/version/version.go` → `go generate ./cmd/wen` 重新生成 .ico 与 .syso → 构建验证 → 提交（版本号与生成物必须在同一个提交里，否则 exe 属性里的版本与界面显示会分叉）→ 补 `CHANGELOG.md` 单独一个提交 → **在 CHANGELOG 那个提交上**打附注 tag（`git tag -a vX.Y.Z`）→ 推送 main 与 tag。tag 压在最后，是为了让它指向的那次提交自带本版本的更新日志——tag 打在前面的话，检出该 tag 得到的版本里没有它自己那一版的日志。CHANGELOG 的条目扫 `git log --no-merges <上一个 tag>..HEAD` 得到（跳过合并提交，它们与被合并的提交重复），此时新 tag 尚未存在，故取到 HEAD 为止。

推送 tag 之后的事由 `.github/workflows/release.yml` 接手：交叉编译六个平台（windows/linux/darwin × amd64/arm64）、打包、算 SHA256、把 CHANGELOG 里对应版本那一节作为正文创建 Release。全部目标从一台 ubuntu 交叉编译——本项目没有 cgo，平台差异靠 `//go:build` 分文件，`.syso` 文件名带 `GOOS_GOARCH` 后缀只在对应目标上链接。它同时把上面那条流程里最容易忘的两步变成硬性检查：**tag 与 `internal/version.Version` 必须一致**，以及 **`go generate ./cmd/wen` 后不能有 diff**（漏跑生成就意味着 exe 属性里的版本与界面显示分叉）。手动触发（workflow_dispatch）时只构建、不发布，产物挂在那次运行下，用来在打 tag 之前验证流水线。workflow 里的 `run:` 跑的是 `bash -e`，判断一律写成 `if`——`[ 条件 ] && 赋值` 在条件不成立时返回 1，会直接中断整个步骤。

`CHANGELOG.md` 按 [Keep a Changelog](https://keepachangelog.com/zh-CN/) 的约定写，几条硬性要求：**按改动类型分组**（新增 / 变更 / 修复 / 移除 / 废弃 / 安全），分组本身在传递「我此前是否受影响」；**一条改动一行**，不写「以前……现在……」的对照叙事（那属于提交信息，那里才有地方说为什么）；**只记使用者能感知的变化**，内部重构、测试与依赖调整不进；破坏性变更单列并排在最前。措辞面向使用者而非开发者，不出现接口名、类型名这类词。版本标题保留 Keep a Changelog 的方括号写法，但**不维护文件底部的链接定义**——那要求每发一版手工补一行，是整个流程里最容易漏的一步（已经漏过四版），而 compare 链接在 Release 页面本来就有。该文件不设「未发布」区、不由人工逐条维护，全部在打 tag 时从提交范围生成——这一条成立的前提是约定第 4 条（提交信息写清做了什么、为什么），重建的原料由它保证。但**扫出来的提交不能机械转写成条目**：先判断哪些使用者根本感知不到（内部重构、测试、依赖调整）并剔除，再把剩下的改写成使用者的语言。照抄提交标题省事，产出读起来就是提交日志，「任何人都能看懂」正好被牺牲掉。

插件可选声明 `Dependent`（`Requires() []string`）与 `Conflicting`（`Conflicts() []string`）。依赖是硬性的：依赖未满足时拒绝启用（校验放在 `Init` **之前**，避免产生副作用），被依赖的插件也无法在依赖方仍启用时关闭——拒绝而非级联关闭，因为界面没有确认或提示通道，级联只会表现为「另一个开关自己变灰了」。冲突只告警不阻止。依赖校验必须在**全部 `Register` 之后**由 `Manager.Resolve()` 统一做（register 是逐个进行的，依赖方可能先注册），且要显式检出依赖环。被强制关闭的插件记在 `entry.forcedOff` 而不是直接改 `enabled`：状态文件是全量重写的，直接改会把强制关闭固化成用户意图，依赖恢复后也回不来。

给核心加东西时守住一条界线：加进核心的必须是**通用机制**而非具体功能。已有十八处按此标准放行：

1. `InitContext.StateDir` —— 插件专属持久化目录；
2. `InitContext.Sessions` —— 会话的只读窄查询（最近活跃的会话、某会话是否还在）；`InitContext.SessionDir` —— 会话目录，只给要读会话**正文**的插件；
3. `InitContext.Complete` —— 辅助模型调用；
4. `CompactObserver` —— 压缩前通知；
5. **可见域**（`Scope` / `ScopeDecider` / `TurnPrompter`，见下节）；
6. `Requires` / `Conflicts` —— 插件间的依赖与互斥声明；
7. **操作确认**（`ConfirmFunc` / `WithConfirmer` / `ConfirmerFrom`，见下节）；
8. **插件发起轮次**（`InitContext.RunTurn` / `NewSession` / `Compact`，见「插件发起轮次约定」）；
9. `Stoppable` —— 插件停止钩子（禁用、重配、进程退出三处由 Manager 在**锁外**调用）；
10. `TurnObserver` —— 轮次结束观察（`Manager.NotifyTurnEnd` 逐个 recover 广播）；
11. **交互标记与会话活跃时间**（`WithInteractive` / `Meta.LastActiveAt`，见「插件发起轮次约定」）；
12. `InitContext.Status` —— 模型配置与会话用量快照，与 Web UI 的状态命令同源，远端界面（如 QQ 的 /status）据此保持一致输出。
13. **轮次过程通知**（`WithTurnNotes` / `TurnNotesFrom`，`internal/plugin/turn.go`）—— 发起方按需安装回调，核心在每轮模型响应后送出完整思考链与工具名批次（**只有名字**：参数与结果可能载有隐私，转发与否不由核心替接收方决定）；不安装零开销。IM 插件的「展示思考过程 / 展示工具调用」开关（默认都关）据此把过程转发到远端，措辞与 Web UI 对齐（🧠 思考过程 / 🔧 调用工具）。
14. **插件操作入口**（`Actionable`，`internal/plugin/action.go`）—— 插件声明可在设置页触发的操作（如扫码绑定），状态含说明文字、一张可选 PNG 与一个可选链接原文（PNG 给图形界面、链接给终端界面自渲染成字符二维码，均只经内存下发不落盘）；`Actions()` 与 `SystemPrompt()` 同契约（廉价、每次刷新列表都调用），`StartAction` 立即返回、长流程放后台 goroutine 自带超时，进行中重复触发=重新开始。界面入口统一在齿轮的配置弹窗内（有操作或有配置项都会出齿轮，卡片上不单独摆按钮），点击操作弹出进展窗盖在配置弹窗之上、**配置弹窗不关**（否则填了一半的内容会丢，「测试」类操作就等于要求先保存）；关闭进展窗只停轮询不打断流程。
15. **插件状态行**（`StatusReporter`，`internal/plugin/statusline.go`）—— 插件向状态命令贡献一行运行状况（如心跳报当前节奏与下次时机），`Manager.StatusLines` 按注册顺序只收**启用**插件的非空行（禁用的插件报一行「已停」只是噪声）；与 `SystemPrompt` 同契约（廉价、无副作用）；不得在 `StatusLines` 里回头调 `InitContext.Status`（那条路径又会回到 `StatusLines`，是无限递归），也不能在 `Init` 内查状态，那仍是写锁内。措辞由插件自己负责，核心不解释内容；三处输出（Web UI、QQ、微信）统一从 `StatusInfo.PluginLines` / `/api/status` 的 `plugin_lines` 取同一份数据，接在会话行之后。 状态文本的**措辞**同样只有一份：Go 侧在 `internal/statustext`（QQ 与微信共用，此前是两份逐字节相同的复制品，改一处忘一处就会分叉），Web UI 因为跑在浏览器里另有一份（`app.js` 的 `runStatus`），改措辞时两边要一起动，`internal/statustext` 的测试盯着格式。版面按「一行说一件事」压紧：模型与思考深度同行，上下文窗口并进会话那一行——窗口大小单独占一行时，读的人还得自己拿它和用量做除法，而占用比例本来就在旁边。
16. **会话注记**（`InitContext.Notice`，`internal/agent/notice.go`）—— 插件往一个会话里留一行只给人看的说明（`session.KindNotice`）：落盘、在界面展示，但**永不进入模型上下文**，也不进压缩摘要、不计入 token 估算（与 `KindEphemeral` 正好相反，那个是「给模型看一轮、界面不当用户消息展示」）。存在的理由是后台工作与轮次不同步——插件发起的活儿在轮次收尾之后才跑完，那时 `/api/chat` 的事件流已经关闭，结果只能进日志。标签取自 ctx 的可见域（「在人格 A 的库里记了什么」也属于人格 A），发起方由 Manager 注入。实时送达经 `Agent.SetNoticeSink` → server 的 `noticeHub` → 常驻的 `GET /api/events`（一条流服务所有会话，前端按当前会话筛；订阅者积压就丢，内容已落盘、刷新即补齐）。`AppendNotice` **刻意不取轮次锁**：工具的 `Execute` 也可能想写一条，而那时本轮正持着锁，取锁就是自锁；代价是与并发压缩的 `Replace` 有极小概率丢一条注记，注记是旁注不是对话内容，丢了不影响任何后续行为。
17. **操作的草稿配置值**（`WithActionValues` / `ActionValuesFrom` / `ActionValueOr`，`internal/plugin/action.go`）—— 触发插件操作时，把配置弹窗里**尚未保存**的表单值经 ctx 一并交给插件，使「测试」类操作能先验后存（`weather` 测城市能不能解析，同样的形状可用于任何「测试连接」）。走 ctx 而不是给 `StartAction` 加参数，是为了不动已有实现的签名；值未经校验，就是界面原样提交的内容。**必须在 `StartAction` 内同步取出**——ctx 属于那个 HTTP 请求，响应发出后就失效了，而长流程在后台 goroutine 里。

18. **轮次失败转译**（`FailureTranslator`，`internal/plugin/failure.go`）—— 轮次失败时给插件一个把失败转成一句正常回复的机会（roleplay/dual_persona 据此把内容拦截演成角色的走神），核心不知道「拒绝」「拦截」这些概念。单所有者，但征询按**逆注册序**：提示词注入的约定是靠后注册的片段覆盖靠前的（里人格设定「优先于上文的角色设定」），一句台词该由最上层的声音说出——dual_persona 因此先于 roleplay 拿到里人格的轮次。只在真人在场（`WithInteractive`）的轮次尝试：后台轮次没人看，转译白费一次调用，还会把失败伪装成成功、干扰发起方（如心跳）的判断。转译文本以正常助手消息落盘（带本轮可见域标签），**原始错误转入会话注记**，真相不丢；配置类错误（`llm.IsConfigError`：401/403/404）插件必须放行原样报出，那类问题只有看到原文才修得好。

任何插件都能用，核心不知道「记忆」「检索」「人格」「场景」「现场」「天气」「身体」「心情」「危险命令」「心跳」「定时」「技能」「QQ」「微信」「飞书」「Lark」或「Telegram」这回事。

`skills` 是这条界线的一次正例检验：外面那套「技能」（一批用户自己安装的 SKILL.md，模型按需读取）在本项目里不需要核心加任何东西——`StateDir` 拿目录、`SystemPrompt` 常驻一份「名称 + 用途」的清单、工具返回正文，现有契约刚好够用。它的正文之所以走**工具返回值**而不是任何提示词注入，是「上下文分层约定」直接推出来的：按需加载意味着内容会在第 N 轮才出现，落进 system 或 `<本轮状态>` 都会让整段前缀作废，而工具结果是追加进历史的，此后前缀不再变动。它排在角色演绎那组之后，理由与 `memory` 相同（见 `buildPlugins` 的注释）。

**核心对插件的回调一律在锁外进行。** `Manager` 先在锁内快照「实现了某个接口的启用插件」（`enabledAs`），再到锁外逐个调用，每次调用都过 `safely` 兜住 panic（单个插件不该连累整轮对话）。这不是风格偏好：Go 的 `RWMutex` 在有写者排队时会挡住后续所有 `RLock`，读锁一旦跨进插件代码，插件里一次反向调用就会永久卡住——而 `OnCompact` 里有一次真实的模型往返，那把锁会一直握到提炼结束，期间在设置页拨一下开关就足以让整个服务停住。代价是快照与调用之间有个窗口，期间刚被禁用的插件仍会被回调一次，可接受（插件本就要求自行加锁、`Init` 可重入）。例外只有两处，都留在锁内：`Init` 要与开关状态一起原子完成，依赖与冲突的推算要看一致的整张注册表——因此 `Init` 内不得反向调用 `Manager`，`Requires` / `Conflicts` 必须是静态声明。

远程 IM 通道目前是**五个插件、四套协议实现**：`qq_bot`（QQ 官方开放平台，WebSocket 网关）、`wechat_bot`（微信官方 ClawBot 插件，iLink HTTP 长轮询，扫码绑定走「插件操作入口」）、`feishu_bot` 与 `lark_bot`（飞书 / Lark，同一套实现的两次实例化）、`telegram_bot`（Bot API，HTTP 长轮询）。

**通道无关的部分一律走 `internal/imbot`**，不在插件里各写一份：入站分发（去重、准入、`/apply` `/deny` 直投确认代理）、命令集 /new /status /compact /help、按用户串行的 worker、会话绑定与持久化（`sessions.json`）、确认代理、`WithInteractive` 与过程通知转发。通道只实现 `imbot.Sender`（把一段文本发给某个用户）并把入站消息归一化成 `imbot.Message`。这是补课补出来的：QQ 与微信的确认代理曾是逐行相同的两份，命令层约六成重合——`internal/statustext` 当初正是这么分叉的。

留在插件侧的只有协议层：鉴权、收发、错误码、分段上限与格式降级（markdown 转纯文本共用 `internal/mdtext`）、推送凭据。`Message.ReplyTo` 是通道自定义的回复凭据，骨架不解释内容原样交回 `Sender`（QQ 是 msg_id，微信是 context_token，Telegram 是 message_id，飞书是 message_id）。两个可选钩子把通道特有的概念留在通道里：`OnAccepted`（微信记住 context_token）与 `Typing`（微信的输入状态、Telegram 的 sendChatAction）。`Push` 是主动推送（没有可回带的入站凭据时用），返回值要如实报告「有没有真的交给平台」——微信缺 context_token 时推不出去，转投方据此才知道那段话没送达。`Config.Allow` 为 nil 表示**一律拒绝**而不是一律放行——某条通道忘了实现不该变成对全网开放。

五个插件都实现 `TurnObserver`：**后台轮次**（`Origin` 非空且非自身，如心跳、定时任务）落在 IM 绑定的会话上时，把助手最终文本推送给绑定用户——否则结果只进会话文件，远端永远看不到；前台轮次与自己发起的轮次不推（各有回复渠道）。推送凭据各不相同：QQ 走主动消息（无 msg_id，受限容忍）；微信必须回带 context_token，故按用户持久化最近一次入站消息的 token（`tokens.json`，0600），没有 token 的用户只记日志；Telegram 与飞书按 chat_id / open_id 直接发。推送 goroutine 用插件自己的 ctx——广播的 ctx 在发起方轮次结束后立即被取消。

**同一套协议的不同租户域做成两个插件**（`feishu_bot` / `lark_bot`），判据是「凭证不通用且要能同时连」：飞书与 Lark 的接口、事件、SDK 完全一致，但国际版应用不能用于中国版，open_id 也是两套；一个插件只能存一份凭证就只能连一边。协议代码只写一遍，差异集中在 `larkbot.variant`（插件名、默认域、开发者后台地址、文案里的产品名），插件名不同意味着 `StateDir` 不同，白名单与会话绑定天然隔离。域**不做成配置项**——能在界面上改就等于允许「飞书插件连 Lark」，那是个必然失败的组合。以后再来一条 IM 时按这条判据决定合还是分。

飞书的收消息是唯一一处「协议层不自己写」的例外：长连接的线格式是 protobuf 私有协议，官方不公开，只能用 `github.com/larksuite/oapi-sdk-go/v3` 的 `ws` 包（好处是本机就能收事件，不需要公网地址）。发消息仍是手写 HTTP。SDK 回调带进来的 ctx 属于那一次事件投递，**不能拿去跑轮次**，要用插件自己的 ctx。

## 插件持久化与生命周期约定

需要落盘的插件用 `InitContext.StateDir` = `<配置目录>/plugins/<插件名>/`（由 `Manager.initCtxFor` 从 `statePath` 推导，目录可能不存在需自行创建）。该字段为空表示没有可用的持久化位置，插件应在 `Init` 中返回错误拒绝启用，**不要**退化到写进程当前目录。`plugins/` 已在 `.gitignore` 中。要读会话数据分两档，**按需要的最窄那一档取**：只想知道「该落在哪个会话上」或「记下的会话还在不在」，用 `InitContext.Sessions`（`SessionQuery`：`LastActive` / `LastInteraction` / `Exists`）；真要读会话**正文**（检索、归档）才用 `SessionDir`。目录是个读写路径，为回答一个布尔值把全部对话的读写权限交出去不划算——心跳、定时任务与两个 IM 插件从前都是这么拿的。写入一律走 `StateDir`。

「最近活跃的会话」的判定规则（按 `LastActiveAt` 排序，旧会话缺该字段时回落 `CreatedAt`）归核心，不要在插件里各写一份——心跳与定时任务曾经就是两份逐行相同的复制品。注意 `LastActive` 与 `LastInteraction` 问的不是同一件事：前者挑会话，后者只答「上一次有人来过是什么时候」，把刚创建的空会话当成「有人来过」会让空闲衰减永不触发。

`InitContext.Complete` 让插件用当前模型做一次一问一答（不带工具、不启用思考、不写会话），由 `Agent.Complete` 实现。它在 Agent 建好之前就要传进 `buildPlugins`，故 `main.go` 用闭包延迟取值。为 nil 表示当前不可用，插件应降级而不是崩掉；每次调用都是真实开销，只放在低频且信息即将丢失的路径上。「低频」是硬要求：心跳曾用它在**每一轮真人对话结束后**判定聊天热度，只为换回「加快/放缓/保持」三个词，于是用户每发一条消息都要多付一次模型调用——那件事后来改成了给模型一个工具（`set_heartbeat_interval`），模型在对话里自己定下次开口的时机，每轮零额外开销且粒度更细。**能让模型顺手用工具表达的，不要另起一次调用去问它。**

`CompactObserver.OnCompact(ctx, CompactEvent) (note string, err error)` 在 `compact` 用 `store.Replace` 物理删除历史**之前**由 `Manager.NotifyCompact` **广播给所有订阅者**（自动与手动压缩共用一个调用点）：`memory` 借此提炼长期记忆，`session_search` 借此归档原文，`roleplay` 借此保住最后一处场景演绎，各管各的领域。返回的注记由核心追加到摘要消息末尾，因此只落进该会话的历史。插件返回 error 只记日志不阻断压缩：压缩是上下文溢出时的保底手段，不能被插件卡住。历史带可见域标签时按标签分组，每组一次事件，`CompactEvent.Scope` 给出本组的标签。

**定时类状态记「上一次发生的时刻」，不记「还剩多久」。** 进程内的定时器只是执行手段，不是状态载体——它随进程消失，于是重启就把倒计时清零重算，重启比周期更频繁时那件事一次都不会发生（心跳曾经如此：只存了间隔，没存上次心跳时刻）。存了时刻之后，下一次由「时刻 + 周期」推算，重启天然延续；已经过期就补一次，但要给一个启动宽限期，别在服务刚起来的那一秒就动作。同理，带时效的缓存要连同取得时刻一起落盘（天气的观测），否则重启既产生数据空窗，又白打一次外部接口。按轮数累计的缓冲同理（`memory` 的提炼窗口）：只在内存里攒，重启就归零，「每 N 轮做一次」在重启比 N 轮更频繁时永远走不完。`scheduler` 的 `LastRun`、`mood` 的 `Updated` 是这条的既有实现，新写这类插件时照着来。另注：每轮都起 goroutine 写盘时，它们的完成顺序不保证，必须串行化并丢弃过期快照，否则晚到的旧内容会盖掉新进展。

压缩这个时机上要做的事必须**当场做完**，不能靠提示模型"稍后自己处理"：自动压缩无人值守触发、历史随即被物理删除，而"稍后"可能永远不会到来（会话可能就此结束）。

注意 `Init` 可能在运行时被反复调用（`SetConfig` 会重新 `Init`），此时可能有 in-flight 的 `Execute` 正在读插件字段——有状态的插件必须自己加锁，不要照抄现有几个内置插件在 `Init` 里裸写字段的写法。

插件向 system 消息注入的内容会随**每一轮** LLM 调用重复发送（`trimToBudget` 永不裁 `msgs[0]`），且计入自动压缩的判据。因此体量会增长的注入内容必须有硬上限，超限时降级要保住"存在性"信息而不是整条丢弃。

## 插件发起轮次约定

插件能以编程方式跑一轮完整对话：`InitContext.RunTurn`（写会话、带工具、注入 system 提示词，返回助手最终文本）、`NewSession`、`Compact`，均为闭包延迟绑定（`main.go`），nil 表示不可用应降级。`Manager.initCtxFor` 下发 `RunTurn` 时自动注入发起方标记（`WithTurnOrigin(插件名)`），插件无法伪装成前台；发起方随整轮消息落盘到 `StoredMessage.Origin`。

**per-session 轮次锁**（`Agent.turnLocks`）：前台 `Run` / `Compact` 排队；插件的 `RunTurn` / `CompactTurn` 忙时立即返回 `plugin.ErrSessionBusy` **不排队**——后台任务堆在锁上只会在解锁瞬间连环轰炸会话，跳过等下个周期才是对的。这把锁同时根治并发轮次交错写历史与压缩 `Replace` 覆盖并发 `Append` 的丢消息竞态。

**交互标记**：真人在对面的轮次（Web UI 的 chat handler、QQ 等远程 IM 插件）用 `WithInteractive(ctx)` 标记，核心只在此时更新 `Meta.LastActiveAt`。「最近活跃会话」按它判定（缺字段回落 `CreatedAt`，查询逻辑放插件侧）；机器自发的轮次（心跳、定时任务）不标记，否则以最近活跃会话为落点的后台功能会自我续命。

机器注入的提示词（如心跳）用 `WithEphemeralInput(ctx)` 标记为**一次性输入**：只在当轮发给模型，落盘带 `Kind=ephemeral`（审计与界面提示仍在），后续轮次的上下文、压缩摘要、会话标题都不含它，Web UI 渲染成一行来源提示而非用户气泡；助手的回复照常保留。抽掉 user 消息后 Anthropic 侧可能出现连续 assistant，其组装层本就合并同角色消息，安全。

后台轮次**不注入 confirmer**，execcmd 的「拿不到答复=拒绝」正是无人值守想要的默认；要接入远程确认（如 QQ 的 /apply //deny）由发起方插件自己 `WithConfirmer`。起 goroutine 的插件必须实现 `Stoppable`（只做取消 + 有界等待，不得等整轮对话），并保证 `Init` 可重入（先停旧循环再起新的）。`TurnObserver.OnTurnEnd` 在轮次收尾的同步路径上广播，实现必须快速返回，耗时工作自行开 goroutine。

## 可见域约定

同一会话内的消息可以分区，使一部分内容对某些轮次不可见（`internal/plugin/scope.go`）。**标签的语义完全由插件定义**，核心只做三件事：给消息打标签、按标签过滤历史、按标签分组压缩——它不知道"人格"这回事。约定 **空标签始终对所有域可见**：升级前无标签的历史因此继续可用，"共享"也不需要任何插件参与就成立。

分两阶段：`ScopeDecider.DecideScope` 先定域，`TurnPrompter.TurnPrompt` 再按已定的域注入一次性提示词（不落盘，注入位置见「上下文分层约定」：随 `<本轮状态>` 走在历史之后，不进 system）。之所以分开，是因为 `memory` 要按可读域过滤记忆索引，而域由 `dual_persona` 裁决，单阶段广播里两者无序。可见域是**单所有者**机制：按注册顺序第一个返回非零 `Scope` 的插件胜出，不做多插件合并（`Write` 来自一个插件而 `Read` 来自另一个是无法推理的组合）。

**不要改 `SystemPrompt()` 的签名**：它的契约是廉价、无副作用、随时可调（`Manager.List` 会对**禁用**的插件也调用它），需要会话上下文的内容一律走 `TurnPrompt`。`memory` 的记忆索引就是按这条从 `SystemPrompt` 挪走的。

几条实现上必须守住的规则：

- **可见域一轮只裁决一次**，随后全程从 `ctx` 取（`plugin.ScopeFrom`）。若在工具执行阶段再裁一次，同一轮的 assistant 与 tool 结果可能落到不同标签，`tool_use` / `tool_result` 会被永久拆散。
- **标签按整轮统一分配**，过滤因此是整轮粒度的，工具调用与其结果同去同留。`agent.sanitizeSequence` 兜住不变量被破坏的情况（外部改过 jsonl、进程中途被杀）。
- `Scope.Write` 会被插件拿去拼持久化目录，故按插件名的字符集校验，不合规的裁决整条作废降级为零值。按域分目录用 `plugin.DomainDir` / `plugin.ReadDomains`（空标签用基准目录本身，其余用同级的 `<base>-<tag>`），两个插件各写一套迟早漂移。
- **计数与报错也会泄漏存在性**：条数、"另有 N 条"、"已存在同名记忆 X" 这类输出必须按可读域算，否则内容藏住了、"存在什么"还是露了出来。会话标题同理（它取自首条用户消息，故 `session.Meta` 也带 `Tag`）。
- 压缩必须**按域分组**，每组一条带该组标签的摘要，否则不可读域的内容会经无标签的摘要泄漏出去。组序按标签**最后一次出现**排，使压缩不反转"最后一条带标签消息"所指向的域。
- 自动压缩判据取「实测用量」与「全量历史估算」的较大者：实测用量只反映本轮实际发出的、已过滤的上下文，只看它会让上下文很小的可见域永不触发压缩，而其它域的历史仍在无限增长。

**可见域可以绑通道**（`internal/imbot/route.go`）：一个插件安装 `imbot.SetRouter`，通道就按「这个会话的回复该发往哪条通道」投递，与这一轮从哪条通道进来无关。`dual_persona` 用它让表里两面各占一条 IM——在 QQ 上说出暗号，接手的那一面在微信上回话。规则只有一条（回复跟着人格走），推论都是想要的：说暗号那一轮当场就转投（裁决在轮次开头已经写下新人格）、后台轮次也跟着人格走、两条通道不再各推一份。核心不参与，`imbot` 也只认通道名——「表 / 里」是 `dual_persona` 的词汇。几处必须守住的：**转投失败不回落到来源通道**（回落等于把另一面的话直接摆到这一面的窗口里，正是要防的事），丢弃并留一条会话注记；**过程通知（思考链、工具名）与最终回复走同一条出口**，否则思考过程会漏在另一侧；**命令回执、错误提示与确认请求一律原路回**（确认的 pending 登记在来源通道的 broker 上，投到别处没人能答）。目标通道上没人绑到该会话时，只在「已知用户恰好一人」时把他接过来，多于一人宁可不送达——把一面的话广播给该通道上每个聊过天的人，是这个功能能造成的最坏后果。另注：`TurnObserver` 的推送判定必须排除**由某条通道发起**的轮次（`imbot.IsChannel(ev.Origin)`），否则两条通道绑在同一会话上时，一边的前台回复会被另一边再推一遍。

**可见域是上下文隔离，不是沙箱。** `read_file` 与 `exec_command` 是通用的文件与命令通道，能直接读到会话文件、记忆目录与插件状态文件，绕过一切提示词与工具层过滤。`dual_persona` 把它们声明为 `Conflicts` 告警，但不阻止——这是产品前提，不是待修的漏洞。

## 操作确认约定

不可逆的操作在执行前交由人判断（`internal/plugin/confirm.go` + `internal/server/confirm.go`）。核心只提供通道，**不知道什么算危险**——由插件自己判定风险并发起请求。

链路：工具用 `plugin.ConfirmerFrom(ctx)` 取通道并调用 → server 侧的确认器经当前对话的 SSE 流发 `confirm_request` 帧、在 broker 里登记一个 channel 并阻塞等待 → 浏览器 `POST /api/confirmations/{id}` → broker 交回答复 → 再发一帧 `confirm_done` 让界面定稿。

- 确认器**按请求注入**（`WithConfirmer`）而不是放进 `InitContext`：确认必须回到发起这轮对话的那个界面，而插件是进程级单实例。
- 工具执行是 `run` 循环里的**同步调用**，所以阻塞在这里就等于这一轮对话停住了、命令还没执行——这正是想要的效果，同时也意味着 SSE 写入仍是单 goroutine，不需要加锁。
- **拿不到答复不等于得到许可**：`ConfirmerFrom` 返回 false（无可交互界面）、返回 error（超时、断开）都必须按拒绝处理。别给「无人值守就放行」的开关，要放行就用插件自己的总开关，那是个看得见的选择。
- 等待必须有上限（`ctx` 超时）并在结束时 `release`，否则 broker 的 pending 会一直涨、工具会永久挂住。
- 拦截生效时要把规则**告诉模型**，否则被拒绝的模型会改写命令重试。

关于 `exec_command` 为什么选拦截确认而不是沙箱，见 `internal/plugin/builtin/execcmd/guard.go` 顶部的说明：shell 接的是任意字符串，靠文本分析做路径约束是能被轻易绕过的安全假象，而用户会信它。

## 模型配置约定

提供商与模型由 `internal/modelcfg` 管理：config.yaml 的 `model:` / `providers:` 段是初始值，Web UI 的改动写入 `<配置目录>/models.json` 覆盖层（0600 权限，含明文密钥，不入库），**永不回写 config.yaml**（`${VAR}` 在解析前展开，回写会固化明文并丢注释）。同名条目由覆盖层完全覆盖，config.yaml 新增的提供商仍可见（`source=config`），界面删除写「删除坠碑」，未改动过的条目不落盘。保存与切换后由 `Agent.SetModel` 热生效，一轮请求全程使用入口处取的同一份快照。

新增 API 模式需在 `internal/llm/factory.go` 的 `KnownTypes` 注册并实现 `Provider` 接口（构造参数走 `llm.Config`）。Anthropic 侧只支持当前世代模型的参数形式（`thinking:{type:"adaptive"}` + `output_config.effort`，不发 `temperature`），带签名的思考块通过 `llm.Message.ReasoningBlocks` 原样回传，无签名的一律丢弃。

提供商级的 `prompt_cache`（config.yaml 的 `providers.<name>.prompt_cache` 与设置页的开关，三态 `*bool`，未设置=开启）只对 anthropic 有意义——只有它要求调用方在请求里显式打断点，且缓存写入单独计费。它必须是个**看得见的开关**而不是默认行为：命中按约十分之一计费，而未命中的写入要多付约四分之一，使用节奏比缓存有效期还慢的人（消息间隔常常超过几分钟）开着就是一直在付写入的钱。openai_compat 那边（DeepSeek、OpenAI）的缓存由服务端自动维护，没有开关可言，命中与否只取决于前缀是否稳定。

## 上下文分层约定

一轮请求的上下文分**稳定段**与**易变段**两层，`internal/agent/agent.go` 的 `run` 按这个顺序组装：

    system（[系统环境] + 各插件 SystemPrompt + 用户配置提示词）
    历史（隔得久的地方插一行「此处距上一条消息……」）
    <本轮状态>（当前时间 + 距上一条消息 + 各插件 TurnPrompt）+ 本轮用户输入

**当前时间与插件的每轮片段一律不进 system**。这一条同时服务两件事，改任何一处之前先想清楚这两件：

1. **模型对「现在」的判断**。放在 system 开头时，它离生成位置有几千 token 远，而历史对话里出现过的时刻是叙事事实、就在眼前——多轮之后模型会把上次那个时刻当成现在，接着演深夜。放到历史之后并宣告「与历史里的说法不一致时以这里为准」才压得住。
2. **提示词缓存**。缓存是前缀精确匹配的，system 里放一个每轮都变的时间戳，等于让整段 system 与历史永远无法命中——DeepSeek 这类服务端自动缓存的提供商也一样，那里没有开关可调，唯一的抓手就是前缀别变。

由此得到两条硬性要求：**`SystemPrompt()` 的返回值必须整轮之间逐字节一致**（它按契约本来就是无参、无副作用的，做到这点是自然的），会随会话变化的内容一律走 `TurnPrompt`；**`<本轮状态>` 块里不放会话历史级别的长内容**，它每轮重发且永远不命中缓存。`internal/agent/context_layout_test.go` 盯着这两条。

历史里的间隔标记只加在发出去的副本上，不落盘（`StoredMessage.TS` 本来就记着时间，只是从不发给模型）。压缩摘要末尾同样带一个时间锚——摘要是 pinned 的、永不裁剪，不带锚的话它里面的时段描述会被一直当成「现在」。间隔的**计算与措辞归核心**，插件不要自己再报一遍（roleplay 原本有个 `[对话间隔]`，已并入核心，它现在只说间隔长了该怎么演）。

## Git 工作流约定

1. **功能开发一律先建分支**（`feature/<功能名>`），实现并验证通过后合并回 `main`，不直接在 `main` 上开发。
2. **敏感与本地文件不入库**：`config.yaml`（本地配置，含 API Key）、`sessions/`（会话数据）、`*.log`、构建产物均已在 `.gitignore` 中，新增此类文件时同步维护忽略列表。
3. **推送前必须做一次敏感信息检查**：对 `origin/main..HEAD` 的全部 diff 扫描密钥模式（API Key、token、`Bearer`/JWT 形态的长串）、真实的 AppID/AppSecret/openid、个人身份信息；测试与示例一律用明显的占位值（如 `"123"`），不用任何真实凭据脱敏改造。发现疑似内容先停下与用户确认，不带病推送。
4. 提交信息用中文，说明"做了什么、为什么"。

## 常用命令

```bash
go build -o wen.exe ./cmd/wen   # 构建
go test ./...                   # 测试
go run ./cmd/wen                # 运行（读取 ./config.yaml，密钥也在其中）
```
