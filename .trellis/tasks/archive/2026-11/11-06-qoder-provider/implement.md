# Qoder provider 实施方案

对应 `design.md`. 6 个阶段, 每阶段可独立验证, 按序推进.

---

## P0 — COSY 编码与签名

**目标**: `qoder_encode` + `CosySession` 与 Python 参考实现逐字节一致.

### 文件
- `internal/runtime/executor/helps/qoder_cosy.go` (新增)
- `internal/runtime/executor/helps/qoder_cosy_test.go` (新增)

### 内容

```go
// 常量
const qoderAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
const qoderPad = '$'
const cosyVersion = "1.1.75"
const cosyClientType = "5"
const cosyUserAgent = "undici"
const cosyClientIP = "169.254.198.161"

// 编码
func QoderEncode(plain []byte) string
func QoderDecode(encoded string) ([]byte, error)

// 会话
type CosySession struct { ... }
func NewCosySession(uid, nickname, accessToken, refreshToken, userType, orgID, orgName string) (*CosySession, error)
func (s *CosySession) Headers(encodedBody, rawURL, modelKey string, sse bool) map[string]string
func (s *CosySession) Bearer(encodedBody, rawURL string) (payloadB64, date, bearer string)
```

### 测试缝 (必须)
- `newCosySessionWithKey(..., tempKey string)` — 注入固定 AES 密钥
- `bearerWithRequestID(body, url, requestID string)` — 注入固定 requestId
- `cosyNowFunc` — 注入固定时间

### 黄金向量
用固定输入跑 Python 参考实现, 生成以下确定性中间量并硬编码进测试:
1. `QoderEncode([]byte("{}"))`
2. `QoderEncode(一个 1000 字节的 JSON)` (覆盖三段重排的边界)
3. `jsonSortedCompact(identity)`
4. `identity` 明文
5. `raw` 签名字符串
6. `md5(raw)`
7. `payloadB64` (固定 info + 固定 requestId)

**不可比对**: RSA 密文, 最终 bearer (含随机 padding).

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P0.1 `QoderEncode` / `QoderDecode` + 字母表映射
- [x] P0.2 `jsonSortedCompact` (键升序, 无空白, UTF-8 原样)
- [x] P0.3 `pkcs7Pad` + `aesCBCEncrypt` (key == iv)
- [x] P0.4 RSA 公钥解析 + `rsa.EncryptPKCS1v15` 封装 (注入随机源)
- [x] P0.5 设备指纹派生 (machineId / machineType / machineToken / hostname / machineOS)
- [x] P0.6 `newCosySessionWithKey` / `bearerWithRequestID` / `cosyNowFunc` 测试缝
- [x] P0.7 `Bearer()` + path 剥离 `/algo` + `Headers()`
- [x] P0.8 黄金向量测试 7 组全部通过
- [x] P0.9 `gofmt` + `go build ./cmd/server`

### 验证
```bash
go test ./internal/runtime/executor/helps/ -run TestQoderCosy
```

---

## P1 — 请求体构建与流解包

**目标**: 给定 OpenAI payload 产出期望的上游 body; 给定上游帧序列产出期望 chunk.

### 文件
- `internal/runtime/executor/helps/qoder_body.go` (新增)
- `internal/runtime/executor/helps/qoder_stream.go` (新增)
- 对应 `_test.go`

**D8 已拍板: 不内嵌 `qoder_system_prompt.txt`.** 客户端未给 system 时使用中性兜底
`"You are a helpful assistant."` (常量, 无 `go:embed`). 理由见 `research/06`:
官方 12.3KB 模板含采集者机器路径 (`D:/Projects/Qoder`)、未渲染占位符、以及本地不会下发的
工具名 (`TodoWrite`/`Bash`/`Task`), 且客户端在真实场景下 100% 自带 system prompt.

### qoder_body.go

```go
type QoderCatalogMeta struct {
    Key, DisplayName string
    IsVL, IsReasoning bool
    MaxInputTokens   int
}

func BuildQoderBodyDoc(doc *ojson.Doc, payload []byte, meta QoderCatalogMeta,
                       userType string, sessionID string) error
```

字段集依据 `research/08` A.2: Qoder 的 ModelInfo 转换只消费
`key` / `display_name` / `max_input_tokens` / `is_vl` / `thinking_config` / `enable`.
`QoderCatalogMeta` 只需前 4 个; `thinking_config` 由 P4 映射进 `registry.ModelInfo`,
`enable` 由 P4 用于过滤, 都不进 body builder.

关键点:
- 常量信封内嵌 (design.md 3.1)
- system prompt: 客户端给则替换, 否则中性兜底常量
- `chat_context.text.type = "text"` 与 `extra.originalContent.type = "text"` **显式写**
- `parameters.max_tokens` 默认 `32768`
- `business.model_key` 必须写
- `max_input_tokens` 缺失时兜底 `180000` (参考实现 `qoder_proxy.py:6294`)
- 全程 `ojson.Doc`, 不中途序列化
- 注意 `ojson.NewDoc(nil)` 产出**空字节串**而非 `{}`, 必须用 `NewDocString("{}")` 或显式 `Set`

### qoder_stream.go

