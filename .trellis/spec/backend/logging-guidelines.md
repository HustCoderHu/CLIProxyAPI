# 日志准则

> CLIProxyAPI 的结构化日志与日志级别。

---

## 使用 logrus 结构化日志

项目使用 `github.com/sirupsen/logrus`（别名 `log`）。使用结构化字段，而非字符串拼接：

```go
log.WithFields(log.Fields{
    "provider": provider,
    "model":    modelInfo.ID,
}).Errorf("upstream request failed: %v", err)
```

运行时日志不要用标准库 `log` 包。（启动外的标准 `log.Fatal` 禁止——见错误处理。）

## 日志级别

- `Debug` — 冗长的每请求状态、payload 摘要、分支决策。任何大块内容都用 `Debug` 兜住，使生产环境保持安静。
- `Info` — 生命周期事件：服务启动、配置重载、模型注册表更新。
- `Warn` — 可恢复问题：触发限流、回退到下一个凭证、触发重建的缓存未命中。
- `Error` — 导致向客户返回非 2xx 或中止请求路径的操作失败。

## 绝不泄漏密钥

`Authorization` 头、OAuth token、API key、会话凭证绝不得出现在日志输出中。记录前脱敏——记录稳定标识符（如 `credentialID`、`provider`），而非 token 值。

证据：`internal/clienterror` 与 executor 代码在不回显 token 材料的前提下完成故障分类；没有记录 `Authorization` 的模式。

## 避免热路径日志噪声

流 executor 会发出大量事件。把每 chunk 的日志保持在 `Debug`，不要在 `Info` 记录完整请求/响应体。使用已有的 helper `internal/runtime/executor/helps/logging_helpers.go` 做一致且脱敏的流日志，而不是临时 `log.Printf`。

## 关联字段

在 executor 中记录日志时，包含足够上下文以跨阶段追踪单个客户请求：`provider`、`model`、`session`（若有）、可用的请求标识符。一致性使 `server-test.log` 可被 grep。

## 反模式

- `log.Printf("token=%s", token)` — 泄漏密钥。
- 在 `Info` 记录完整上游 payload。
- 同一调用路径混用标准库 `log` 与 logrus。
- 对预期的、已处理的回退用 `Error`（应用 `Warn`）。
