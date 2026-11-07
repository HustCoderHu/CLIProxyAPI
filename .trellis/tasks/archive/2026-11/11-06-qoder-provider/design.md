# Qoder provider 设计

决策: D1=双 provider (`qoder` CN + `qoder-intl`), D2=不合 main, D3=拉取目录, D4=可彻底重构.

调研依据见 `research/01-05-*.md`. 本文只写结论与实现结构.

---

## 0. 一句话架构

两个 provider 共用一个 `QoderExecutor` 实现 (按 provider 名区分 realm),
COSY 签名在 executor 内完成, 模型目录按 auth 异步探测并发布 (Antigravity 模式).

---

## 1. 包结构

```
internal/auth/qoder/           新增: 低层 OAuth 客户端 (双区常量 + device flow + PAT)
  qoder.go                     REALM_CONFIGS, NewClient*, StartDeviceFlow, WaitForAuthorization, Refresh
  refresh_route.go             按 token 前缀分流: drt- / jrt- / PAT
  qoder_test.go

internal/runtime/executor/
  qoder_executor.go            新增: QoderExecutor (~500 行)
  helps/qoder_cosy.go          新增: 自定义 Base64 + COSY 签名 (~200 行)
  helps/qoder_body.go          新增: 请求体构建 (常量信封 + 消息压平) (~250 行)
  helps/qoder_stream.go        新增: 信封解包 + clean_chunk (~150 行)
  helps/qoder_cosy_test.go     黄金向量测试

sdk/auth/
  qoder.go                     Authenticator (双区共用, 由 Metadata.realm 区分)
  qoder_models.go              纯 COSY 拉取编解码: FetchQoderModels(ctx, req) -> []*registry.ModelInfo

sdk/cliproxy/
  qoder_models.go              per-auth 目录探测与发布 (克隆 antigravity)
  qoder_model_probes.go        队列/围栏 (克隆 antigravity)
```

**分包理由**: `helps/` 只放无状态纯函数 (编码/签名/解包), executor 只放编排.
符合 AGENTS.md "executor 目录只放 executor 与其单测".
`sdk/auth` 不 import `sdk/cliproxy`, 避免循环.

**复核 (2026-11-06)**: `sdk/auth -> helps` 是**合法**的, 已实测 `go build ./sdk/auth` 通过,
且 `go list -deps` 确认无环 (`sdk/cliproxy/auth` 不依赖 `sdk/auth`; `helps` 不依赖 `sdk/auth`).
因此 `sdk/auth/qoder_models.go` 可直接复用 `helps` 里的 `CosySession`/`QoderEncode`,
不需要"头集回调注入"的绕行方案. research/08 的冲突 #1 结论不成立.

---

## 2. COSY 协议实现

### 2.1 自定义 Base64

```
正向: std = base64(plain); a = len(std)/3
      R = std[n-a:] + std[a:n-a] + std[:a]      (尾/中/首 三段)
      然后按字母表替换: A-Za-z0-9+/ -> 自定义 64 字符, '=' -> '$'
```

实现要点:
- 必须先做**标准** base64 再重排, 不能先重排再编码
- 用 `moego/base64.StdEncoding` 编码, 再手写重排 + 字符映射 (逐字节表, 无分配)
- 反向函数 `qoderDecode` 必须有, 用于测试与调试
- 常量 `qoderAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"`

### 2.2 COSY 签名 (每会话一次 + 每请求一次)

会话级 (创建 `CosySession` 时):
```
tempKey     = 16 位 ASCII (随机)
cosyKey     = base64( RSA-PKCS1v15(tempKey, 官方公钥) )
identity    = JSON 排序紧凑 {
                name, aid, uid, yx_uid, organization_id, organization_name,
                user_type, security_oauth_token, refresh_token
              }
info        = base64( AES-128-CBC(identity, key=tempKey, iv=tempKey) )
```

请求级 (`bearer(body, url)`):
```
path        = url 的路径; 若以 "/algo" 开头则剥掉
payload     = JSON 排序紧凑 {
                cosyVersion: "1.1.75", ideVersion: "", info,
                requestId: uuid4, version: "v1"
              }
payloadB64  = base64(payload)
date        = Unix 秒
raw         = payloadB64 + "\n" + cosyKey + "\n" + date + "\n" + body + "\n" + path
bearer      = "Bearer COSY." + payloadB64 + "." + md5hex(raw)
```