```go
// 信封
type QoderStreamFrame struct {
    StatusCode int    `json:"statusCodeValue"`
    Body       string `json:"body"`
}

// 解包一帧; done=true 表示收到内层 [DONE]
func DecodeQoderStreamFrame(data []byte) (inner []byte, status int, done bool, err error)

// 清理空噪声; 返回 nil 表示整帧丢弃
func CleanQoderChunk(doc *ojson.Doc) (changed bool, drop bool)
```

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P1.1 `QoderCatalogMeta` + 常量信封 + `BuildQoderBodyDoc` 骨架
- [x] P1.2 消息压平 (developer->system, 首个 system 抽取, tool 保留, tool_calls 透传)
- [x] P1.3 `model_config` 10 键 + `chat_context` + `parameters` + `business`
- [x] P1.4 系统提示词中性兜底 (D8)
- [x] P1.5 `DecodeQoderStreamFrame` (含 statusCodeValue / 内层 [DONE] / status 非 200)
- [x] P1.6 `CleanQoderChunk` (NOISE_KEYS / 空 function_call / 空 tool_calls / 空 delta)
- [x] P1.7 body builder 单测 (给定 payload -> 期望 JSON)
- [x] P1.8 stream 单测 (正常 / 空 delta / reasoning_content 空 / status 500 / [DONE] / UTF-8 边界)
- [x] P1.9 `gofmt` + `go build ./cmd/server`

### 测试用例
- 正常 chunk -> 原样通过
- 空 delta (`{"choices":[{"delta":{}}]}`) -> 丢弃
- `delta.reasoning_content: ""` -> 删除该键
- `statusCodeValue: 500` -> 返回错误
- `{"body":"[DONE]"}` -> done
- 多字节 UTF-8 边界
- `thinking_config` 存在但无 `efforts` 的模型 (qmodel): 不写 `reasoning_effort` 时不应崩

### 验证
```bash
go test ./internal/runtime/executor/helps/ -run 'TestQoderBody|TestQoderStream'
```

---

## P2 — executor 骨架

**目标**: 可编译可注册, 用 mock 上游跑通端到端.

### 文件
- `internal/runtime/executor/qoder_executor.go` (新增, ~500 行)
- `internal/constant/constant.go` 加 `QoderCN = "qoder"` / `QoderINTL = "qoder-intl"`
- `sdk/cliproxy/service_executors.go:211-242` baseline 加两个
- `sdk/cliproxy/service_executors.go:300-369` switch 加两个 case

### 结构

```go
type QoderExecutor struct {
    provider string          // "qoder" | "qoder-intl"
    cfg      *config.Config
    // CountTokens 靠嵌入继承
    *OpenAICompatExecutor
}
```

### 方法
| 方法 | 实现 |
|---|---|
| `Identifier()` | 返回 `e.provider` |
| `UsesConfig(cfg)` | 手写 (嵌入不派发) |
| `Execute` | 强制 `opts.Stream=true`, 消费流, `AggregateOpenAIStreamChunks` |
| `ExecuteStream` | 自建帧循环 (design.md 4) |
| `Refresh` | 按前缀分流 (P3 补全, 先占位) |
| `CountTokens` | 嵌入继承 |
| `HttpRequest` | 12 行委托 |
| `ForAPIKey()` | 返回自身 |
| `SupportsApplyPatch()` | false |

### ExecuteStream 顺序 (不可调换)
```
EnsureSessionContext
reporter = NewExecutorUsageReporter; defer TrackFailure
translate -> ojson.NewDoc
  -> 若 source 是 responses, 走 compat 的 compaction 路径
  -> ApplyRequestThinkingDoc
  -> 构建 Qoder body (P1)
  -> outgoing transforms (若有)
  -> ApplyPayloadConfigWithRequestDoc    <- 最后一道语义屏障
  -> qoderEncode(doc.Bytes())
  -> CosySession.Headers
  -> 发请求
  -> 帧循环 (P1 DecodeQoderStreamFrame)
  -> TranslateStreamWithClaudeInputTokens
  -> 发出
```

### 已核实的接口约束 (2026-11-06)

| 约束 | 影响 |
|---|---|
| `statusErr` 字段全小写私有 | `QoderExecutor` **必须**在 `executor` 包内, 不能放 `helps/` |
| `baselineExecutorAuths()` 不含 qoder | 不加会落到 `default` 分支被当成 `openai-compatibility`, **静默走错 executor** |
| 嵌入的 `SupportsApplyPatch()` 是 `e != nil` | Qoder 恒为 true, 必须显式覆盖为 false |
| `UsesConfig` 嵌入不派发 | 必须手写 |
| `resolveCredentials` 只读 `Attributes["base_url"/"api_key"]` | Qoder 凭证在 `Metadata`, 不能复用 compat 发送路径 |
| `helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)` | 传 0 即无超时, 内部已 `httptransport.Decorate` |
| `helps.NewExecutorUsageReporter` 只要求 `Identifier()` | 组合体可直接传 |

