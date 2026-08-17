# Wen Agent

用 Go 编写的轻量通用 Agent（MVP）：对接 DeepSeek（OpenAI 兼容协议），自带 Web UI 聊天界面、工具调用循环与本地 Session 管理，单二进制部署。

## 特性

- **LLM 可配置**：`providers` 注册表 + `model` 选择，后续接入其他 OpenAI 兼容后端只需改配置
- **插件化架构**：精简核心（agent 循环 / session / server / llm）+ 插件扩展。工具能力全部由插件提供，插件可运行时开关（Web UI 设置页或 API），并可选择性向系统提示词注入片段、订阅会话生命周期事件、发起辅助模型调用。内置五个系统插件：`read_file`、`exec_command`、`web_fetch`、`memory`、`session_search`
- **长期记忆**：跨会话的持久事实库，索引随每次对话自动注入；模型可自行保存与读取，历史被压缩前还会**自动提炼**其中值得长期保留的结论（见下）
- **历史检索**：按关键词与日期回查原始对话，范围含现存会话与压缩前的完整归档
- **Web UI**：`go:embed` 内嵌单页聊天界面，SSE 流式输出，工具调用过程可视化
- **Session 管理**：每个会话一个 JSONL 文件（首行 meta + 逐行消息），重启不丢历史
- **单一配置文件**：全部配置（含 API Key）统一在 `config.yaml`（已被 .gitignore 保护），值支持 `${VAR}` 从环境变量读取

## 快速开始

```bash
# 1. 准备配置：复制示例并填入 providers.deepseek.api_key
cp config.example.yaml config.yaml

# 2. 运行
go run ./cmd/wen

# 3. 打开浏览器
# http://127.0.0.1:8080
```

配置文件查找顺序：`-c` 指定路径 → 当前目录 `config.yaml` → `~/.wen/config.yaml`。

```bash
wen -c /path/to/config.yaml -p 9000   # 指定配置与端口
```

## 配置说明

见 [config.example.yaml](config.example.yaml)。要点：

| 配置项 | 说明 |
|---|---|
| `model.provider` / `model.name` | 初始使用的 Provider 与模型 id（之后由设置页的「模型」栏目接管，见下） |
| `model.thinking` | 思考模式：`off` / `low` / `medium` / `high` / `xhigh` / `max`，默认 `high`（开启时 `temperature` 不生效；思考过程在 Web UI 以可折叠块展示） |
| `model.context_length` | 模型上下文窗口（token），默认 1000000；超出预算时自动从最旧对话轮次整轮裁剪；会话实测占用达到 90% 时自动压缩为摘要 |
| `providers.<name>` | Provider 注册表：`type`（`openai_compat` / `anthropic`）、`base_url`、`api_key` |
| `agent.system_prompt` | 系统提示词（当前留空；运行时会自动在其前注入 `[系统环境]` 块：操作系统、Shell、工作目录、区域语言、当前时间） |
| `agent.max_turns` | 单次请求内工具循环上限 |
| `agent.workdir` | 插件与环境块共用的工作目录，空 = 进程当前目录 |
| `plugins.<name>` | 系统插件：`enabled` 初始状态 + `config` 插件配置；运行时的开关与在设置页改过的配置持久化在 `<配置目录>/plugins.state.json`（优先于初始值，不回写本文件）。插件自身的数据落在 `<配置目录>/plugins/<插件名>/` |
| `session.dir` | 会话存储目录，默认 `<配置目录>/sessions` |

## 模型配置（设置页 → 模型）

提供商与模型可以在 Web UI 的设置页里增删改，并切换当前使用的组合，保存后**下一次对话请求即生效**，无需重启。

- 改动写入 `<配置目录>/models.json`（权限 0600，已在 `.gitignore` 中），**不回写 config.yaml**——因为 `api_key` 里的 `${VAR}` 在解析前就被展开，回写会把明文密钥固化进文件并丢掉全部注释。
- config.yaml 的 `providers:` 与 `model:` 段是初始值：同名条目由 models.json 完全覆盖；config.yaml 里**新增**的提供商仍会出现在列表中（标记「来自配置文件」），在界面上删除它会记一条删除坠碑，重启不会复活；从未在界面上改过的条目不写进 models.json，继续跟随配置文件。
- 每个模型条目可单独覆盖 `context_length` / `max_tokens` / `thinking` / `temperature`，留空则回退到 `model:` 段的全局值。
- **注意**：`${VAR}` 展开后的密钥一旦被写进 models.json 就固定下来，此后修改环境变量不再生效。

