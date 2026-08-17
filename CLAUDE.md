# Wen Agent 项目约定

## 提示词规范（硬性要求）

1. **所有写入本项目的提示词一律使用中文**——包括工具的 Description、参数 Schema 描述、环境块、默认 system 提示词等，不使用英文提示词。
2. **严禁注入身份信息**——除 session 管理与工具调用所必需的功能性内容外，不得向模型上下文注入任何公司名、模型名等身份类提示词（避免污染底层模型的真实输出）。`agent.system_prompt` 默认留空，由用户在配置中自行决定。
3. **横切信息不进工具描述**——操作系统、Shell、工作目录、区域语言、时间等环境信息统一由 `internal/agent/env.go` 的 `[系统环境]` 块注入 system 消息开头，新增工具时描述只写功能本身。

## 插件架构约定

核心（agent / session / server / llm）不包含具体工具；工具能力一律通过 `internal/plugin` 的 `Plugin` 接口以插件形式提供，内置插件放 `internal/plugin/builtin/<name>/`，在 `cmd/wen/main.go` 的 `buildPlugins` 注册。插件名限小写字母、数字与下划线（`Register` 强制校验，因为它会被用来拼持久化目录）。`Register` 注册的插件来源为 `builtin`，`RegisterExternal` 为 `external`，界面上以「内置 / 外源」标签区分。插件可通过 `SystemPrompt()` 注入提示词片段（可返回空串不注入），注入位置在环境块之后、用户配置提示词之前。插件可选实现 `Configurable`（`ConfigFields() []ConfigField`）声明可配置项，Web UI 设置页据此在插件卡片上显示齿轮按钮并生成配置表单；字段类型有 `int` / `bool` / `string` / `select` / `text`（多行，渲染成 textarea）；保存时由 `Manager.SetConfig` 校验、重新 `Init` 使其立即生效。

**插件的开关与配置只有一个来源**：`<配置目录>/plugins.state.json`（0600，因为里面含插件配置），由设置页维护。config.yaml **不再**支持 `plugins:` 段——两处都能配的时候，哪一份在生效需要记住一条优先级规则，而界面的改动又不回写配置文件，于是文件里的内容会慢慢变成误导；残留的该段由 `config.warnLegacyPlugins` 提示一句后忽略，不报错。`main.go` 的 `buildPlugins` 只给出「状态文件还不存在时」的初值：默认启用，`needsSetupPlugins` 里的除外——不配置就没法工作的插件（roleplay 没角色设定、dual_persona 没触发词）默认打开只会让人以为功能坏了。初值的 `Config` 一律留空，插件自己声明的默认值就是初值，不在注册处重复一遍。

`NormalizeConfig` 的空串语义按类型区分：数值/开关/单选的空输入表示「用默认值」（界面对清空的 number input 提交的就是空串），而 `string` / `text` 的空串是合法取值——否则用户清空文本框后保存会看到默认值又长回来，字段永远清不掉。

插件可选声明 `Dependent`（`Requires() []string`）与 `Conflicting`（`Conflicts() []string`）。依赖是硬性的：依赖未满足时拒绝启用（校验放在 `Init` **之前**，避免产生副作用），被依赖的插件也无法在依赖方仍启用时关闭——拒绝而非级联关闭，因为界面没有确认或提示通道，级联只会表现为「另一个开关自己变灰了」。冲突只告警不阻止。依赖校验必须在**全部 `Register` 之后**由 `Manager.Resolve()` 统一做（register 是逐个进行的，依赖方可能先注册），且要显式检出依赖环。被强制关闭的插件记在 `entry.forcedOff` 而不是直接改 `enabled`：状态文件是全量重写的，直接改会把强制关闭固化成用户意图，依赖恢复后也回不来。

给核心加东西时守住一条界线：加进核心的必须是**通用机制**而非具体功能。已有十四处按此标准放行：

