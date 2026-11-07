# SSE 信封解包: 可复用模式与 executor 骨架

来源: 只读走查, 2026-11-06. 5 个 agent 中唯一确认 "仓库内无同类实现" 的一项.

## 一、结论

**CLIProxyAPI 里没有任何 executor 解包过 SSE `data:` 里的嵌套 JSON 字符串.**
全仓库零 `statusCodeValue` 命中; 唯一的 `"body"` 字符串解码在 WebSocket relay
(`internal/wsrelay/http.go:188,219`) 与 Codex WebSocket 错误路径
(`codex_websockets_errors.go:90`), 都不是 SSE 帧循环.

⇒ **Qoder 必须自建帧循环**.

## 二、四个候选的实际匹配度

| 候选 | 帧结构 | 嵌套 body 字符串 | 信封内 status | 非 data 行 | 内层 usage |
|---|---|---|---|---|---|
| Devin | Connect/Protobuf 二进制 | 否 (protobuf 字段) | 类比 (trailer) | N/A | 是 |
| Kimi | 纯 OpenAI SSE | 否 | 否 | 否 | 是 |
| Antigravity | 嵌套**对象** `{"response":{...}}` | 否 (对象, 对 JSON path 透明) | **是** (`error.code`) | **是** | 是 |
| AI Studio | relay 不透明流 | 否 | **是** (`event.Status`) | **是** | 是 |

**最接近的是 Antigravity** (`antigravity_executor_stream.go:214-449`):
- status 提取: `ojson.GetBytesNoCopy(payload, "error").Get("code")` (`:300-313`),
  强制到 400..599 或 502, 经 `newAntigravityStatusErr` 抛出
- 非 data 行: `helps.JSONPayload` (`usage_helpers.go:4132-4154`) 对 `event:`/`[DONE]`/非 `{` 返回 nil,
  循环 `continue` (`:297-299`)
- usage: `streamUsage.ObserveAntigravityStream(line)` (`:276`)

Qoder 需要的只是把 `error.code` 换成 `statusCodeValue`, 把对象换成字符串 — 骨架可照抄.

**AI Studio** (`aistudio_executor.go:321-491`) 是"不透明流需重新分帧"的最佳传输层类比:
`wsrelay.MessageTypeHTTPResp` 分支 (`:427-465`) 检查 `event.Status` (`:437-446`);
`helps.RelaySSEBuffer` + `splitRelayFrames` (`:342-366`, helper `:674-694`).

## 三、可复用的 helps

### 帧扫描
- `helps.StreamScannerBuffer` (`helps/gemini_interactions.go:21`) = 52,428,800. 用常量, 别写字面量
- `helps.JSONPayload(line []byte) []byte` (`usage_helpers.go:4132`) — 剥 `data:`, 丢弃 `event:`/`[DONE]`/非对象
- `helps.RelaySSEBuffer` / `Feed` / `Finish` (`relay_sse_buffer.go:7,15,61`) — 跨任意 chunk 边界重组帧
- `helps.GeminiInteractionsSSEPayload` / `SSEDone` (`gemini_interactions.go:224,255`) — 唯一已理解 `event:` 语义的 helper

**不要**用 `helps.CodexSSEScanner` (`codex_sse_scanner.go:79`): 它的职责是多行 JSON 片段拼接
(`hold`/`release` `:157-192`), 与信封解包正交, 只会增加复杂度.

### usage 缓冲
- `helps.StreamUsageBuffer` (`usage_helpers.go:922`)
- `.ObserveOpenAIStream(line)` (`:1187`) — **喂内层 chunk**, 这是 Qoder 要的
- `.Observe(detail, ok)` (`:995`), `.Publish(ctx, reporter)` (`:1469`), `.Detail()` (`:1493`)

### usage 上报
- `helps.NewExecutorUsageReporter(ctx, executor, model, auth)` (`:82`)
- `.TrackFailure(ctx, &err)` (`:556`), `.Publish` (`:340`), `.ObserveResponseModel` (`:219`)

### 非流式聚合
- `helps.AggregateOpenAIStreamChunks(raw []byte) []byte` (`stream_aggregator.go:17`) —
  接收换行分隔的裸 JSON chunk (无 `data:`, 容忍 `[DONE]` `:35-38`), 保留最后一个 `usage` (`:60-62`),
  产出 `chat.completion`. **Qoder 的 Execute 直接用它**
- 用法先例: `codebuddycn_executor.go:442-444`

### payload 屏障
- `helps.ApplyPayloadConfigWithRequestDoc` (`payload_helpers.go:39`)
- 顺序必须照抄 CodeBuddy: transforms -> `ApplyPayloadConfigWithRequestDoc` -> `doc.Bytes()`
  (`openai_compat_executor.go:447-453`)

