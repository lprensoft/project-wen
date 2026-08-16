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
| `model.provider` / `model.name` | 使用的 Provider 与模型 id |
| `model.thinking` | 思考模式：`off` / `low` / `medium` / `high` / `xhigh` / `max`，默认 `high`（开启时 `temperature` 不生效；思考过程在 Web UI 以可折叠块展示） |
| `model.context_length` | 模型上下文窗口（token），默认 1000000；超出预算时自动从最旧对话轮次整轮裁剪；会话实测占用达到 90% 时自动压缩为摘要 |
| `providers.<name>` | Provider 注册表：`type`（当前仅 `openai_compat`）、`base_url`、`api_key` |
| `agent.system_prompt` | 系统提示词（当前留空；运行时会自动在其前注入 `[系统环境]` 块：操作系统、Shell、工作目录、区域语言、当前时间） |
| `agent.max_turns` | 单次请求内工具循环上限 |
| `agent.workdir` | 插件与环境块共用的工作目录，空 = 进程当前目录 |
| `plugins.<name>` | 系统插件：`enabled` 初始状态 + `config` 插件配置；运行时开关持久化在 `<配置目录>/plugins.state.json`（优先于初始值，不回写本文件） |
| `session.dir` | 会话存储目录，默认 `<配置目录>/sessions` |

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/sessions` | 会话列表 |
| POST | `/api/sessions` | 新建会话 |
| GET | `/api/sessions/{id}` | 会话历史消息 |
| DELETE | `/api/sessions/{id}` | 删除会话 |
| POST | `/api/chat` | `{"session_id","message"}` → SSE 流（`delta` / `thinking` / `tool_start` / `tool_result` / `compact_*` / `done` / `error`） |
| GET | `/api/plugins` | 插件列表与状态 |
| PUT | `/api/plugins/{name}` | `{"enabled": bool}` 运行时开关插件 |

## 项目结构

```
cmd/wen/                 入口（注册内置插件、装配各层）
internal/config/         配置加载（YAML + ${VAR} 环境变量替换）
internal/llm/            Provider 接口 + OpenAI 兼容实现
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
