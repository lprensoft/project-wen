# Wen Agent

用 Go 编写的轻量通用 Agent（MVP）：对接 DeepSeek（OpenAI 兼容协议），自带 Web UI 聊天界面、工具调用循环与本地 Session 管理，单二进制部署。

## 特性

- **LLM 可配置**：`providers` 注册表 + `model` 选择，后续接入其他 OpenAI 兼容后端只需改配置
- **插件化架构**：精简核心（agent 循环 / session / server / llm）+ 插件扩展。工具能力全部由插件提供，插件可运行时开关（Web UI 设置页或 API），并可选择性向系统提示词注入片段。内置三个系统插件：`read_file`、`exec_command`、`web_fetch`
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
| `plugins.<name>` | 系统插件：`enabled` 初始状态 + `config` 插件配置；运行时的开关与在设置页改过的配置持久化在 `<配置目录>/plugins.state.json`（优先于初始值，不回写本文件） |
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

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/sessions` | 会话列表 |
| POST | `/api/sessions` | 新建会话 |
| GET | `/api/sessions/{id}` | 会话历史消息 |
| DELETE | `/api/sessions/{id}` | 删除会话 |
| POST | `/api/chat` | `{"session_id","message"}` → SSE 流（`delta` / `thinking` / `tool_start` / `tool_result` / `compact_*` / `done` / `error`） |
| GET | `/api/plugins` | 插件列表与状态（含可配置项声明 `config_fields` 与当前生效值 `config`） |
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
internal/plugin/         插件协议（Plugin / Tool 接口）+ Manager（开关与聚合）
internal/plugin/builtin/ 内置系统插件：readfile / execcmd / webfetch
internal/session/        JSONL 会话存储
internal/server/         HTTP API + SSE + 内嵌 Web UI
```

## 编写插件

实现 `internal/plugin` 的 `Plugin` 接口（`Name` / `Description` / `Init` / `SystemPrompt` / `Tools`），在 `cmd/wen/main.go` 的 `buildPlugins` 注册即可。约定：提示词与描述一律中文、只写功能、不含身份信息；`SystemPrompt()` 返回空串表示不注入。

## 开发

```bash
go build ./...   # 构建
go test ./...    # 测试
go vet ./...     # 静态检查
```

## 许可证

本项目基于 [GNU GPL v3.0](LICENSE) 开源。

## 后续规划

- 更多 Provider 类型（Anthropic、Ollama 等）与模型 fallback
- Markdown 渲染、上下文压缩、MCP 工具
- Web UI 配置管理页