### 错误分类
- `statusErr` (`openai_compat_executor.go:1330-1360`) — 同包 `executor`, Qoder 可直接构造.
  字段: `code`, `msg`, `retryAfter`, `credentialScoped`, `streamTruncated`,
  `transientRateLimit`, `diagnostic`
- `newOpenAICompatStatusError(status, headers, body)` (`:1364`)
- **`openAICompatStreamDataError` (`:1298-1328`) 不认识 `statusCodeValue`** — Qoder 要自己判
- `internal/clienterror.HTTPStatusFromError` / `IsRequestFault` (`client_error.go:40,77`)

### 流翻译
- `helps.TranslateStreamWithClaudeInputTokens(...)` (`claude_input_tokens.go:48`)
- `helps.NewClaudeInputTokenState` (`:35`)
- `helps.InitializeApplyPatchStream` / `EndApplyPatchStream` / `RecordApplyPatchStreamFailure`

### 日志
- `RecordAPIRequest` / `RecordAPIResponseMetadata` / `RecordAPIResponseError` /
  `AppendAPIResponseChunk` / `SummarizeErrorBody` (`logging_helpers.go:65,195,224,249,701`)

### HTTP
- `helps.NewProxyAwareHTTPClient` (`proxy_helpers.go:36`)
- `sdk/httptransport.Decorate` (`sdk/httptransport/decorator.go:56`) — executor 必须应用以保留可观测性

## 四、为什么不能继承 OpenAICompatExecutor 改帧

`OpenAICompatExecutor` 只有两个注入点 (`openai_compat_executor.go:51-66`):

| 字段 | 位置 | 作用域 |
|---|---|---|
| `httpClientFactory` | `:55-57`, 用於 `:82-85,118,241,342,489,785` | **仅请求侧** |
| `outgoingTransforms` | `:59-65`, 派发 `:1188-1193` | **仅请求侧** |

**没有响应钩子.** 流循环 `executeStreamWithCredentials` (`:387-722`) 是私有方法,
帧状态 (`upstreamEvent`, `frameData`, `seenDone`, `sawFinishReason`, `streamUsage`)
是 goroutine 内**局部变量** (`:520-532`), `processFrame` 是**局部闭包** (`:550-616`).
无字段、无接口、无回调可覆盖. 代码本身也写明了 (`:62-64`):
"Because Go does not dispatch methods virtually through embedding..."

`CodeBuddyCNExecutor.ExecuteStream` (`codebuddycn_executor.go:110-113`) 直接调
`e.OpenAICompatExecutor.ExecuteStream`, 绕过任何外层方法 — 印证了这点.

### 硬塞会发生什么

若强行走 compat 循环, 信封在 `processFrame` 之前就被破坏:

| 位置 | 行为 | 后果 |
|---|---|---|
| `:577` | `!json.Valid(dataPayload)` | 信封本身是合法 JSON, 通过 |
| `:582` | `openAICompatStreamDataError` | 找不到 `error` (status 在 `statusCodeValue`), 漏判 |
| `:586` | `openAICompatChunkHasFinishReason` | 没有 `choices`, 漏判 |
| `:592-594` | `streamUsage.ObserveOpenAIStream(envelope)` | usage 解析失败 |
| `:596-597` | 送 translator | `to=openai -> target=openai` 直通, **客户端收到原始信封** |
| `:571-572` | `[DONE]` 比较 | `{"body":"[DONE]"}` ≠ `[DONE]`, 永远不匹配 |
| `:654-656, 688-704` | 于是报 "closed before [DONE]" | 流提前报错 |

### 唯一可行的包装方式 (OpenCode 先例)

`OpenCodeExecutor.ExecuteStream` (`opencode_executor.go:118-144`) 包装的是**chunk 通道**:

```go
outChunks := make(chan cliproxyexecutor.StreamChunk, 16)
for chunk := range inChunks {
    chunk.Payload = helps.NormalizeOpenCodeToolCallChunk(chunk.Payload)
}
```

这能成立仅因为 compat 循环**已经**在 `:596-606` 产出解码后的 OpenAI chunk JSON.
Qoder 的信封在这之前就被处理坏了, 所以这条路不通.

## 五、最小可行的 Qoder executor

### 接口必须实现的 6 个方法

`ProviderExecutor` (`sdk/cliproxy/auth/conductor.go:18-33`) 全部是接口方法, 没有可选的:

| 方法 | 最省实现 | 先例 |
|---|---|---|
| `Identifier()` | 一行返回常量 | `codebuddycn_executor.go:59` |
| `Execute` | **强制 `opts.Stream = true`, 消费 ExecuteStream, 聚合** | `codebuddycn_executor.go:75-107`; 聚合用 `helps.AggregateOpenAIStreamChunks` (`:442-444`) |
| `ExecuteStream` | **必须自建** — 帧扫描 + 信封解包 | 骨架照抄 `antigravity_executor_stream.go:214-449` |
| `Refresh` | 实现 token 轮换 (Qoder 按前缀分流) | `kimi_executor.go:1122-1137` (无 token 时直接返回) |
| `CountTokens` | **嵌入 compat 实例即免费继承** | `codebuddycn_executor.go` / `opencode_executor.go` 都**没有**定义, 靠嵌入继承 `openai_compat_executor.go:916-964` |
| `HttpRequest` | 12 行委托 | `devin_executor.go:126-139`; `opencode_executor.go:164-177` |

### 值得顺带实现的

- `UsesConfig(cfg)` — 配置热重载必需 (`service_executors.go:465`). 嵌入不会派发, 必须手写
  (`codebuddycn_executor.go:64-69`)
- `ForAPIKey()` — `oauth_scope_executor.go:76-87`
- `SupportsApplyPatch()` — `meta_executor.go:410`
- `PrepareRequest` — 仅在需要 `InjectCredentials` 时
- `RequestToFormat` — Qoder 上游是 openai 格式, **不需要** (见 QODER_RESEARCH_TODO D6)

### 无法回避的新代码 (仓库内零等价物)

1. **信封结构 + status 闸门** — `statusCodeValue` 全仓零命中. 用 ojson 读取,
   照 `antigravity_executor_stream.go:300-313` 的形状:
   ```go
   status := int(ojson.GetBytesNoCopy(dataPayload, "statusCodeValue").Int())
   body := ojson.GetBytesNoCopy(dataPayload, "body").String()
   ```
   `Result.String()` 语义已确认 (`moego@v0.1.2/ojson/result.go:79-96`):
   `Type==String` 时返回**解码后的内层值**, 正是所需.
2. **嵌套 `[DONE]` 检测** — 必须在信封**内部**判 `body == "[DONE]"`,
   早于 `openai_compat_executor.go:571-572` 的顶层判定.
3. **空 delta 清理 (`clean_chunk`)** — 无等价物.
   `NOISE_KEYS = ("extra_fields", "refusal", "reasoning_content")` (`qoder_proxy.py:344`),
   逻辑 `:3402-3436`. 最接近的仓库惯用法是 `helps.NormalizeOpenCodeToolCallChunkDoc`
   (`opencode_tools.go:251`) — 基于 `ojson.Doc` 就地裁剪, 返回 `bool changed`, 未变不重序列化.
4. **忽略 `event: finish{...}`** — **不要**复用 `openAICompatErrorEvent` (`:1285-1287`),
   它只认 `error`/`response.error`/`response.failed`, `finish` 不是错误.
   Meta 的先例 (`meta_executor_stream.go:128-180`) 展示如何干净地跳过 `event:` 行对.

### ExecuteStream 骨架 (约 150 行)

1. `ctx = helps.EnsureSessionContext(...)`;
   `reporter := helps.NewExecutorUsageReporter(...)`; `defer reporter.TrackFailure(ctx, &err)`
2. 构造请求: translate -> `ojson.NewDoc` -> outgoing transforms ->
   **`ApplyPayloadConfigWithRequestDoc`** -> `doc.Bytes()`
3. `scanner.Buffer(nil, helps.StreamScannerBuffer)`; 累积 `data:` 行成帧; 空行时 `processFrame`
4. `processFrame`: `JSONPayload` 风格守卫 -> 解信封 -> status 闸门 ->
   内层 `body == "[DONE]"` 则停 -> `streamUsage.ObserveOpenAIStream(inner)` ->
   `cleanChunk(inner)` -> `TranslateStreamWithClaudeInputTokens` -> 发出
5. EOF 未见终止帧: `statusErr{code: 502, streamTruncated: true}`
   (参照 `openai_compat_executor.go:692`)

## 六、对任务前提的一处修正

`devin_executor.go` 2902 行与 `kimi_executor.go` 1553 行都不是合适的模板.
**`codebuddycn_executor.go` (444 行) 才是合适的体量模板** — 它的
"Execute 强制流式 + 聚合"设计 (`:75-107`) 正是 Qoder 非流式路径所需.
但它的流式部分 100% 委托给 compat 循环 (`:110-113`), **不含任何信封处理代码**.

分工:
- 流式**形状**来自 Antigravity
- **生命周期**来自 CodeBuddy / OpenCode
- **信封解码**是全新代码, 约 30 行