`body` 是**编码后**的 body (即 `qoder_encode` 的输出), 不是明文 JSON.
这是"签名与编码后 body 强绑定"的由来.

### 2.3 头集合

固定头 (顺序不敏感, 但集合固定):
```
cosy-data-policy: agree          content-type: application/json
cosy-business-product: cli       cosy-business-type: agent
cosy-scene: assistant            cosy-clienttype: 5
cosy-date: <date>                cosy-user: <uid>
cosy-key: <cosyKey>              accept: text/event-stream
authorization: <bearer>          accept-encoding: identity
cosy-version: 1.1.75             cosy-machineid: <derived>
cosy-machinetoken: <derived>     cosy-machinetype: <derived>
cosy-clientip: 169.254.198.161   login-version: v2
user-agent: undici               cache-control: no-cache
cosy-machineos: <os>
cosy-machinehostname: <derived>  (仅 sse)
x-model-key: <key>               (仅带模型时)
x-model-source: system           (仅带模型时)
```

设备指纹派生 (纯函数, 无状态):
```
machineId    = md5("machine:" + uid) -> 伪装 UUIDv4 形状 (第13位=4, 第17位=a, 加连字符)
machineType  = md5("machinetype:" + uid) 去连字符取前 18
machineToken = urlsafe_b64(sha512("machinetoken:" + uid)) 去 '=' 取前 43
hostname     = 由 uid 稳定派生, ASCII 安全化
```

### 2.4 黄金向量测试

RSA 加密有随机 padding, **不能比对最终密文**. 可比对的确定性中间量:
1. `qoderEncode(plain)` 输出 — 纯确定
2. `jsonSortedCompact(identity)` 输出 — 纯确定
3. `raw = payloadB64 + "\n" + cosyKey + "\n" + date + "\n" + body + "\n" + path` — 纯确定
4. `md5(raw)` — 纯确定
5. `identity` 明文 — 纯确定 (RSA 之前)
6. `payloadB64` — 若有固定 info 则确定

测试策略: 用固定 uid/nickname/org/token/date/requestId, 注入固定 `tempKey`
(测试缝: `newCosySessionWithKey(uid, ..., tempKey)`), 断言上述 6 个中间量
与 Python 参考实现逐字节一致. 参考值由一次性脚本生成后硬编码进测试.

**必须保留的测试缝**: `tempKey` 与 `requestId` 可注入, 否则测试不可重复.

---

## 3. 请求体构建

模板 59KB 中 74% 是 tools、24% 是 messages, 协议信封只占 2%.
**不移植模板**. 常量内嵌 (见 `research/01-baseprompt-fields.md`).

### 3.1 常量信封

```go
qoderBodyEnvelope = map[string]any{
    "chat_task":    "FREE_INPUT",
    "code_language": "",
    "source":       1,
    "version":      "3",
    "chat_prompt":  "",
    "session_type": "qodercli",
    "agent_id":     "agent_common",
    "task_id":      "common",
    "is_reply":     true,
    "is_retry":     false,
    "image_urls":   nil,
}
```

系统提示词：下游（如 Claude Code CLI 等）100% 自带完整 system prompt。若客户端未传，则使用轻量中性兜底（如 `"You are a helpful assistant."`），彻底抛弃 12.3KB 带有假工具与采集者机器指纹的官方模板。

### 3.2 动态字段

```
request_id, request_set_id        uuid4
chat_record_id                    会话 id 或 uuid4
session_id                        uuid4
stream                            true (恒)
aliyun_user_type                  account.user_type 或默认
model_config                      见下
messages                          [system] + 压平后的轮次
tools                             客户端 tools 或 []
parameters                        max_tokens 默认 32768 + 可选 reasoning_effort
business                          {id, name, begin_at, product, type, stage, model_key, version}
chat_context                      {chatPrompt:"", text:{type:"text",text}, extra:{...}, features:[], imageUrls:null}
```

`model_config` 10 键:
```go
{key, display_name, model:"", format:"openai", is_vl, is_reasoning,
 api_key:"", url:"", source:"system", max_input_tokens}
```

**移植陷阱** (必须显式写, Python 靠模板继承):
- `chat_context.text.type = "text"` 和 `extra.originalContent.type = "text"`
- `parameters.max_tokens` 默认 32768
- `business.model_key` (计费归属, 缺了会被静默归到 auto)

### 3.3 消息压平