`switch` 里两个 provider 可合并: `case "qoder", "qoder-intl":` 按 `a.Provider` 决定 realm.

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P2.1 `internal/constant` 加 `QoderCN` / `QoderINTL`
- [x] P2.2 `QoderExecutor` 结构 + `Identifier` / `UsesConfig` / `SupportsApplyPatch(false)` / `ForAPIKey`
- [x] P2.3 `Execute` 强制流式 + `AggregateOpenAIStreamChunks`
- [x] P2.4 `ExecuteStream` 请求构建链 (translate -> thinking -> body -> payload barrier -> encode -> headers)
- [x] P2.5 `ExecuteStream` 帧循环 (DecodeQoderStreamFrame -> CleanQoderChunk -> translate -> send)
- [x] P2.6 首帧同步探测 (status/排队判定, 不提前返回 StreamResult)
- [x] P2.7 `HttpRequest` / `Refresh` 占位
- [x] P2.8 `baselineExecutorAuths` + `switch` 注册
- [x] P2.9 mock 上游端到端测试 (流式 + 非流式)
- [x] P2.10 `gofmt` + `go build ./cmd/server` + `go test ./internal/runtime/executor/ -run TestQoder`

### mock 上游测试
`httptest.Server` 返回固定帧序列, 断言客户端收到的 chunk 序列.

### 验证
```bash
go build -o test-output ./cmd/server && rm -f test-output
go test ./internal/runtime/executor/ -run TestQoder
```

### P2 完成记录 (2026-11-06)

**状态**: 全部完成. `go build ./cmd/server` 通过;
`go test ./internal/runtime/executor/ -run TestQoder` 13 项全绿;
`go test ./internal/runtime/executor/` 仅剩已知既有失败
`TestClaudeCloakedDateReminderPersistsAcrossRestartWithLocalBBoltKV`;
`go test ./sdk/cliproxy/` 通过.

**交付文件**:
- `internal/runtime/executor/qoder_executor.go` (新增)
- `internal/runtime/executor/qoder_executor_test.go` (新增, 10 个 `TestQoder*`)
- `internal/constant/constant.go` (加 `QoderCN` / `QoderINTL`)
- `sdk/cliproxy/service_executors.go` (`baselineExecutorAuths` + switch case)

**实现要点**:
- `Execute` 把 `opts.ResponseFormat` 临时改为 openai 收集 chunk, 折叠后再
  `sdktranslator.TranslateNonStream` 回客户端格式; 否则 Claude 入口会拿到裸 OpenAI chunk.
- `qoderQueuedError` 实现 `Error` / `StatusCode` / `RetryAfter` / `IsRequestScoped`,
  P6 在此基础上加等待重试.
- `qoderCatalogMeta` 暂由 `cliproxyauth.ResolvedModelInfo` 投影
  (`DisplayName` / `image` modality / `Thinking != nil` / `InputTokenLimit`),
  P4 接入真实目录后改为读 `ModelInfo` 的 catalog 字段.
- `uid` 缺失时退化为 `auth.ID`, 保证指纹稳定; P3 登录后写入真实 `uid`.

**验收修正 (2026-11-06 第二轮)**:
- 403 分类改为四段优先级: 排队 -> 内容策略 -> 额度 -> 其余授权拒绝.
  内容策略标记串与参考实现 `qoder_proxy.py:6855-6861` 逐字一致
  (`DataInspectionFailed` / `inappropriate content` / `input text data may contain` /
  `ContentFilter` / `SensitiveContent`), 命中时**不带** `credentialScoped`.
- **分类必须在截断之前**. `helps.QoderStreamStatusError` 在构造时已把 `Body`
  截到 400 字节, 所以 executor 不能用 `err.Error()` 做分类 — 新增 `qoderFrameError`,
  直接吃原始帧行文本. 该缺陷由
  `TestQoderExecutorContentPolicyMarkerAfterBodyLimitStillMatches` 锁定.
- 测试补齐请求目标断言: `method == POST` 且
  `url == "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"`
  (字符串字面量, 不引用常量).
- 测试补齐签名可复现断言: 用捕获到的编码 body + `cosy-key` + `cosy-date` +
  剥掉 query 与 `/algo` 的签名 path 重算 md5, 必须等于 `authorization` 的签名段.

以下是动工前核实的接口事实, 保留备查.

1. **推理端点与 design.md 不一致 (重要)**. research/07 的示例 URL 写的是
   `/algo/api/v2/chat/completions`, 但参考实现 `qoder_proxy.py:330` 的真实常量是

   ```
   CHAT_PATH = "/algo/api/v2/service/pro/sse/agent_chat_generation"
               "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
   ```

   `qoderSignPath` 会剥掉 `/algo`, 所以签名覆盖的 path 是
   `/api/v2/service/pro/sse/agent_chat_generation`; query 不进签名.
   `AgentId=agent_common` 与 body 的 `agent_id` 必须一致.

2. **包内 `statusErr` 私有字段可用**: `code` / `msg` / `retryAfter` /
   `credentialScoped` / `streamTruncated` / `transientRateLimit` / `diagnostic`.
   已实现 `StatusError` / `IsCredentialScoped` / `IsStreamTruncated` /
   `IsTransientRateLimit`, **未**实现 `IsRequestScoped` — 排队错误需在本文件自定义类型.

3. **首帧探测可行且必要**: `ExecuteStream` 要在返回 `StreamResult` 前同步读第一帧,
   否则 Gin 会先给客户端 200; 且 conductor 的 `isStreamTruncatedError` /
   `isRequestScopedError` 只在 executor 返回 error 时生效.

4. **`BuildQoderBodyDoc` 会整体替换文档** (`*doc = *ojson.NewDocString("{}")`),
   因此 payload 屏障必须在它之后; `ApplyPayloadConfigWithRequestDoc` 作用在
   最终 Qoder 信封上, 顺序不能调换.

