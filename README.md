# Wen Agent

用 Go 编写的轻量通用 Agent（MVP）：对接 DeepSeek（OpenAI 兼容协议），自带 Web UI 聊天界面、工具调用循环与本地 Session 管理，单二进制部署。

## 特性

- **LLM 可配置**：`providers` 注册表 + `model` 选择，后续接入其他 OpenAI 兼容后端只需改配置
- **工具循环**：内置 `read_file`（读文件）与 `exec_command`（执行 shell 命令）两个示例工具，按 ReAct 循环自动调用
- **Web UI**：`go:embed` 内嵌单页聊天界面，SSE 流式输出，工具调用过程可视化
- **Session 管理**：每个会话一个 JSONL 文件（首行 meta + 逐行消息），重启不丢历史
- **配置分离**：`config.yaml` 放配置，`.env` 放密钥，支持 `${VAR}` 环境变量替换

## 快速开始

```bash
# 1. 准备配置（也可跳过——全部走默认值，只需设置环境变量 DEEPSEEK_API_KEY）
cp config.example.yaml config.yaml
cp .env.example .env        # 编辑 .env，填入真实 DEEPSEEK_API_KEY

# 2. 运行
go run ./cmd/wen

# 3. 打开浏览器
# http://127.0.0.1:8080
```

配置文件查找顺序：`-c` 指定路径 → 当前目录 `config.yaml` → `~/.wen/config.yaml`。`.env` 与配置文件同目录。

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
| `agent.tools.enabled` | 启用的工具列表 |
| `session.dir` | 会话存储目录，默认 `<配置目录>/sessions` |

## HTTP API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/sessions` | 会话列表 |
| POST | `/api/sessions` | 新建会话 |
| GET | `/api/sessions/{id}` | 会话历史消息 |
| DELETE | `/api/sessions/{id}` | 删除会话 |
| POST | `/api/chat` | `{"session_id","message"}` → SSE 流（`delta` / `tool_start` / `tool_result` / `done` / `error`） |

## 项目结构

```
cmd/wen/                 入口
internal/config/         配置加载（YAML + .env + ${VAR} 替换）
internal/llm/            Provider 接口 + OpenAI 兼容实现
internal/agent/          工具循环
internal/agent/tools/    Tool 接口与内置工具
internal/session/        JSONL 会话存储
internal/server/         HTTP API + SSE + 内嵌 Web UI
```

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