```
1. role "developer" -> "system"
2. 首个 system/developer 消息抽为 system_text (后续忽略, 空文本视为未给)
3. user/assistant 原地保留, content 压平为字符串
4. tool 结果: 保留 {role:"tool", tool_call_id, content} (content 恒空串, 不可为 null)
5. assistant 的 tool_calls 原样透传 (arguments 统一为 JSON 字符串)
6. assistant 的 reasoning_content 仅 DeepSeek 族模型保留
```

### 3.4 输出

```go
doc := ojson.NewDoc(nil)
buildQoderBodyDoc(doc, payload, account, modelKey, catalogMeta)
encoded := qoderEncode(doc.Bytes())
```

必须走 `*ojson.Doc` (AGENTS.md 规则), 最终只序列化一次.

---

## 4. SSE 信封解包

### 4.1 上游帧形态

```
data:{"headers":{...},"body":"<内层 OpenAI chunk JSON 字符串>","statusCodeValue":200}
data:{"body":"[DONE]"}
event:finish{...}
```

### 4.2 处理规则与首帧同步探测 (关键防错)

**核心架构要求**: Qoder 上游 HTTP 握手永远是 `HTTP 200 OK`，业务报错与排队全是包在首个 SSE 帧的 JSON 里的 (`statusCodeValue: 403/418/10605`)。
`ExecuteStream` 绝不能在 HTTP 连接建立时就直接返回 `StreamResult, nil`（那会导致 Gin 提前向客户端发 200 且 Conductor 无法换号）。
必须在 `ExecuteStream` 内部**同步读取/窥视首个 SSE 帧**后，再决定是重试、换号还是返回流通道：

```go
// ExecuteStream 伪代码流程
1. 发起 HTTP POST 请求
2. 读取首个 SSE 数据行 (data: ...)
3. 解析外层信封 json，读取 statusCodeValue
4. 若 status == 10605 / isQueued（排队）：
     - 在 executor 内部按 retryAfterSeconds 同步 sleep 并重试（受预算限制）
5. 若 status == 403 且 code in (110, 112)（额度耗尽/每日上限）：
     - 标记 credentialScoped = true，返回 statusErr，触发 Conductor 冷却换号
6. 若首帧 status == 200：
     - 正式启动后台 goroutine 转发后续 SSE 帧，并将 out 通道返回给 Conductor
```

### 4.3 clean_chunk

丢弃空噪声:
```
NOISE_KEYS = {extra_fields, refusal, reasoning_content}
对每个 choice.delta:
  - function_call 若为空 (无 name) 则删
  - tool_calls 若为空数组则删
  - NOISE_KEYS 中值为空的键则删
  - 若 delta 为空且无 finish_reason, 整帧丢弃
```

用 `ojson.Doc` 就地裁剪, 返回 `bool changed`, 未变不重序列化
(参照 `helps.NormalizeOpenCodeToolCallChunkDoc`).

### 4.4 非流式

`Execute` 强制 `opts.Stream = true`, 消费整个流, 用
`helps.AggregateOpenAIStreamChunks` 折叠成 `chat.completion`
(与 CodeBuddy 相同策略).

---

### 4.5 `business.begin_at` 的已知限制

本仓库拿不到参考实现里的 `conversation["begin_at"]` (那是 Python 网关自己的会话登记表),
但该值必须**跨回合恒定**, 否则上游用量面板会把一个 agent 回合拆成多条 `<1min 的对话`,
日上限 (code 110) 被刷爆.

当前实现 (`qoder_body.go` `qoderDerivedBeginAt`): 取 `sha256(sessionID)` 前 8 字节,
映射到固定窗口 `[2026-01-01 - 30d, 2026-01-01]`.

**已知限制**: 该窗口不随真实时间前进, 所有会话的 `begin_at` 永远是历史值.
若上游确实按时间窗口过滤用量统计, 可能导致会话不显示. 这一点**未验证**.

若将来实测确认有问题, 可改为"锚点取当天 00:00 + 派生偏移", 代价是跨天会话的
`begin_at` 会变一次. 不要为了猜测性的问题提前引入每日漂移 — 恒定性的收益
(code 110 风险) 是已验证的.

## 5. 排队与错误处理

### 5.1 错误分类

