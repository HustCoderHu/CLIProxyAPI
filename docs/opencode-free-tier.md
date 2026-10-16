# OpenCode 免费层接入

CLIProxyAPI 可以通过标准 OpenAI 兼容的 Chat Completions 端点代理 OpenCode 的免费层模型。
本文档说明此前导致无法接入的网关契约、该契约如何被满足，以及涉及的各组件。

## 1. 问题背景

把 OpenCode 配置成一个普通的 `openai-compatibility` provider 时，每个请求都会被上游立即拒绝：

```json
{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode. Download it at https://opencode.ai and keep building."}}
```

该网关并不校验账户层级或额度，它校验的是**请求身份**：请求必须看起来是由官方 OpenCode CLI 发出的。
配置一个有效的 API key 对此毫无帮助。

## 2. 网关契约

免费层网关同时要求以下各项。缺失任何一项都会返回同样的 403，
这也正是该故障难以定位的原因。

| 要求 | 说明 |
| --- | --- |
| `User-Agent` | 官方 CLI 正式发布格式，例如 `opencode/latest/2.0.29/cli`。版本号需 >= 1.17.0，且必须是真实 release，而非开发构建版。 |
| `x-opencode-client` | 期望值为 `cli`。 |
| `Authorization` | 匿名免费层要求大小写敏感的字面量 `Bearer public`。下游客户端透传的占位 key 必须被重置。 |
| `x-opencode-project` | 裸的 40 位小写十六进制字符串，不带 `prj_` 等前缀。 |
| `x-opencode-request` | **禁止携带**。只要存在即触发 403。 |
| `x-opencode-session` / `x-opencode-session-id` | 两者必须同时存在且取值完全一致。 |
| session ID 格式 | `ses_` + 12 位小写十六进制 + 14 位 base62 字符（总长 30）。十六进制部分编码了一个真实的近期时间戳。 |
| 核心工具 schema | 请求体的 `tools` 数组必须包含 OpenCode 的五大内置工具，且 `name`、`description`、`parameters` 完全一致。空桩会被拒绝。 |

### 2.1 为什么时间戳无法伪造

session ID 的十六进制段不是一个不透明令牌，服务端会将其反解回时间戳以校验合理性。
任何确定性替代物（例如 SHA-256 摘要）产生的时间戳要么超出范围，要么在时间上恒定不变，
都会被拒绝。唯一可行的方式是运行真实算法并使用真实时钟。

## 3. 架构设计

```
              Claude Code 客户端（工具：Bash, Read, Edit, ...）
                                |
                                v
                sdk/cliproxy 路由 / provider 调度
                                |
                                v
+--------------------------------------------------------------------------+
| OpenCodeExecutor（内嵌 *OpenAICompatExecutor）                            |
|                                                                          |
|  prepareOpenCodeAuth                                                     |
|    - User-Agent: opencode/latest/2.0.29/cli                              |
|    - x-opencode-client: cli                                              |
|    - x-opencode-project: <40-hex>                                        |
|    - x-opencode-session / -session-id / x-session-affinity / X-Session-Id|
|      全部设为规范化的 descending 时间戳 session ID                       |
|    - 免费模型的 Authorization 归一化为 "public"                           |
|    - 剥离 x-opencode-request                                             |
|                                                                          |
|  outgoingTransforms（在 helps.ApplyPayloadConfigWithRequest 之前执行）   |
|    - 强制 stream: true                                                   |
|    - 设置 stream_options.include_usage: true                             |
|    - 合并 OpenCode 五大核心工具 schema，同时保留调用方原有工具           |
|                                                                          |
|  Execute（非流式客户端）                                                 |
|    - 强制上游流式，随后在本地聚合 SSE 帧                                  |
|    - 将上游工具调用归一化回 Claude Code 约定                              |
|                                                                          |
|  ExecuteStream（流式客户端）                                             |
|    - 出站时逐 chunk 归一化 delta.tool_calls                              |
+--------------------------------------------------------------------------+
                                |
                                v
            https://opencode.ai/inference/openai/v1/chat/completions
```

### 3.1 载荷次序

所有内置转换（`outgoingTransforms`）均先于 `helps.ApplyPayloadConfigWithRequest` 执行，
从而保证用户在配置文件中设置的 payload 规则仍拥有最高的覆盖与过滤优先级。

### 3.2 组件划分

| 文件 | 职责 |
| --- | --- |
| `internal/runtime/executor/opencode_executor.go` | 执行器、网关请求头契约、凭据归一化、强制流式。 |
| `internal/runtime/executor/helps/opencode_identity.go` | session ID 生成与亲和性缓存。 |
| `internal/runtime/executor/helps/opencode_tools.go` | 工具 schema 合并（出站）与工具调用归一化（入站）。 |
| `internal/runtime/executor/helps/opencode_tools.json` | 内嵌的五大核心工具规范 schema。 |
| `internal/runtime/executor/helps/stream_aggregator.go` | 可复用的 SSE 分片聚合器，与 `CodeBuddyCNExecutor` 共用。 |

## 4. session ID 算法

来源：OpenCode 仓库中的 `Identifier.descending("session")`。