5. **分类信号来源**: 排队 = `403` + body 含 `"10605"`/`"isQueued"` +
   可选 `retryAfterSeconds`; 额度 = `403` + `code` 110 (每日上限) / 112 (额度耗尽).
   参考实现 `:6823-6830`, `:7080-7082`.

6. **`Execute` 非流式**: 强制 `opts.Stream = true`, 收集 chunk 后用
   `helps.AggregateOpenAIStreamChunks` 折叠, 与 `CodeBuddyCNExecutor:75-107` 同构.

---

## P3 — 认证

**目标**: 真实账号可登录, token 可刷新.

### 文件
- `internal/auth/qoder/qoder.go` (新增)
- `internal/auth/qoder/refresh_route.go` (新增)
- `internal/auth/qoder/qoder_test.go`
- `sdk/auth/qoder.go` (新增)
- `sdk/auth/refresh_registry.go:9-20` 加两条
- `sdk/cliproxy/service_auth.go:21-35` 加两个
- `internal/cmd/auth_manager.go:13-28` 加两个 **[易漏]**
- `internal/cmd/qoder_login.go` (新增)
- `cmd/server/main.go` 五处
- `internal/api/handlers/management/auth_files_provider_oauth.go` 加两个 handler
- `internal/api/handlers/management/auth_files_provider_oauth.go:423-426` 加两个 **[易漏]**
- `internal/api/handlers/management/auth_files_v8.go:403-428` 两个 case
- `internal/api/server_management.go:196-204` 两条路由
- `internal/api/handlers/management/oauth_sessions.go:645-664` 两个 case
- `internal/watcher/synthesizer/file.go:238-246` base_url 注入

### 顺序
1. 低层客户端 (含 device flow + PAT + 刷新分流) + 单测
2. Authenticator
3. 两处 auth manager 注册 + refresh registry
4. 管理 API handler + 路由
5. CLI flag + 登录命令
6. executor 的 `Refresh` 补全

### 已核实的接入点后果 (漏掉的代价)

| 接入点 | 漏掉的后果 |
|---|---|
| `NormalizeOAuthProvider` 加两个 case | 管理 API 授权 URL 请求直接 **404**, 不是降级 |
| `isIdentitylessOAuthProvider` 加两个 | 带 `?name=` 定向重登报 "account does not match target credential" |
| `internal/cmd/auth_manager.go` | `--qoder-login` CLI 报 "authenticator not registered" |
| `sdk/cliproxy/service_auth.go` | 管理 API 登录失败 (与上式是**两个独立** manager) |
| `refresh_registry.go` | `ProviderRefreshLead` 返回 nil, token **永不自动刷新** |
| `argvFlagConsumesValue` | 既有缺陷, `codebuddy-cn-login` 就漏在这里 |
| `executor.Refresh` | `sdk/auth.Authenticator` 接口**无** Refresh 方法, 这是唯一刷新入口 |

### 硬约束

- `expires_in` 单位是**毫秒**, 必须 `/1000`. 漏了会让过期时间算到 80 年后, 自动刷新永不触发.
- 刷新按 token 前缀分流: `drt-` -> `/api/v1/deviceToken/refresh`; `jrt-` -> `/api/v1/jobToken/refresh`; 有 PAT -> `/api/v1/jobToken/exchange`.
- `organization_id` 必须存**字符串** (读路径无 `UseNumber`, 大整数会丢精度).
- auth 文件 `type` 必须**精确等于 provider key** (`qoder` / `qoder-intl`), 合成器靠它推导 provider.
- COSY 的 `uid` 来自 auth 文件. 缺 `uid` 时 `qoderCredentials` 会回退到 `auth.ID`; P3 落地后应写入真实 uid.

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P3.1 `internal/auth/qoder` 双区常量 + PKCE + device flow
- [x] P3.2 `internal/auth/qoder` PAT 交换
- [x] P3.3 `internal/auth/qoder` 刷新分流 (`drt-` / `jrt-` / PAT)
- [x] P3.4 `internal/auth/qoder` 单测 (含 `expires_in` 毫秒转换)
- [x] P3.5 `sdk/auth/qoder.go` Authenticator (双区共用, `Metadata.realm` 区分)
- [x] P3.6 `refresh_registry.go` 两条注册
- [x] P3.7 两处 auth manager 注册 (`sdk/cliproxy/service_auth.go` + `internal/cmd/auth_manager.go`)
- [x] P3.8 管理 API handler + `isIdentitylessOAuthProvider` + v8 switch + v0 路由
- [x] P3.9 `NormalizeOAuthProvider` 两个 case
- [x] P3.10 `internal/watcher/synthesizer/file.go` base_url / auth_kind 注入
- [x] P3.11 CLI flag + `internal/cmd/qoder_login.go` + `argvFlagConsumesValue`
- [x] P3.12 executor `Refresh` 补全 (替换 P2 的占位错误)
- [x] P3.13 `gofmt` + `go build ./cmd/server` + 相关包测试

### 验证
```bash
go build ./cmd/server
./cli-proxy-api --qoder-login --no-browser      # 手动
curl -X GET 'http://127.0.0.1:8317/v0/management/qoder-auth-url'
```

---

## P4 — 模型目录

**目标**: `/v1/models` 出现正确模型, `enable=false` 被过滤.