| 上游信号 | 分类 | 处理 |
|---|---|---|
| 403 + `isQueued`/`10605` + `retryAfterSeconds` | 排队 | executor 内同步等待后重试 |
| 403 + code `112` (额度耗尽) / `110` (每日上限) | 额度/配额超限 | 标记 `credentialScoped = true`，触发账号冷却与轮换（112 冷却 30m，110 冷却至次日 0 点） |
| 429 | 真频控 | 抛 429 + Retry-After |
| 401 | 凭证失效 | 抛 401, conductor 冷却/刷新 |
| `TOKEN_EXPIRE` | session 死亡 | 抛 401, 标记需重新登录 |
| 5xx / 418 / 传输层 | 瞬时 | 同账号短重试 2 次 (1s/2s), 再换号 |
| 其他 4xx | 请求侧确定性错误 | 快速失败, 不重试 |

### 5.2 排队等待

**在 executor 内完成**, 不走 `request-scoped-errors` 配置 (理由见
`QODER_RESEARCH_TODO.md` C1).

预算 (可配置, 默认保守):
```
单轮最长等待   180s
总预算         600s
最多轮数       3
抖动           0..2s
```

超出预算后: 返回实现 `IsRequestScoped() bool` 的错误, conductor 跳过冷却, 客户端自行重试.

**超时约束**: 等待用 `time.NewTimer` + `select ctx.Done()`, **不设** `http.Client.Timeout`
(AGENTS.md 规定上游连接建立后不得设超时). 流读取同理, 只靠 ctx 取消.

### 5.3 瞬时重试与换域

网关候选 (按 realm):
- CN: `gateway.qoder.com.cn` (单主机)
- INTL: `api3.qoder.sh` -> `api2.qoder.sh` -> `api1.qoder.sh`

传输层失败时先换域, 签名只覆盖 path, 换域不影响签名有效性.

---

## 6. 认证

### 6.1 双区常量

| | CN | INTL |
|---|---|---|
| openapi | `https://openapi.qoder.com.cn` | `https://openapi.qoder.sh` |
| gateway | `https://gateway.qoder.com.cn` | `https://api3.qoder.sh` (+2 备) |
| website | `https://qoder.com.cn` | `https://qoder.com` |
| client_id | `1c5e33e1-...` | `e883ade2-...` |
| redirect_uri | `qoder-work-cn://` | `qoder://aicoding.aicoding-agent/login-success` |
| domain | `qoder.com.cn` | `qoder.com` |
| UA | `QoderWork/1.1.75` | `Qoder/1.1.75` |
| nonce 形态 | 带横线 UUID | 32-hex |

### 6.2 登录 (PKCE S256 + 浏览器)

```
1. verifier, challenge = PKCE()
2. nonce = UUID 或 32-hex (按 realm)
3. authURL = <website>/device/selectAccounts?challenge=..&challenge_method=S256&nonce=..
             + 按 realm 追加 redirect_uri/client_id/machine_id
4. 轮询 GET <openapi>/api/v1/deviceToken/poll?nonce=..&verifier=..&challenge_method=S256
   404 或 202 = pending; 成功返回 {token, refresh_token, user_id}
5. GET <openapi>/api/v1/userinfo (Bearer) 补全 name/user_type/organization_id
6. 写 auth 文件
```

**不复用 codebuddy 的 `WaitForAuthorization` 形态**: Qoder 的 poll 是 GET + query 参数,
codebuddy 是 GET + state. 结构相似但不通用.

### 6.3 PAT 导入

```
POST <openapi>/api/v1/jobToken/exchange  {"personal_token": "pt-..."}
-> {token, refresh_token, expires_in}
```

配置 API-key 路径 (`qoder-api-key` / `qoder-intl-api-key`) 与登录导入走同一个 exchange,
但交换时机不同: 登录导入在写入 auth 文件前完成; 配置路径在**请求前**由
`QoderExecutor.PrepareRequestAuth` (`RequestAuthPreparer`) 完成, 因为交换结果是
per-credential 状态, 必须由 auth manager 合并并持久化.

判定条件 (`ShouldPrepareRequestAuth`): 有 `metadata["personal_token"]`, 且
`auth_kind != "oauth"`, 且 (未见 `pat_exchanged` 标记) 且 (`access_token` 缺失 /
等于 `personal_token` / 该 PAT 凭证尚未记录交换产生的 refresh token).
交换成功后写 `pat_exchanged: true`, 后续请求成为 no-op.

失败映射: 401/403 -> credential-scoped (冷却并轮换), 5xx/传输层 -> 502,
其余 4xx 原样透出; 错误消息不回显 token.

