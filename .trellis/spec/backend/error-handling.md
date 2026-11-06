# 错误处理

> 失败如何被表示、记录、上报与测试。

---

## 核心规则

### 库代码 / handler 中禁用 `log.Fatal`

`log.Fatal`/`log.Fatalf` 会终止进程。在 executor、handler、store 或任何启动后可能运行的路径中不要使用。应返回 error 并用 logrus 记录。

证据：`internal/` 中 `log.Fatal` 出现 0 次。仅 `cmd/server` 的启动代码可用；其余路径都必须返回 error。

### 面向客户端的错误分类使用 `internal/clienterror`

`internal/clienterror` 是"错误映射到 HTTP 状态"与"检测 provider 特定故障类"的唯一事实来源。示例（`client_error.go`）：

- `HTTPStatusFromError(err) int` / `HTTPStatusFromErrorOr(err, fallback) int`
- `IsRequestFault(status, err) bool`
- `IsClientCancellation(status, err) bool`
- provider 故障检测器：`IsGeminiFilterBlockError`、`IsClaudePolicyRefusalError`、`IsClaudeThreadNotFound`、`IsDevinRequestPermissionDenied`、`IsCreditBalanceTooLow`、`IsModelNotFound*`、`IsAuthenticationError*`。

在这些地方复用它们，而不是在各调用点重新推导状态码或匹配原始子串。新增 provider 故障类时，在这里加检测器，而不是在每个调用点加。

### 用 context 包装 error

在能补充语义的边界处（哪个 provider、哪个 model、哪一步）包装 error。使用 `fmt.Errorf("...: %w", err)`。当 error 携带调用方或 logger 需要的信息时，不要用裸 `_` 吞掉。

### 处理 deferred close 的 error

不要丢弃 deferred 的 `Close`/`Flush` 调用返回的 error。应包起来：

```go
defer func() {
    if err := f.Close(); err != nil {
        log.Errorf("close failed: %v", err)
    }
}()
```

证据：该模式出现在 `internal/api/handlers/management/api_tools.go` 与 `auth_files_*.go`。裸 `defer f.Close()` 会静默丢失失败。

### HTTP handler 中避免 panic

handler 必须返回有意义的 HTTP 状态并记录 error，绝不能 panic。请求路径中的 panic 会使 worker 崩溃且无法返回干净的状态。

### 日志中不得泄漏密钥 / token

绝不在 debug 级别或传入 `log` 的错误消息中记录完整凭证、OAuth token 或 `Authorization` 头。记录前先脱敏。

## 错误流向（进入与出口）

1. 上游响应 / 网络错误到达 executor（`internal/runtime/executor/*_executor.go`）。
2. executor 经 `internal/clienterror` 分类并返回类型化 error。
3. API handler 通过 `HTTPStatusFromError*` 将其映射为 HTTP 状态，并写出对客户安全的响应体（不含内部密钥）。
4. 流路径用同样的分类把 error 翻译为终止型 SSE / 流事件。

## 测试错误行为

- 在同级目录加单元测试，断言新错误类的状态码与故障检测器结果。
- 上游故障回放：executor 保留内嵌响应 fixture 的 `*_test.go`（见 `codex_executor_signature_test.go`、`gemini_executor_terminal_disconnect_test.go`）。
- 优先断言 `clienterror` 检测器，而非对原始 body 做字符串匹配。

## 反模式

- 用 `log.Fatalf` 退出请求路径。
- 在每个调用点匹配 `"401"` / `"auth"` 子串，而不是用 `clienterror.IsAuthenticationError*`。
- 记录 `req.Header.Get("Authorization")` 或 token 字段。
- 裸 `defer f.Close()` 不处理 error。