### 文件
- `sdk/auth/qoder_models.go` (新增)
- `sdk/cliproxy/qoder_models.go` (新增)
- `sdk/cliproxy/qoder_model_probes.go` (新增)
- `sdk/cliproxy/service_models.go:91` 加两个 case

### 克隆 Antigravity 的部分
| Antigravity | Qoder |
|---|---|
| `antigravityCapabilityKeyForRoute` (`:104-112`) | 含 auth.ID/epoch/provider/endpoint/tokenHash |
| `queueAntigravityModelRefresh` (`probes:31-78`) | 同构 |
| `applyAntigravityModelHints` (`:549-600`) | 同构, 换 `ReplaceClientModels` |
| `persistAntigravityCapabilityHints` (`:176-216`) | 同构 |

### 目录项转换
见 `design.md` 7.3 / 7.4. 要点:
- `enable == false` 不注册
- `thinking_config` 存在 -> `Thinking = {Levels, ZeroAllowed}`, **不写 min/max**
- 不存在 -> `Thinking = nil`
- `is_reasoning` 不作判据

### 上游 payload 事实 (research/08 A 节)

- 顶层只有 `{"chat": [...]}`, **无分页**, 无需额外 query (除 `Encode=1`)
- `thinking_config.disabled` 恒为 **object** (可为 `{}`), 判据必须是**键存在性**
  (`"disabled" in tc`), 不能读值真假
- `efforts` 永远在 `enabled.efforts` 下, 不是 `thinking_config` 直接子键
- `enable` 缺失视为 `true`
- `qmodel` / `qmodel_latest` 的 `enabled` 存在但**无 `efforts` 键** — 见
  `research/03` 的 `levels:["high"]` 表达手段说明

### 已核实的调用契约 (research/08 C 节)

| 调用 | 签名/语义 |
|---|---|
| `ReplaceClientModels` | `(clientID, clientProvider string, expectedEpoch uint64, models) (uint64, bool)` |
| `PersistMetadataEntry` | `(ctx, id string, expectedEpoch uint64, key string, value any, validate func) error` — 写 `auth.Metadata` 顶层 key |
| `ReconcileRegistryModelStates` | 必须调用, 清理消失模型的冷却状态 |
| `RefreshSchedulerEntry` | 仅当 `updated == true` 时调用 |

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P4.1 `sdk/auth/qoder_models.go` — `FetchQoderModels` 网络编解码 (复用 `helps.CosySession`)
- [x] P4.2 `FetchQoderModels` 单测 (payload 解析 / enable 过滤 / thinking 映射)
- [x] P4.3 `sdk/cliproxy/qoder_models.go` — 路由 key / 缓存 / 投影 / 带围栏发布
- [x] P4.4 `sdk/cliproxy/qoder_model_probes.go` — 队列 / epoch 围栏 / 失败 key / backoff
- [x] P4.5 `service_models.go` provider switch 两个 case + 冷启动播种
- [x] P4.6 `service_lifecycle.go` 启动探测循环 (Home 模式跳过)
- [x] P4.7 `internal/registry/model_definitions.go` 静态种子 4 处
- [x] P4.8 `gofmt` + `go build ./cmd/server` + `sdk/cliproxy` 测试

### 验证
```bash
curl -s http://127.0.0.1:8317/v1/models | jq '.data[].id'
curl -s http://127.0.0.1:8317/v1/models | jq '.data[] | select(.id|test("qmodel"))'
```

---

## P5 — config 双段

**目标**: v8 保存往返不丢配置.

### 文件
- `internal/config/config.go` 加两个字段
- `internal/config/config_types.go` 加两个类型
- `internal/config/config_v8.go:67-73` 加两行
- `internal/config/config_normalization.go` 两个 sanitizer
- `internal/config/config_load.go:312-313` 与 `internal/config/parse.go:114-129` **两处注册**
- `config.example.yaml` 注释块加两段
- `internal/watcher/synthesizer/config.go:95-124` 加两个合成调用

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P5.1 `config.go` 加 `QoderKey` / `QoderINTLKey` 字段
- [x] P5.2 `config_types.go` 加两个 Key 类型
- [x] P5.3 `config_v8.go` `v8KeyFamilies` 加两行
- [x] P5.4 `config_normalization.go` 两个 sanitizer
- [x] P5.5 `config_load.go` **与** `parse.go` **两处都**注册 sanitizer
- [x] P5.6 `config.example.yaml` 两段注释
- [x] P5.7 `internal/watcher/synthesizer/config.go` 两个合成调用
- [x] P5.8 测试 + `gofmt` + `go build`

### 测试
- v8 保存往返: 写 `api-keys.qoder` + `api-keys.qoder-intl` -> LoadConfig -> SaveConfig -> 比对
- `TestV8ExampleLoadsAndRoundTrips` 必须继续通过
- sanitizer 在两个入口行为一致

### 验证
```bash
go test ./internal/config/ ./internal/api/handlers/management/
```

---

## P6 — 排队与错误

**目标**: 403 排队不冷却账号, 超预算后正确回传.

### 文件
- `internal/runtime/executor/qoder_executor.go` (补充)
- 新增 `helps/qoder_errors.go` 可选

### 内容
```
错误分类表 (design.md 5.1)
排队等待实现 (design.md 5.2)
瞬时重试 + 换域 (design.md 5.3)
```

### 已具备的基础 (P2 已实现, P6 在此之上扩展)