### 6.4 刷新按前缀分流 [关键]

```
drt- 开头  -> POST /api/v1/deviceToken/refresh  {refresh_token}
jrt- 开头  -> POST /api/v1/jobToken/refresh     {refresh_token}
有 PAT     -> POST /api/v1/jobToken/exchange    {personal_token}
```

**关键细节 (`expires_in` 毫秒转秒)**: Qoder 服务端下发的 `expires_in` 单位是**毫秒**。解析时必须显式做 `/ 1000`（或判断大于 1e8 则按毫秒转秒），否则过期时间会被算到 80 年后，导致自动刷新循环永不触发。

PAT 永不覆盖活跃的 OAuth 会话, 只做 jrt- 过期后的兜底.

### 6.5 auth 文件 schema

```json
{
  "type": "qoder" | "qoder-intl",
  "auth_kind": "oauth" | "pat",
  "access_token": "...",
  "refresh_token": "...",
  "personal_token": "...",
  "uid": "...",
  "nickname": "...",
  "user_type": "personal_professional_trial",
  "organization_id": "...",
  "organization_name": "...",
  "base_url": "https://gateway.qoder.com.cn",
  "timestamp": 1762345678901,
  "expired": "RFC3339",
  "source": "oauth" | "pat",
  "disabled": false
}
```

**`organization_id` 必须存字符串** (A2: 读路径无 `UseNumber`, 大整数丢精度).
**`type` 必须精确等于 provider key** (合成器靠它推导 provider).

---

## 7. 模型目录 (D3 拉取)

### 7.1 落点: per-auth 探测 (Antigravity 模式)

**不是** 独立 catalog updater. 理由见 `research/04-catalog-fetch-layering.md`:
`catalogUpdater` 完全不可注入, 且钩子签名无法传达"用哪条凭证".

### 7.2 流程

```
Startup / 定时 (每分钟扫描)
  -> 遍历 coreManager.List(), 过滤 Provider in {qoder, qoder-intl}
  -> 每 auth 一个 worker (合并排队, epoch 围栏)
  -> FetchQoderModels(ctx, req)      // sdk/auth, 纯编解码
       签名: COSY (与推理请求同一套)
       GET <gateway>/algo/api/v2/model/list?Encode=1
       body: qoder_encode("{}")
  -> 解析 {"chat": [...]}, 每项全字段
  -> 转换为 []*registry.ModelInfo (见 7.3)
  -> applyExcludedModels / applyModelPrefixes / applyOAuthModelAlias / applyOAuthSettings
  -> reg.ReplaceClientModels(auth.ID, providerKey, expectedEpoch, models)
  -> ReconcileRegistryModelStates + RefreshSchedulerEntry
```

缓存 key 必须含: `auth.ID`, `RegistrationEpoch`, provider, endpoint, `access_token` hash.
token 轮换即失效重建.

冷启动: 从 `auth.Metadata` 读上次成功结果播种 (`PersistMetadataEntry`),
再触发异步探测.

### 7.3 目录项 -> ModelInfo

| Qoder 字段 | ModelInfo |
|---|---|
| `key` | `ID` |
| `display_name` | `DisplayName` |
| `max_input_tokens` | `ContextLength` + `InputTokenLimit` |
| `is_vl` | `SupportedInputModalities` 含 `image` |
| `enable` | **过滤依据, 不进 ModelInfo** |
| `format` | 恒 `openai`, 忽略 |
| `thinking_config` | `Thinking` (见 7.4) |
| — | `Type` = provider key (`qoder` / `qoder-intl`) |
| — | `OwnedBy` = `qoder` |

**`enable == false` 的模型不注册** (INTL 侧 15/17 是 false).

### 7.4 thinking 映射

纯 level-only. **绝不写 `min`/`max`/`dynamic_allowed`/`disable_type`**.

```
levels       = thinking_config.enabled.efforts 的键, 升序
                (Qoder 用 {low, medium, high, xhigh, max}, 是 CLIProxyAPI 词表的完整子集)
zero_allowed = thinking_config.disabled 是否存在
```

**判据是 `thinking_config` 是否存在, 不是 `is_reasoning`**.
`is_reasoning: false` 但有 `thinking_config` 的模型 (performance / qmodel_latest /
qmodel / kmodel_latest / kmodel / dfmodel / mmodel-CN) 必须给非 nil 的 Thinking.

