# Wen Agent 项目约定

## 提示词规范（硬性要求）

1. **所有写入本项目的提示词一律使用中文**——包括工具的 Description、参数 Schema 描述、环境块、默认 system 提示词等，不使用英文提示词。
2. **严禁注入身份信息**——除 session 管理与工具调用所必需的功能性内容外，不得向模型上下文注入任何公司名、模型名等身份类提示词（避免污染底层模型的真实输出）。`agent.system_prompt` 默认留空，由用户在配置中自行决定。
3. **横切信息不进工具描述**——操作系统、Shell、工作目录、区域语言、时间等环境信息统一由 `internal/agent/env.go` 的 `[系统环境]` 块注入 system 消息开头，新增工具时描述只写功能本身。

## 插件架构约定

核心（agent / session / server / llm）不包含具体工具；工具能力一律通过 `internal/plugin` 的 `Plugin` 接口以插件形式提供，内置插件放 `internal/plugin/builtin/<name>/`，在 `cmd/wen/main.go` 的 `buildPlugins` 注册。插件可通过 `SystemPrompt()` 注入提示词片段（可返回空串不注入），注入位置在环境块之后、用户配置提示词之前。插件可选实现 `Configurable`（`ConfigFields() []ConfigField`）声明可配置项，Web UI 设置页据此在插件卡片上显示齿轮按钮并生成配置表单；保存时由 `Manager.SetConfig` 校验、重新 `Init` 使其立即生效。运行时开关状态与界面上改过的配置存 `<配置目录>/plugins.state.json`（优先于 config.yaml，不回写 config.yaml）。

## 模型配置约定

提供商与模型由 `internal/modelcfg` 管理：config.yaml 的 `model:` / `providers:` 段是初始值，Web UI 的改动写入 `<配置目录>/models.json` 覆盖层（0600 权限，含明文密钥，不入库），**永不回写 config.yaml**（`${VAR}` 在解析前展开，回写会固化明文并丢注释）。同名条目由覆盖层完全覆盖，config.yaml 新增的提供商仍可见（`source=config`），界面删除写「删除坠碑」，未改动过的条目不落盘。保存与切换后由 `Agent.SetModel` 热生效，一轮请求全程使用入口处取的同一份快照。

新增 API 模式需在 `internal/llm/factory.go` 的 `KnownTypes` 注册并实现 `Provider` 接口。Anthropic 侧只支持当前世代模型的参数形式（`thinking:{type:"adaptive"}` + `output_config.effort`，不发 `temperature`），带签名的思考块通过 `llm.Message.ReasoningBlocks` 原样回传，无签名的一律丢弃。

## Git 工作流约定

1. **功能开发一律先建分支**（`feature/<功能名>`），实现并验证通过后合并回 `main`，不直接在 `main` 上开发。
2. **敏感与本地文件不入库**：`config.yaml`（本地配置，含 API Key）、`sessions/`（会话数据）、`*.log`、构建产物均已在 `.gitignore` 中，新增此类文件时同步维护忽略列表。
3. 提交信息用中文，说明"做了什么、为什么"。

## 常用命令

```bash
go build -o wen.exe ./cmd/wen   # 构建
go test ./...                   # 测试
go run ./cmd/wen                # 运行（读取 ./config.yaml，密钥也在其中）
```