1. `InitContext.StateDir` —— 插件专属持久化目录；
2. `InitContext.SessionDir` —— 会话目录，只读用；
3. `InitContext.Complete` —— 辅助模型调用；
4. `Lifecycle` —— 会话生命周期通知；
5. **可见域**（`Scope` / `ScopeDecider` / `TurnPrompter`，见下节）；
6. `Requires` / `Conflicts` —— 插件间的依赖与互斥声明；
7. **操作确认**（`ConfirmFunc` / `WithConfirmer` / `ConfirmerFrom`，见下节）；
8. **插件发起轮次**（`InitContext.RunTurn` / `NewSession` / `Compact`，见「插件发起轮次约定」）；
9. `Stoppable` —— 插件停止钩子（禁用、重配、进程退出三处由 Manager 在**锁外**调用）；
10. `TurnObserver` —— 轮次结束观察（`Manager.NotifyTurnEnd` 逐个 recover 广播）；
11. **交互标记与会话活跃时间**（`WithInteractive` / `Meta.LastActiveAt`，见「插件发起轮次约定」）；
12. `InitContext.Status` —— 模型配置与会话用量快照，与 Web UI 的状态命令同源，远端界面（如 QQ 的 /status）据此保持一致输出。
13. **轮次过程通知**（`WithTurnNotes` / `TurnNotesFrom`，`internal/plugin/turn.go`）—— 发起方按需安装回调，核心在每轮模型响应后送出完整思考链与工具名批次（**只有名字**：参数与结果可能载有隐私，转发与否不由核心替接收方决定）；不安装零开销。IM 插件的「展示思考过程 / 展示工具调用」开关（默认都关）据此把过程转发到远端，措辞与 Web UI 对齐（🧠 思考过程 / 🔧 调用工具）。
14. **插件操作入口**（`Actionable`，`internal/plugin/action.go`）—— 插件声明可在设置页触发的操作（如扫码绑定），状态含说明文字与一张可选 PNG（只经内存下发不落盘）；`Actions()` 与 `SystemPrompt()` 同契约（廉价、Manager 持锁时调用），`StartAction` 立即返回、长流程放后台 goroutine 自带超时，进行中重复触发=重新开始。界面入口统一在齿轮的配置弹窗内（有操作或有配置项都会出齿轮，卡片上不单独摆按钮），点击操作转入进展弹窗；关闭弹窗只停轮询不打断流程。

任何插件都能用，核心不知道「记忆」「检索」「人格」「危险命令」「心跳」「定时」「QQ」或「微信」这回事。

远程 IM 插件目前有两个同构实现：`qq_bot`（QQ 官方开放平台，WebSocket 网关）与 `wechat_bot`（微信官方 ClawBot 插件，iLink HTTP 长轮询，扫码绑定走「插件操作入口」）。共同约定：每个远端用户映射一个普通会话、命令集 /new /status /compact /help /apply /deny、`WithInteractive` + 自带确认通道、白名单外一律拒绝只记日志、markdown 转纯文本共用 `internal/mdtext`。两者都实现 `TurnObserver`：**后台轮次**（`Origin` 非空且非自身，如心跳、定时任务）落在 IM 绑定的会话上时，把助手最终文本推送给绑定用户——否则结果只进会话文件，远端永远看不到；前台轮次与自己发起的轮次不推（各有回复渠道）。QQ 推送走主动消息（无 msg_id，受限容忍）；微信必须回带 context_token，故按用户持久化最近一次入站消息的 token（`tokens.json`，0600），没有 token 的用户只记日志。推送 goroutine 用插件自己的 ctx——广播的 ctx 在发起方轮次结束后立即被取消。

## 插件持久化与生命周期约定

需要落盘的插件用 `InitContext.StateDir` = `<配置目录>/plugins/<插件名>/`（由 `Manager.initCtxFor` 从 `statePath` 推导，目录可能不存在需自行创建）。该字段为空表示没有可用的持久化位置，插件应在 `Init` 中返回错误拒绝启用，**不要**退化到写进程当前目录。`plugins/` 已在 `.gitignore` 中。要读会话数据用 `SessionDir`（只读；写入一律走 `StateDir`）。