无 `thinking_config` 的模型: `Thinking = nil` (客户端参数被确定性剥离).

**修正 (2026-11-07, 真号实测)**: 上面的规则描述的是**静态快照**, 线上
`GET /algo/api/v2/model/list?Encode=1` 的响应只有 `key` / `display_name` /
`enable` / `is_reasoning` / `is_vl` / `max_input_tokens`, **没有任何
`thinking_config`**. 所以 `thinking_config` 只能来自静态快照.

发布契约改为"静态 seed 打底 + 线上字段增量覆盖"
(`sdk/cliproxy/qoder_models.go` `qoderModelInfoFromEntry` / `qoderModelsFromCatalog`):

```
base = registry.GetQoderModels(provider) 中同 ID 的条目     # 未命中则无 base
Cover(display_name)  : 线上非空才覆盖
Cover(max_input_tokens): 线上 > 0 才覆盖
Cover(is_vl)         : 线上存在才覆盖
Cover(thinking_config): 线上非 nil 才覆盖, 为 nil 时保留 seed 的 Thinking
```

成员集合仍由线上列表 + `enable` 决定; seed 里有而线上没返回的模型不注册.
CN 用 CN seed, INTL 用 INTL seed. 这与参考实现 `qoder2api-hub` 的
`merge_catalog` (`qoder_proxy.py:2729-2778`) 一致, 其注释明确 "None 值不覆盖".

`Thinking = nil` 只发生在 **seed 与线上都没有**该模型的 `thinking_config` 时
(例如线上新出现的未知 key).

`is_default` 无法表达, 接受降级到 CLIProxyAPI 的 medium 推导.

### 7.5 待决策: 严格 vs 钳制

`type: "qoder"` 不在 `isKnownProviderFamily` 中, 因此 OpenAI->OpenAI 走严格校验:
客户端发 `qmodel` (仅 `high`) 不支持的 `low` 会得到 `ErrLevelNotSupported`.
把 `"qoder"` 加进该表则变成钳制到最近支持档.

**默认取严格** (数据准确时更有价值). 若实测客户端越界过多再切换.

---

## 8. Provider 隔离 (D1 的必需配套)

双 provider **不能**隔离同名模型 ID (注册表以 ID 为键合并).
必须配:

```
auth.prefix = "cn" / "intl"
routing.force-model-prefix = true
```

`applyModelPrefixes` 会注册 `<prefix>/<id>` 变体, `rewriteModelForAuth` 请求时剥掉.

副作用范围 (2026-11-06 复核, research/08 D.2): `force-model-prefix` 是**全局开关**,
`applyModelPrefixes` 只在 `prefix == ""` 时提前返回 (`:717-720`), 因此它影响**所有带 prefix
的 auth**, 不只是 qoder. 若用户已有其它带 prefix 的 auth (如 `teamA`), 打开该开关会让
`teamA/<model>` 的无前缀别名消失. 这是此设计的真实 blast radius, 必须在文档与配置注释里写明.

qoder 侧的前提是: `cn` / `intl` 两个前缀由 qoder 独占, 不与现有 provider 冲突.

---

## 9. 接入点清单

### config (A 组)
| 文件 | 改动 |
|---|---|
| `internal/config/config.go:165-166` 旁 | 加 `QoderKey` / `QoderINTLKey` 两个字段 |
| `internal/config/config_types.go` | 加 `QoderKey` / `QoderINTLKey` 类型 (含 `region`? 不需要, provider 名已表达) |
| `internal/config/config_v8.go:67-73` | `v8KeyFamilies` 加两行 |
| `internal/config/config_normalization.go` | 两个 sanitizer |
| `internal/config/config_load.go:312-313` **与** `parse.go:114-129` | **两处都**注册 sanitizer |

### auth
| 文件 | 改动 |
|---|---|
| `internal/auth/qoder/` | 新包 |
| `sdk/auth/qoder.go` | Authenticator |
| `sdk/auth/refresh_registry.go:9-20` | 注册两个 provider 的 refresh lead |
| `sdk/cliproxy/service_auth.go:21-35` | 注册 authenticator |
| `internal/cmd/auth_manager.go:13-28` | **第二个** auth manager, 也要注册 |
| `internal/cmd/qoder_login.go` | CLI 登录命令 |
| `cmd/server/main.go:116,147,663,740,968-976` | flag 五处 |
| `internal/api/handlers/management/auth_files_provider_oauth.go` | `RequestQoderToken` / `RequestQoderINTLToken` |
| `internal/api/handlers/management/auth_files_provider_oauth.go:423-426` | `isIdentitylessOAuthProvider` 加两个 [易漏] |
| `internal/api/handlers/management/auth_files_v8.go:403-428` | 两个 case |
| `internal/api/server_management.go:196-204` | 两条 v0 路由 |
| `internal/api/handlers/management/oauth_sessions.go:645-664` | `NormalizeOAuthProvider` 两个 case |
| `internal/watcher/synthesizer/file.go:238-246` | base_url / auth_kind 注入 |

