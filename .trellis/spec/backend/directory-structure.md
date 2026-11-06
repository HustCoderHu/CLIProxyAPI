# 目录结构

> CLIProxyAPI 的模块组织与文件布局。

---

## 概览

CLIProxyAPI 是一个 Go 代理服务器，对外提供 OpenAI/Gemini/Claude/Codex 兼容 API，支持 OAuth 与轮询负载均衡。仓库是一个单一 Go module（`go.mod`：`github.com/router-for-me/CLIProxyAPI/v8`），包含两棵平行的树：

- `internal/` — 服务器实现（代码主体）
- `sdk/` — 可嵌入 SDK 入口（`sdk/cliproxy`），把 `internal` 的部分能力以公开接口暴露

新代码应放在已经拥有该职责的目录下。无充分理由不要新建顶层 package。

## 顶层布局

| 路径 | 职责 |
|------|------|
| `cmd/server` | 服务器入口（`main`）。保持精简：装配配置、启动服务。 |
| `cmd/fetch_*` | 一次性工具，拉取远程模型列表。可使用 timeout。 |
| `internal/api` | Gin HTTP API：路由、中间件、handlers、`modules/amp`。 |
| `internal/thinking` | "规范表示 → 各 provider 翻译" 的 thinking 管道。 |
| `internal/runtime/executor` | 各 provider 的运行时 executor（含 Codex WebSocket）。 |
| `internal/translator` | provider 协议翻译器（及共享 `common`）。 |
| `internal/registry` | 模型注册表与远程更新器（`StartModelsUpdater`）。 |
| `internal/store` | 存储实现与密钥解析。 |
| `internal/auth/*` | 每个 provider 一个子包，负责 OAuth / 凭证获取。 |
| `internal/watcher` | 配置热重载与 watcher。 |
| `internal/wsrelay` | WebSocket 中继会话。 |
| `internal/usage` | 用量与 token 记账。 |
| `internal/home` | CLIProxyAPIHome 控制面集成。 |
| `internal/tui` | Bubbletea 终端 UI。 |
| `internal/cache` | 请求签名缓存。 |
| `internal/config`、`internal/constant`、`internal/util`、`internal/logging` | 横切支撑。 |
| `sdk/cliproxy` | 可嵌入 SDK 入口：`service`、`builder`、`watchers`、`pipeline`。 |
| `test/` | 跨模块集成测试（与单元测试分开存放）。 |

## Executor 布局规则（重要）

`internal/runtime/executor/` 只能放 **executor 及其单元测试**。任何 helper / 支撑文件都放到 `internal/runtime/executor/helps/`。

证据：目录已做隔离——`*_executor.go` 与 `*_test.go` 并列，其余一切（请求构造、工具 schema、配额、压缩、代理池、用量 helper、payload 收尾）都在 `helps/`。新增可复用 helper 时，放到 `helps/` 并在那里加 `*_test.go`，不要因为正在编辑某个 executor 就把共享逻辑内联进去。

## Thinking 管道（不可破坏）

`internal/thinking/` 强制一套架构：

1. `apply.go` — `ApplyThinking()` 解析后缀（`suffix.go`，后缀覆盖 body），将配置规范化到规范 `ThinkingConfig`（`types.go`）。
2. `validate.go` / `convert.go` — 集中规范化与校验。
3. `ProviderApplier`（`provider/`）— 最后应用的 provider 特定输出。

规范表示是 `ThinkingConfig`（`types.go:73`）。每个 provider 实现 `ProviderApplier`，含两个方法：`Apply([]byte, ThinkingConfig, *registry.ModelInfo)` 与 `ApplyDoc(*ojson.Doc, ThinkingConfig, *registry.ModelInfo)`（`types.go:121-128`）。新增 provider 行为应注册新的 `ProviderApplier`，绝不要在 `apply.go` 里加特例。

## Translator 规则

**不要**单独改动 `internal/translator/`，除非它是别处更大改动的一部分。如果任务只要求改 `internal/translator/`，先确认远程写权限（`gh repo view --json viewerPermission`）；如果没有权限，提交 GitHub issue 并停止。共享翻译原语放在 `common` 子包，不要复制到每个 provider 子目录。

## 命名约定

- Executor：`<provider>_executor.go`，按职责拆文件（`*_request.go`、`*_stream.go`、`*_auth.go`、`*_execute.go`）。
- 测试：与待测源码同目录并列 `<same_name>_test.go`。
- 测试数据：`testdata/` 与 `static/` 目录放在对应测试旁边。

## 禁止事项

- 不要把共享 helper 直接放进 `executor/`，应使用 `helps/`。
- 不要为单个文件新建顶层 package。
- 不要在 thinking 规范路径（`apply.go`）里为 provider 加特例；应注册 `ProviderApplier`。