`InitContext.Complete` 让插件用当前模型做一次一问一答（不带工具、不启用思考、不写会话），由 `Agent.Complete` 实现。它在 Agent 建好之前就要传进 `buildPlugins`，故 `main.go` 用闭包延迟取值。为 nil 表示当前不可用，插件应降级而不是崩掉；每次调用都是真实开销，只放在低频且信息即将丢失的路径上。

`Lifecycle.OnCompact(ctx, CompactEvent) (note string, err error)` 在 `compact` 用 `store.Replace` 物理删除历史**之前**由 `Manager.NotifyCompact` **广播给所有订阅者**（自动与手动压缩共用一个调用点）：`memory` 借此提炼长期记忆，`session_search` 借此归档原文，`roleplay` 借此保住最后一处场景演绎，各管各的领域。返回的注记由核心追加到摘要消息末尾，因此只落进该会话的历史。插件返回 error 只记日志不阻断压缩：压缩是上下文溢出时的保底手段，不能被插件卡住。历史带可见域标签时按标签分组，每组一次事件，`CompactEvent.Scope` 给出本组的标签。

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

分两阶段：`ScopeDecider.DecideScope` 先定域，`TurnPrompter.TurnPrompt` 再按已定的域注入一次性提示词（不落盘）。之所以分开，是因为 `memory` 要按可读域过滤记忆索引，而域由 `dual_persona` 裁决，单阶段广播里两者无序。可见域是**单所有者**机制：按注册顺序第一个返回非零 `Scope` 的插件胜出，不做多插件合并（`Write` 来自一个插件而 `Read` 来自另一个是无法推理的组合）。

**不要改 `SystemPrompt()` 的签名**：它的契约是廉价、无副作用、随时可调（`Manager.List` 会对**禁用**的插件也调用它），需要会话上下文的内容一律走 `TurnPrompt`。`memory` 的记忆索引就是按这条从 `SystemPrompt` 挪走的。

几条实现上必须守住的规则：

- **可见域一轮只裁决一次**，随后全程从 `ctx` 取（`plugin.ScopeFrom`）。若在工具执行阶段再裁一次，同一轮的 assistant 与 tool 结果可能落到不同标签，`tool_use` / `tool_result` 会被永久拆散。
- **标签按整轮统一分配**，过滤因此是整轮粒度的，工具调用与其结果同去同留。`agent.sanitizeSequence` 兜住不变量被破坏的情况（外部改过 jsonl、进程中途被杀）。
- `Scope.Write` 会被插件拿去拼持久化目录，故按插件名的字符集校验，不合规的裁决整条作废降级为零值。按域分目录用 `plugin.DomainDir` / `plugin.ReadDomains`（空标签用基准目录本身，其余用同级的 `<base>-<tag>`），两个插件各写一套迟早漂移。
- **计数与报错也会泄漏存在性**：条数、"另有 N 条"、"已存在同名记忆 X" 这类输出必须按可读域算，否则内容藏住了、"存在什么"还是露了出来。会话标题同理（它取自首条用户消息，故 `session.Meta` 也带 `Tag`）。
- 压缩必须**按域分组**，每组一条带该组标签的摘要，否则不可读域的内容会经无标签的摘要泄漏出去。组序按标签**最后一次出现**排，使压缩不反转"最后一条带标签消息"所指向的域。
- 自动压缩判据取「实测用量」与「全量历史估算」的较大者：实测用量只反映本轮实际发出的、已过滤的上下文，只看它会让上下文很小的可见域永不触发压缩，而其它域的历史仍在无限增长。

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

新增 API 模式需在 `internal/llm/factory.go` 的 `KnownTypes` 注册并实现 `Provider` 接口。Anthropic 侧只支持当前世代模型的参数形式（`thinking:{type:"adaptive"}` + `output_config.effort`，不发 `temperature`），带签名的思考块通过 `llm.Message.ReasoningBlocks` 原样回传，无签名的一律丢弃。

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