### executor
| 文件 | 改动 |
|---|---|
| `internal/runtime/executor/qoder_executor.go` | 新文件 |
| `internal/runtime/executor/helps/qoder_*.go` | 新文件 |
| `internal/constant/constant.go:28-29` | 两个常量 |
| `sdk/cliproxy/service_executors.go:211-242` | baseline 加两个 |
| `sdk/cliproxy/service_executors.go:300-369` | switch 加两个 case |

### 模型目录
| 文件 | 改动 |
|---|---|
| `sdk/auth/qoder_models.go` | `FetchQoderModels` |
| `sdk/cliproxy/qoder_models.go` + `qoder_model_probes.go` | 探测与发布 |
| `sdk/cliproxy/service_models.go:91` | 两个 case (模板: antigravity 分支 `:131-147`) |
| `internal/registry/model_definitions.go` | 静态种子共 **4 处** (research/08 E.3): ① `staticModelsJSON` struct 加字段, ② 新增 getter (仿 `GetAntigravityModels`), ③ `GetStaticModelDefinitionsByChannel` 加 `qoder`/`qoder-intl` case, ④ `LookupStaticModelInfo` 的 `allModels` 列表加段. 不加会静默丢弃或管理端点 400 |

**不改**:
- `internal/registry/models/models.json` (不内嵌)
- `internal/registry/model_updater.go` 的 `detectChangedProviders` / `requiredSections`
- `internal/registry/catalog_sources.go` 的 carry-forward
- `internal/thinking/validate.go` 的 `isKnownProviderFamily` (默认严格)
- `sdk/cliproxy/auth/conductor_execution.go` 的 `requestToFormat` (default 已是 OpenAI)

### usage
| 文件 | 改动 |
|---|---|
| `sdk/cliproxy/usage/accounting.go:358` | marker 列表加 `"qoder"` (覆盖两区, 子串匹配) |

---

## 10. 分阶段

| 阶段 | 内容 | 可独立验证 |
|---|---|---|
| P0 | `helps/qoder_cosy.go` + 黄金向量测试 | 与 Python 逐字节比对 |
| P1 | `helps/qoder_body.go` + `qoder_stream.go` + 单元测试 | 给定 payload 产出期望 JSON; 给定帧序列产出期望 chunk |
| P2 | `qoder_executor.go` + 注册 | `go build` + mock 上游端到端 |
| P3 | `internal/auth/qoder` + `sdk/auth/qoder.go` + 管理 API + CLI | 真实账号登录 |
| P4 | 目录探测 + thinking 映射 + prefix | `/v1/models` 正确 |
| P5 | config 双段 + sanitizer + 管理前端路由 | v8 保存往返 |
| P6 | 排队重试 + 错误分类 | 构造 403 响应验证 |

每阶段结束都要 `go build ./cmd/server` 并跑相关测试.

---

## 11. 风险与开放问题

| # | 风险 | 缓解 |
|---|---|---|
| R1 | COSY 常量 (版本号/公钥/字母表) 随官方升级失效 | 集中在一个文件, 便于替换; 黄金向量测试会立即报警 |
| R2 | 上游 `enable` 语义变化 | 按账号过滤, 不缓存跨账号 |
| R3 | 排队等待占用 goroutine | 有预算上限; ctx 取消即退出 |
| R4 | 未知协议字段 (`chat_task` 等 9 个) 是否需要 | 保守保留; 后续 A/B 再精简 |
| R5 | 12.3KB 系统提示词是否触发内容审核 | 保留原样 (官方客户端同样使用); 若被拒再做中性替换 |
| Q1 | 严格 vs 钳制 thinking | 默认严格 |
| Q2 | 是否需要 Anthropic 入口特殊处理 | 暂不需要, 走通用 translator |
| Q3 | 是否需要 TLS 指纹 | 先不做; 被封再评估 |
