# 后端开发准则

> `internal/` 与 `sdk/` 中 Go 服务器实现的约定。

---

## 准则索引

| 准则 | 说明 | 状态 |
|------|------|------|
| [目录结构](./directory-structure.md) | 模块布局、executor/helps 拆分、thinking 管道、translator 规则 | 已启用 |
| [存储后端](./database-guidelines.md) | 文件/Postgres/Git/对象/Bolt 后端、密钥哈希契约 | 已启用 |
| [错误处理](./error-handling.md) | 禁用 `log.Fatal`、`clienterror`、defer close、handler 禁止 panic | 已启用 |
| [质量准则](./quality-guidelines.md) | `ojson`/`base64` 规则、payload 屏障、超时例外、gofmt | 已启用 |
| [日志准则](./logging-guidelines.md) | logrus 结构化日志、禁泄漏密钥、日志级别 | 已启用 |

---

## 横切硬规则（速览）

完整内容见各链接准则，这些最容易被违反：

- 所有 JSON/Base64 用 `ojson`（绝不用 `tidwall/gjson`/`sjson`）与 `moego/base64`（绝不用标准库 `encoding/base64`）。
- `*ojson.Doc` 全程传递，仅在最终出口序列化一次。
- payload 配置是发送前的最终语义屏障；用户 payload 规则仅应用一次，之后不得覆盖。
- 上游连接建立后不得设置超时（仅 Codex websocket 存活、wsrelay、管理 API、fetch 工具为例外）。
- 启动外禁用 `log.Fatal`；客户端错误用 `internal/clienterror` 分类。
- helper 放在 `internal/runtime/executor/helps/`，不要放 `executor/`。

**语言**：文档与代码注释仅用英文。