支持两种 API 模式：

| 模式 | 说明 |
|---|---|
| `openai_compat` | OpenAI 兼容协议（DeepSeek 等） |
| `anthropic` | Anthropic Messages API。思考档位映射为 `thinking:{type:"adaptive"}` + `output_config.effort`（`off` 则为 `disabled`）；当前世代 Claude 模型不接受采样参数，故不发送 `temperature`，模型条目里的该项对 Anthropic 无效 |

## 长期记忆（memory 插件）

会话历史不是可靠的记忆：超出上下文预算时最旧的轮次会被裁掉，实测占用达 90% 时整段历史会被**物理替换**成一条摘要。`memory` 插件在会话文件之外维护一层持久事实库。

- **存储**：一条记忆一个文件，YAML frontmatter（标题 / 摘要 / 分类 / 时间）+ Markdown 正文，可直接用编辑器增删改；手工放进去的普通 Markdown 也能被索引到。位置由 `scope` 决定：`global`（默认）存 `<配置目录>/plugins/memory/memories/`，`project` 存 `<工作目录>/.wen/memories/`。
- **索引**：每条用户消息注入一次，形如 `- 2026-05-10 约定/接口命名规范 — REST 路径用小写连字符`。索引**不落盘**，由各文件头部实时生成，因此不会与正文脱节。
- **工具**：`save_memory`（分类限 `偏好`/`约定`/`事实`/`踩坑`；默认拒绝覆盖同名条目，需显式 `mode: replace`，覆盖前留 `.bak`）、`recall_memory`、`list_memories`（可按关键词或分类过滤）、`delete_memory`。
- **压缩时自动提炼**：历史被摘要替换**之前**，用一次独立的模型调用从其中挑出值得长期保留的结论并直接落盘，已存在同名条目则跳过而不覆盖。做成真正的提取而不是提示模型稍后自己保存，是因为自动压缩无人值守、历史随即被删，「稍后」可能永远不会到来。代价是每次压缩多一次模型调用，可用 `auto_extract` 关掉。原始历史的保全由 `session_search` 的归档负责。

**索引开销**：索引在一次工具循环的**每一轮**都会完整发送（系统消息永不被上下文裁剪），因此有硬上限。实测一条约 31 token，100 条约 3100 token。超出字节上限时先省略各条摘要只留标题，仍超出才按最近更新截断并注明剩余条数——优先保住标题，模型才知道这条记忆存在、还能按名读取。

| 配置项 | 默认 | 说明 |
|---|---|---|
| `scope` | `global` | 记忆库范围：全局单库 / 按工作目录分库。切换后原位置的记忆不会自动迁移；用 `project` 时建议把 `.wen/` 加进该项目的 `.gitignore` |
| `max_index_entries` | 200 | 索引最多列出的条数 |
| `max_index_bytes` | 16384 | 索引字节上限（约 170 条带摘要） |
| `max_entry_bytes` | 8192 | 读取单条记忆的返回上限 |
| `auto_extract` | 开 | 压缩时自动提炼记忆（每次压缩多一次模型调用） |
| `max_extract` | 5 | 单次压缩最多提炼条数 |

## 历史检索（session_search 插件）

`memory` 存的是提炼过的结论；要回查当时的原话则用本插件。两者互补：记忆信噪比高、常驻提示词，原始对话保真但噪音大、按需检索。

- **工具**：`search_sessions`（关键词 + 日期范围，一次覆盖现存会话与压缩归档）、`read_session`（读某个会话，可按关键词或日期收窄）、`read_archive`（读某份归档全文；不传文件名则列出全部）。
- **压缩归档**：历史被摘要替换**之前**，完整原文（含思考过程与工具调用）原样写入 `<配置目录>/plugins/session_search/archives/`，并在摘要末尾留一行归档位置。只做文件写入，不发起模型调用。**被压缩过的会话在现存文件里只剩一条摘要，归档是其原文唯一的去处**，因此检索必须覆盖它，否则最值得回查的部分正好成了盲区。
- **日期筛选的坑**：会话 id 与 `Meta.CreatedAt` 是**创建**时间，而会话会跨天继续使用，所以不能只看文件名判断日期——实际按每条消息的 `ts` 过滤。两个方向的剪枝是安全的：文件修改时间早于起始日的可跳过（追加消息会更新文件时间），创建时间晚于结束日的可跳过（消息不会早于会话创建）。

