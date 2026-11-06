# 质量准则

> CLIProxyAPI 的代码标准与禁止模式。

---

## 禁止的 import（硬性规则）

### JSON：用 `ojson`，绝不用 `tidwall/gjson`/`sjson`

所有 JSON 读写操作**必须**使用 `github.com/MoeForge/moego/ojson`。不要 import
`github.com/tidwall/gjson` 或 `github.com/tidwall/sjson`。

证据：647 个文件使用 `ojson`；`tidwall/gjson`/`sjson` 在源码中 0 处出现。

### Base64：用 `MoeForge/moego/base64`，绝不用标准库 `encoding/base64`

所有 Base64 操作使用 `github.com/MoeForge/moego/base64`。不要 import 标准库
`encoding/base64`（最近一次检查时树中恰好有一个游离的 `encoding/base64`
import——任何出现都应视为要删除的 bug）。

## JSON 变更顺序（关键）

把 `*ojson.Doc` 当作贯穿所有变更阶段的"工作表示"：

1. 用 `ojson.NewDoc()` / `ojson.NewDocString(payload)` 创建。
2. 把 `*ojson.Doc` 传入每个变更阶段（`Set`、`SetRaw`、`Delete`、`Get`、`Merge`...）。
3. 仅在最终出口**序列化一次**：`doc.Bytes()` / `doc.String()`。

不要在各阶段之间直接改原始 `[]byte` 或 `string` 负载，也不要序列化→改字符串→再解析。证据：`internal/runtime/executor/helps/payload_finalizer.go:45` 在最终序列化前对 `*ojson.Doc` 调用 `f(doc.Bytes())`。

## Payload 配置屏障（关键）

在每个 executor 中，payload 是发送请求前的**最终语义屏障**。这适用于流、WebSocket、续传、重试/回退、图像与 token 计数等所有路径。

顺序：

1. 先完成所有内置的 payload 翻译、规范化、注入与清理。
2. 然后**仅一次**对最终业务 payload 评估并应用用户 payload 规则。
3. 之后的任何逻辑都不得覆盖已配置的值或恢复被过滤的字段。
4. 只有必要的传输封装、签名计算与只读校验可以随后进行——且都不得改变业务 payload 语义。

回归测试强制此顺序：`internal/runtime/executor/payload_barrier_test.go`、
`payload_review_test.go`。引入或修改任何请求构造路径时，在 `payload_barrier_test.go` 下加新用例。

## 超时（关键，含例外）

上游连接建立**之后**，不得为任何后续网络行为设置超时。超时**仅**允许在凭证获取阶段使用。

明确允许的超时例外（不要"修复"掉它们）：

- Codex websocket 存活 deadline——`codex_websockets_executor.go` / `codex_websockets_session.go:794`（`SetReadDeadline` 配 `codexResponsesWebsocketIdleTimeout`）/ `codex_websockets_connection.go:206`。
- wsrelay 会话 deadline——`internal/wsrelay/session.go`。
- 管理 APICall 超时——`internal/api/handlers/management/api_tools.go`。
- `cmd/fetch_antigravity_models` 工具超时（一次性拉取工具）。

## 格式化

- 每次改 Go 代码后运行 `gofmt -w .`（必须）。未格式化会导致 CI/提交失败。
- 保持 goimports 风格（stdlib、第三方、再本地 `github.com/router-for-me/CLIProxyAPI/v8/...`）。
- 使用 Tab 缩进。`Edit` 的 PreToolUse hook 会检测并纠正缩进不匹配——用 `Edit` 做定向修改，不要用 `sed`/`awk`/`python -c` 重写文件内容。

## 变量遮蔽

当遮蔽会覆盖外层变量（特别是 `err`）时，改用方法后缀重命名：`errStart := server.Start()`、`errInit`、`errOpen`。证据：该模式在 47 个文件中出现。它让外层变量在 deferred/清理路径中仍可用，避免意外复用。

## 注释

- 注释仅用英文。编辑含非英文注释的代码时，翻译成英文；不要新增非英文注释。
- 用户可见字符串，保持该文件/区域已有的语言。

## 报告完成前的构建 / 验证

- `go build -o cli-proxy-api ./cmd/server` — 改完后必须跑。
- `go test ./...` — 跑全部；单测用 `go test -v -run TestName ./path`。
- `gofmt -l .` — 不得报告任何文件。

## 反模式

- payload 用 `import "encoding/json"` — 用 `ojson`。
- 序列化→改字符串→再解析 payload。
- 应用用户 payload 规则后，又让后续代码覆盖它们。
- 为连接后的流加 `http.Client{Timeout: ...}`。
- `err = f(); ...` 遮蔽 deferred close 需要的外层 `err`。