- `qoderQueuedError` 已实现 `IsRequestScoped() bool`, 首帧探测已能区分排队与额度
- 403 四段分类: 排队 -> 内容策略 -> 额度 -> 其余授权拒绝
- `qoderClassifyUpstreamError` 用**原始帧文本**判定, 不受 400 字节截断影响

### 预算 (design.md 5.2)

```
单轮最长等待   180s
总预算         600s
最多轮数       3
抖动           0..2s
```

- 用 `time.NewTimer` + `select ctx.Done()`, **不设** `http.Client.Timeout`
- 超出预算 -> 返回 `IsRequestScoped() == true` 的错误

### 网关候选 (design.md 5.3)

- CN: `gateway.qoder.com.cn` (单主机)
- INTL: `api3.qoder.sh` -> `api2.qoder.sh` -> `api1.qoder.sh`

传输层失败时先换域; 签名只覆盖 path, 换域不影响签名有效性.

### 执行清单 (逐项勾选, 每完成一项立即更新本文件)

- [x] P6.1 排队等待实现 (`retryAfterSeconds` + 抖动 + 预算)
- [x] P6.2 瞬时重试 (同账号 1s/2s 两次)
- [x] P6.3 INTL 换域 (api3 -> api2 -> api1)
- [x] P6.4 预算耗尽返回 request-scoped 错误
- [x] P6.5 队列测试 (不冷却 / 超预算 / 换域)
- [x] P6.6 `gofmt` + `go build` + 测试

### 测试
- 构造 403 + `{"code":"10605","isQueued":true,"retryAfterSeconds":1}`
- 断言: 等待后重试, 不产生冷却
- 构造超预算场景, 断言返回 `IsRequestScoped() == true`
- 传输层失败断言换域

### 验证
```bash
go test ./internal/runtime/executor/ -run TestQoderQueue
```

---

## 收尾检查

```bash
gofmt -w internal/ sdk/ cmd/
go build -o test-output ./cmd/server && rm -f test-output
go test ./internal/runtime/executor/ ./internal/config/ ./sdk/cliproxy/ ./internal/api/handlers/management/
go vet ./...
```

**已知既有失败** (与本任务无关, 不修不对比):

1. `TestClaudeCloakedDateReminderPersistsAcrossRestartWithLocalBBoltKV` in `internal/runtime/executor`.

   失败信息: `TempDir RemoveAll cleanup: unlinkat ...\001\kv.db: The process cannot access the file because it is being used by another process.`
   Windows 上 bbolt 句柄在 TempDir 清理时尚未释放, 与测试逻辑无关.
   归因证据: 文件 `internal/runtime/executor/claude_cloaked_date_pin_test.go` 由提交
   `e6e5708e` 引入 (HEAD 祖先), `git status --short` 为空; 在 HEAD 的干净 worktree 上复跑
   同样失败 (2026-11-06 复核).

2. `TestServiceHeartbeatFailoverDoesNotDrainBlockingScope` in `sdk/cliproxy`.

   失败信息: `service_executionregistry_test.go:1454: new subscription did not start while the old scope remained active`.
   **偶发**, 单跑 5 次约 1 次失败 (clean HEAD 同样复现).
   归因证据: 文件 `sdk/cliproxy/service_executionregistry_test.go` 最后修改为提交 `533687e4`
   (HEAD 祖先), `git status --short` 为空.

复核方法: 用 `git worktree add --detach <dir> HEAD` 起一个干净工作树跑同一测试, 而不是
`git stash` (后者只移除当前阶段新增文件, 会误判前几阶段已落盘文件的失败).

---

## P7 — config API-key PAT 交换 (P5 复核缺陷)

P5 的 `synthesizeQoderKeys` 把原始 `pt-...` 直接写进 `metadata["access_token"]`,
而 executor 把该字段当作 COSY bearer 使用. Qoder 个人访问令牌不能直接做 COSY bearer,
必须先经 `POST /api/v1/jobToken/exchange` 换成 job token pair, 因此 config API-key 路径
在修复前**完全无法认证**.

修复位置在 executor 而不是合成器: 交换结果是 per-credential 状态, 必须由 auth manager
持久化, 而 `RequestAuthPreparer` 正是为此设计的钩子.

### 执行清单

- [x] P7.1 `QoderExecutor` 实现 `cliproxyauth.RequestAuthPreparer`
- [x] P7.2 `ShouldPrepareRequestAuth`: 仅当有 `personal_token` 且尚未交换才返回 true;
      OAuth 凭证恒 false (不顶掉活跃 device 会话)
- [x] P7.3 `PrepareRequestAuth`: 按 provider key 选 realm, 走 `auth.ProxyURL`,
      调 `ExchangePAT`, 在克隆体写入 job token / refresh_token / token_type /
      expires_in / expired / `pat_exchanged` 标记, 保留 identity
- [x] P7.4 失败映射: 401/403 credential-scoped, 5xx/传输层 502, 消息不含 token
- [x] P7.5 测试 (判定矩阵 / 成功 / 失败映射 / 端到端去重)
- [x] P7.6 `gofmt` + `go vet` + `go build` + 测试

### 交付文件

- `internal/runtime/executor/qoder_executor_auth.go` (新增)
- `internal/runtime/executor/qoder_executor_prepare_test.go` (新增)

### 验证

