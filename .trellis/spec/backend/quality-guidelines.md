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

### 例外：字节级签名载荷（已批准）

当载荷的**字节本身**进入加密签名（而非仅作为 JSON 被解析），且需要与外部参考实现
逐字节一致时，允许手写序列化，不走 `ojson`。判定条件（全部满足才可用）：

1. 输出字节参与 MD5/SHA/HMAC 等签名计算，任何转义差异都会导致签名错误；
2. 目标转义集与 `ojson` 的行为不同，且无法通过参数修正。

已验证的三方行为差异（2026-11）：

| 实现 | `<` `>` `&` | U+2028 / U+2029 |
|---|---|---|
| Python `json.dumps(ensure_ascii=False)` | 原样 | 原样 |
| `ojson.AppendJSONString` | 转义为 `<` 等 | 转义 |
| `ojson.Doc.Set` | 原样 | 转义 |

注意 `ojson.DisableEscapeHTML` 是**包级全局变量**，从并发请求路径翻转它是数据竞争，
不能作为按调用点的开关。

现有实例：`internal/runtime/executor/helps/qoder_cosy.go` 的
`qoderJSONSortedCompact` / `qoderAppendJSONString`（Qoder COSY 签名输入）。
手写实现必须在注释里写明为何不能用 `ojson`，并有对外部参考实现的字节级回归测试。

### 例外：可重现的 RSA 公钥运算

`crypto/rsa.EncryptPKCS1v15` 内部调用 `randutil.MaybeReadByte`，有 50% 概率多消耗
1 字节随机流，因此**注入固定 Reader 也无法得到可复现的密文**。当测试需要锁定密文
黄金向量时，允许手写 PKCS#1 v1.5 公钥运算（`EM = 0x00 || 0x02 || PS || 0x00 || M`
后做模幂）。公钥运算不涉及私钥，无侧信道顾虑。已有测试覆盖模幂结果与参考实现一致。

## 判断"既有失败"不能只 stash 当前阶段的文件

当 `go test ./...` 出现失败并试图判定是否为既有问题时，`git stash` **只移除你刚写的
文件**。多阶段任务里，前几阶段已经落盘的文件仍然存在，它们的失败会被误判成"既有"。

已验证的实例（2026-11，Qoder）：P3 阶段把 `internal/util` 的
`TestInPlaceByteWritesAreReviewed` 报告为既有失败，理由是 stash 自己新增的文件后仍失败。
但该测试同时列出了 `internal/kvstore/bolt_store.go`（确实既有）和
`internal/runtime/executor/helps/qoder_cosy.go`（P0 新增，**不是既有**）。
真正既有的是前者，被误判的是后者。

正确做法：

1. 读完整的失败输出，逐个文件名核对，不要看"是否失败"这一个布尔值。
2. 对每个被点名的文件，用 `git log --oneline -1 -- <file>` 和
   `git status --short <file>` 判断它是否属于本次任务。
3. 本次任务新增的文件产生的失败，必须修复或登记，不能归入"既有"。

## 外部协议常量：必须来自证据，不得推导

新增 provider 时，端点路径、协议版本号、公钥、字母表、头名等**外部协议常量**必须
逐字复制自可核查的来源（抓包、参考实现常量、官方客户端产物），并在注释里写明来源位置。

禁止的推导方式：

- 按"其他 provider 的路径形状"推测（例：把 `/v2/chat/completions` 当成推理端点，
  实际是 `/algo/api/v2/service/pro/sse/agent_chat_generation`）
- 按字段语义拼装 query（查询参数的名称、顺序、是否存在都必须来自来源）
- 按同类 provider 的版本号/UA 猜测

实例（2026-11，Qoder）：设计文档与派发指令中的推理端点
`/algo/api/v2/chat/completions` 是按结构推测出来的，真实值是参考实现里的
`/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1`。
实现阶段靠核对参考实现常量才发现。签名只覆盖 path，所以路径错会让请求打到不存在的端点，
而**本地测试不会暴露**（mock 上游接受任意路径）。

对应防错手段：黄金向量/端到端测试断言**完整 URL**，而不只是 header 与 body。

### 黄金向量的 URL 必须取自真实端点

黄金向量里的 URL 不是装饰，它决定签名覆盖的 path。如果 fixture 用"按形状推测"的路径，
测试会永久通过，而真实请求打到不存在的端点。

实例（2026-11，Qoder）：`qoder_cosy_test.go` 早期的签名向量用
`/algo/api/v2/chat/completions`，这是设计文档推导出来的假端点。真实端点是
`/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1`
与 `/algo/api/v2/model/list?Encode=1`。因为 mock 上游接受任意路径，这个错误在端到端测试里
同样不会暴露。补上的防错用例是 `TestQoderCosySignatureVectorRealPaths`：对**每个真实端点**
各锁一组参考实现的 md5。

规则：

1. 黄金向量的输入 URL 必须逐字复制自常量表里的真实端点（含 query 参数顺序）。
2. 每个真实端点至少一组向量，不要只测"一个代表性的 path"。
3. 若保留推导出来的形状 fixture，注释必须写明它不是真实端点、只验证签名形状。

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