| 配置项 | 默认 | 说明 |
|---|---|---|
| `max_scan_sessions` | 300 | 单次最多读取的会话数，按时间从新到旧；超出会在结果里注明 |
| `max_snippets` | 3 | 每个会话最多返回的摘录数 |
| `max_bytes` | 8192 | 单次返回上限 |
| `max_archives` | 20 | 保留的压缩归档数，0 = 不归档 |

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/sessions` | 会话列表 |
| POST | `/api/sessions` | 新建会话 |
| GET | `/api/sessions/{id}` | 会话历史消息 |
| DELETE | `/api/sessions/{id}` | 删除会话 |
| POST | `/api/chat` | `{"session_id","message"}` → SSE 流（`delta` / `thinking` / `tool_start` / `tool_result` / `compact_*` / `done` / `error`） |
| GET | `/api/plugins` | 插件列表与状态（含来源 `source`、可配置项声明 `config_fields` 与当前生效值 `config`） |
| PUT | `/api/plugins/{name}` | `{"enabled": bool}` 运行时开关插件 |
| PUT | `/api/plugins/{name}/config` | `{"config": {...}}` 保存插件配置，校验通过后立即生效并持久化 |
| GET | `/api/models` | 提供商与模型配置（`api_key` 只返回掩码） |
| PUT | `/api/models` | 整档保存；请求里 `api_key` 留空表示不修改 |
| PUT | `/api/models/current` | `{"provider","model"}` 切换当前模型，立即热生效 |
| POST | `/api/models/test` | 用一次极小的真实请求测试连接 |

## 项目结构

```
cmd/wen/                 入口（注册内置插件、装配各层）
internal/config/         配置加载（YAML + ${VAR} 环境变量替换）
internal/llm/            Provider 接口 + OpenAI 兼容 / Anthropic 实现
internal/modelcfg/       模型与提供商配置（models.json 覆盖层，热切换）
internal/agent/          Agent 循环（工具调用 / 思考 / 压缩 / 上下文预算）
internal/plugin/         插件协议（Plugin / Tool / Configurable / Lifecycle）+ Manager（开关与聚合）
internal/plugin/builtin/ 内置系统插件：readfile / execcmd / webfetch / memory / sessionsearch
internal/session/        JSONL 会话存储
internal/server/         HTTP API + SSE + 内嵌 Web UI
```

## 编写插件

实现 `internal/plugin` 的 `Plugin` 接口（`Name` / `Description` / `Init` / `SystemPrompt` / `Tools`），在 `cmd/wen/main.go` 的 `buildPlugins` 注册即可。约定：提示词与描述一律中文、只写功能、不含身份信息；`SystemPrompt()` 返回空串表示不注入；插件名限小写字母、数字与下划线（它会被用来拼持久化目录）。

两个可选接口：

- `Configurable`（`ConfigFields()`）——声明可配置项，设置页据此生成表单并持久化。
- `Lifecycle`（`OnCompact()`）——在会话历史被摘要替换**之前**收到通知，可借此归档或提炼；返回的注记会追加到摘要末尾，从而只落进该会话的历史。事件广播给所有订阅者（`memory` 与 `session_search` 都订阅了它，各做各的事），返回错误只记日志，不阻断压缩。

`InitContext` 提供四样运行环境：`Workdir`（工作目录）、`StateDir`（该插件专属的持久化目录 `<配置目录>/plugins/<插件名>/`，可能不存在需自行创建）、`SessionDir`（会话目录，只读用）、`Complete`（用当前模型做一次一问一答的辅助调用，不带工具、不写会话）。后三者为空/nil 表示当前不可用，插件应据此拒绝启用或降级，不要退化到写进程当前目录。`Complete` 每次调用都产生真实开销，只放在低频路径上。

## 开发

```bash
go build ./...   # 构建
go test ./...    # 测试
go vet ./...     # 静态检查
```

## 许可证

本项目基于 [GNU GPL v3.0](LICENSE) 开源。

## 后续规划

- 更多 Provider 类型（Ollama 等）与模型 fallback
- MCP 工具（接入后即为「外源」插件，插件卡片上的来源标签为此预留）
- 给 `Meta` 加 `UpdatedAt`：`session_search` 目前只能靠文件修改时间做单向剪枝，有了它可双向剪枝，会话列表也能按最近活跃排序