```bash
go test ./internal/runtime/executor/ -run TestQoderExecutorPrepareRequestAuth -count=1
go test ./internal/runtime/executor/ -run TestQoderExecutorShouldPrepareRequestAuthMatrix -count=1
go test ./internal/runtime/executor/ -run TestQoderExecutorConfigPATCredentialExchangesOnce -count=1
```

端到端用例断言: 交换端点恰好被调 1 次, 一次 500 重试后仍是 1 次, 第二个请求复用持久化
结果仍是 1 次; 每个推理请求的 COSY 签名都能从实际报文重算通过.

---

## P8 — 质量审查补漏 (2026-11-07)

`trellis-check` 审查发现 design.md 的两处要求未落地, 已补齐.

### 缺陷 1: usage accounting marker 缺失

`design.md` §9 要求 `sdk/cliproxy/usage/accounting.go` 的 subset marker 列表加 `"qoder"`,
但实现阶段漏了. 后果: Qoder 用量落到 `tokenAccountingSemanticsUnknown`, breakdown 降级为
`unclassified`, 数字本身不错但账目质量下降.

- `sdk/cliproxy/usage/accounting.go:357-361` 加 `"qoder"` (子串匹配同时覆盖 `qoder-intl`),
  注释说明上游是 OpenAI 形状的 SSE, reasoning token 已含在 output 内.
- `sdk/cliproxy/usage/accounting_test.go` 表驱动加 `qoder` / `qoder-intl` 两个用例.

### 缺陷 2: payload 屏障无 Qoder 回归

AGENTS.md 与 `quality-guidelines.md` 要求新增请求构造路径必须在
`payload_barrier_test.go` 加用例. 屏障调用点 `qoder_executor.go:381` 顺序本来正确,
但无测试锁定.

- `internal/runtime/executor/payload_barrier_test.go` 的
  `TestPayloadBarrierMultiExecutorSingleDocConsistency` 加子测试, 覆盖 Execute 与
  ExecuteStream 两条路径.
- 断言 Override 的 `parameters.max_tokens=12345` 到达上游 (该键只有 body builder 写,
  能到达即证明屏障在 builder 之后), Filter 的 `business.model_key` 未出现在上游 body,
  且 COSY 签名可从实际报文重算通过.

### 验证 (2026-11-07)

```
gofmt -l internal/ sdk/ cmd/       无输出
go build ./cmd/server              BUILD_OK
go vet (改动包)                     无输出
go test ./internal/runtime/executor/ -run 'TestPayloadBarrierMultiExecutorSingleDocConsistency'   PASS (含两个 qoder 子测试)
go test ./internal/runtime/executor/helps/ ./internal/auth/qoder/ ./sdk/auth/
        ./internal/config/ ./internal/watcher/synthesizer/ ./sdk/cliproxy/usage/
        ./internal/api/handlers/management/                                                全部 ok
```

### 审查中额外发现并修复的 3 项

1. **真实端点签名黄金向量缺失** — `qoder_cosy_test.go` 原只覆盖已声明为非真实的
   `/api/v2/chat/completions` 形状 fixture. 新增 `TestQoderCosySignatureVectorRealPaths`,
   锁定 research/07 两个真实端点 (`agent_chat_generation` / `model/list`) 的 md5.
   mock 上游接受任意路径, 路径写错本地不会暴露, 这条是唯一防线.
2. **OAuth handler 日志可能泄漏凭据** — `auth_files_qoder_oauth.go` 两处 `log.Errorf`
   改为 `redactSessionProxyInString(...)` (代理 URL 可能内嵌用户名密码);
   PAT 导入失败原样打印 `StatusError.Error()`, 而上游会把含 `personal_token` 的
   请求体回显进错误, 改为只记录状态码 (`qoderStatusFromError`).
3. **注释与现实不符** — `qoder_executor.go` 配额分支注释声称 112/110 差异化冷却
   (30m / 次日 0 点), 但 conductor 对 credential-scoped 403 统一 30m
   (`sdk/cliproxy/auth/conductor_cooldown.go:3074-3080`). 注释改为如实描述;
   同一文件 `qoderCatalogMeta` / `qoderCredentials` 的阶段性过期注释一并清理.

### 未修复的已知差距 (如实记录)

| 严重度 | 位置 | 说明 |
|---|---|---|
| 中 | `sdk/cliproxy/qoder_models_test.go:36` | `qoderTestSleep` 用 `time.Sleep(1ms)` 轮询让路. 非计时断言, 但仍是墙钟依赖 |
| 低 | `internal/auth/qoder/qoder.go:370` `IsSessionDead` | 有实现有单测, 生产路径无调用点. design §5.1 的 `TOKEN_EXPIRE` 一行只有检测器无消费方 |
| 低 | `model_config.is_reasoning` | 参考实现读 catalog 原生字段; 本实现由 `info.Thinking != nil` 推导. 对 `thinking_config` 存在但 `is_reasoning:false` 的模型, 上游收到的值不同. `registry.ModelInfo` 无该字段, 加字段会扩散到注册表序列化面 |
| 设计差距 | 110 / 112 冷却差异 | design §5.1 要求 112 冷 30m / 110 冷至次日 0 点, 本仓库 conductor 对 credential-scoped 403 统一 30m. **未实现**, 属框架能力差距, 非本次引入 |

### 仍未证实的验收标准 (需真实账号)