```ts
const currentTimestamp = timestamp ?? Date.now()
if (currentTimestamp !== lastTimestamp) {
  lastTimestamp = currentTimestamp
  counter = 0
}
counter += 1

let now = BigInt(currentTimestamp) * BigInt(0x1000) + BigInt(counter)
if (descending) {
  now = ~now
}

const timeBytes = new Uint8Array(6)
for (let i = 0; i < 6; i += 1) {
  timeBytes[i] = Number((now >> BigInt(40 - 8 * i)) & BigInt(0xff))
}
return prefixes[prefix] + "_" + bytesToHex(timeBytes) + randomBase62(LENGTH - 12)
```

Go 实现严格遵循该算法：

- 毫秒时间戳与同毫秒内单调递增的计数器通过
  `(nowMs * 0x1000) + (counter & 0xFFF)` 组合成一个 uint64。
- 对 descending 变体执行 48 位按位取反。
- 取前 6 字节渲染为 12 位小写十六进制字符。
- 追加 14 个随机 base62 字符，最终形如 `ses_<12hex><14base62>`。

会话亲和性通过一个有界的内存缓存维护，将下游会话信号映射到生成的规范化 ID，
从而使多轮对话复用同一个上游会话，而非每请求都重新生成。亲和性只是锦上添花，
网关对全新的 ID 同样放行。

## 5. 工具映射

### 5.1 出站

调用方自有的工具会被完整保留。随后按名称精确合并 OpenCode 的五大核心工具，
凡名称冲突者一律替换为规范 schema：

| OpenCode 工具 | schema 来源 |
| --- | --- |
| `shell` | `opencode_tools.json` |
| `read` | `opencode_tools.json` |
| `edit` | `opencode_tools.json` |
| `glob` | `opencode_tools.json` |
| `grep` | `opencode_tools.json` |

### 5.2 入站

Claude Code 使用 PascalCase 工具名且参数命名不同，因此上游工具调用必须改写，
否则客户端会因遇到未知工具而报错：

| 上游 | 下游 | 参数变化 |
| --- | --- | --- |
| `shell` | `Bash` | 无 |
| `read` | `Read` | `path` -> `file_path` |
| `edit` | `Edit` | 无 |
| `glob` | `Glob` | 无 |
| `grep` | `Grep` | 无 |

聚合后的非流式响应与逐 chunk 的流式 delta 两条路径均已归一化。

## 6. 路由与注册

`registerOpenAICompatProviderExecutor` 在满足以下任一条件时，
将 provider 路由到 `OpenCodeExecutor`：

- `providerKey` 等于 `opencode`
- `compat_name` 属性等于 `opencode`
- `providerKey` 等于派生的 `openai-compatible-opencode`
- `base_url` 包含 `opencode.ai/zen` 或 `opencode.ai/inference`

## 7. 配置示例

```yaml
openai-compatibility:
  - name: "opencode"
    base-url: "https://opencode.ai/inference/openai/v1"
    api-key-entries:
      - api-key: "sk-..."
    models:
      - name: "deepseek-v4.1-flash-free"
        alias: "deepseek-v4.1-flash-free"
      - name: "mimo-v2.6-flash-free"
        alias: "mimo-v2.6-flash-free"
      - name: "longcat-2.5-preview-free"
        alias: "longcat-2.5-preview-free"
      - name: "big-pickle"
        alias: "big-pickle"
      - name: "nemotron-3.5-lightning-free"
        alias: "nemotron-3.5-lightning-free"
```

免费层请求不使用 `api-key` 的值，执行器会将其替换为 `public`。
同一 provider 下的非免费模型则按原样使用所配置的 key。

## 8. 本期范围

已覆盖：

- 免费层模型的 `/v1/chat/completions`，流式与非流式均支持。
- 以 Claude Code 为主要下游客户端，同时兼容标准 OpenAI 客户端。

未覆盖：

- 仅支持 Responses API 的模型（例如 `muse-spark-1.3-contributor-free`，
  只接受 `/v1/responses`）。协议转换留作后续迭代。

## 9. 上游已知行为

- 免费层容量是共享的。部分模型在产出第一个 token 前需排队 40~120 秒，
  排队期间会下发 `: keep-alive` SSE 注释。这些注释被原样转发。
- 部分模型有地域限制，会返回
  `{"type":"error","error":{"type":"RegionError","message":"This model is not available in your country"}}`。
  这是上游策略，非代理缺陷。
- OpenCode 可随时下线某个免费模型，下线后该模型返回 "Model is unavailable"。

## 参考来源

- OpenCode session ID 算法（`Identifier.descending("session")`）：
  https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/id/id.ts
- OpenCode Web 端使用的 session ID 实现：
  https://github.com/anomalyco/opencode/blob/dev/packages/app/src/utils/id.ts
- OpenCode issue #49433 —— 非 OpenCode 客户端调用网关时报
  "OpenCode's free tier can only be used from within OpenCode"
- OpenCode issue #51219 —— 官方客户端额度耗尽后仍被该网关拦截，
  佐证 gate 的判定依据是「请求身份」而非「账户层级」