`TestRequestQoderTokenDeviceFlow` 只覆盖授权 URL 构造与设备流注册; PAT 交换只覆盖
前缀校验与失败映射; 目录拉取只用固定 `{"chat":[...]}` fixture. **真实浏览器授权、
真实 PAT 交换、真实目录拉取均未在线验证.** 另: 403+10605 排队在 executor 侧已严格
验证, 但"不冷却"依赖 conductor 对 `IsRequestScoped()==true` 的处理, 跨层未做端到端断言.

---

## 依赖图

```
P0 (COSY) ─┬─> P1 (body/stream) ─> P2 (executor) ─┬─> P6 (排队)
           │                                       │
           └─────────────────> P4 (目录) <─────────┘
P3 (认证) ─────────────────────┴─> 端到端可用
P5 (config) 独立, 可与 P3 并行
```

P0 是 P1/P2/P4 的共同前置 (签名是三者的必需).
P3 与 P4 可并行 (一个管凭证, 一个管目录).
P5 完全独立.

---

## P9 — 真实账号验证 (2026-11-07)

首次用真实 China mainland 账号 (`rdxhzx`) 跑通, 结论如下.

### 已验证可用

| 项 | 结果 |
|---|---|
| OAuth 设备授权 | 成功. 授权 URL 正确, 浏览器打开后轮询拿到 token |
| auth 文件落盘 | 正确. `type=qoder`, `auth_kind=oauth`, `access_token=dt-...`, `refresh_token=drt-...` |
| `expires_in` 毫秒转换 | 正确. 上游给 2591999 -> 2026-12-07, 不是 80 年后 |
| 目录拉取 | 成功. `qoder_model_catalog_hints` 写入 auth 文件, 12 个模型全部 `enable=true` |
| 非流式推理 | 成功. ~1.2s 返回, usage 与 reasoning_tokens 正常 |
| 流式推理 | 成功. 信封解包正确, 增量 content/reasoning_content 正常 |
| COSY 签名 | 上游接受. 无 401/403 |

### 真实目录与设计假设的偏差 (重要)

**1. 真实目录没有 `thinking_config` 字段.**

拉回来的 12 个模型只有 `key/display_name/enable/is_reasoning/is_vl/max_input_tokens`.
`research/03` 与 `design.md` §7.4 都假设 `thinking_config.enabled.efforts` 存在,
按"键存在性"映射 levels. 真实数据里**一个都没有**.

后果: 走目录探测路径时, 所有模型 `Thinking = nil`, 客户端 `reasoning_effort` 会被剥离.

但实测 `/v1/models` 里 `qmodel_38max` 仍有 `low/medium/xhigh` 三档 — 说明**静态种子没有被
实时目录替换**, 或替换后合并了静态值. 实测发 `reasoning_effort: high` 得到
`level "high" not supported, valid levels: low, medium, xhigh`, 与静态种子一致.
**这一条需要确认**: 是目录探测没生效, 还是生效了但 thinking 来自静态种子.
日志里没有任何 qoder 探测记录 (debug 级别也没有), 无法从这里判定.

**2. 上游响应的 `model` 字段恒为 `auto`.**

请求 `qmodel_38max` / `kmodel` / `gmodel` / `fast`, 响应 `model` 全是 `auto`.
参考实现 `aggregate_stream` 用 `chunk.get("model")` 覆盖请求值, 同样会得到 `auto`,
所以这不是本仓库的回归, 是上游行为. 对客户端影响: 响应里的 model 与实际请求不一致.
若要修正, 需在流翻译后强制改写 `model` 回请求值 — 但那是 translator 层改动, 未做.

**3. `auto` 模型不能单独使用.**

`auto` 是路由器, 单独请求报 `model is not found` (上游 new_api_error).
它不是可直接调用的模型, 但被注册进了 `/v1/models`. 这是静态种子与真实目录共同的偏差.

### 新增发现的缺陷

| 严重度 | 说明 |
|---|---|
| 高 | `organization_id` 为空. 真实账号返回空串, COSY identity 里组织字段为空. 上游接受了, 但未确认长期是否有影响 |
| 中 | 真实目录字段集比设计假设薄, `thinking_config` 缺失导致 thinking 档位实际来自静态种子而非实时目录 |
| 中 | `auto` 是框架保留名, 在 executor 之前就被 `internal/util/provider.go` 的 `ResolveAutoModel` 拦截并改写为注册表首个可用模型, 因此 Qoder 静态种子里的 `auto` 永远不会服务请求, 只会在 `/v1/models` 里多出一个误导性条目. 已从静态种子中移除 (CN 种子 14 -> 13). 注意上文 "auto 不能单独调用" 的结论是误读: 报错 `分组 claude 下模型 ... 无可用渠道` 来自用户的第三方中转配置, 与 Qoder 无关 |
| 低 | 响应 `model` 恒为 `auto`, 与请求不一致 |

### 复现命令

```bash
./cli-proxy-api-qoder.exe --qoder-login
./cli-proxy-api-qoder.exe --config config-dev-8319.yaml
curl -H 'Authorization: Bearer key' http://127.0.0.1:8319/v1/models
curl -X POST http://127.0.0.1:8319/v1/chat/completions -H 'Authorization: Bearer key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"qmodel_38max","messages":[{"role":"user","content":"hi"}],"stream":false}'
```
