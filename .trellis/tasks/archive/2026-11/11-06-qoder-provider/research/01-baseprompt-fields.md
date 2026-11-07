# Qoder 上游请求体: 模板拆解与最小可行形状

来源: 只读分析 `E:/docs/github_repo/qoder2api-hub`, 2026-11-06.
结论用于 Go 移植, 不以本仓库 `.trellis` 之外的代码为准.

## 模板利用率

`baseprompt.json` 59643 字节, 23 个顶层键:

| 部分 | 字节 | 占比 | 是否上线 |
|---|---|---|---|
| `tools` (14 个官方 agent 工具) | 44239 | 74.2% | **从不**. `build_qoder_body` 无条件覆盖 |
| `messages` (12.3KB system + 2 个空 user) | 14181 | 23.8% | 部分. 只保留 system, 且客户端给 system 时被替换 |
| 21 个协议标量键 | 1194 | 2.0% | 全部 |

结论: **不需要移植模板**. 协议信封约 1KB 常量, 其余按逻辑生成.

## tools 被无条件丢弃

`qoder_proxy.py:6351`:

```python
body["tools"] = payload.get("tools") or []
```

无分支、无环境变量开关. `normalize_tools` (`:6356` -> `:3978-4004`) 只处理已替换后的列表.
所有 33 个 `open_upstream` 调用点最终都走同一函数, 包括客户端不传 tools 时 (结果是 `[]`).

副作用: 上游**不要求**这 14 个工具存在, 也不要求 `tools` 非空.

## system prompt 不是协议必需

`qoder_proxy.py:6239-6253` 的分支:

```python
tmpl_system = [m for m in (body.get("messages") or [])
               if (m or {}).get("role") == "system"]
if system_text is None:
    if SYSTEM_PROMPT and SYSTEM_PROMPT != DEFAULT_SYSTEM_PROMPT:
        system_text = SYSTEM_PROMPT
    else:
        body["messages"] = tmpl_system
if system_text is not None:
    body["messages"] = [{"role": "system", "content": system_text}]
body["messages"].extend(flat)
```

- 客户端给 system -> 整体替换 (模板 system 上的 `response_meta` / `reasoning_content_signature` 一并丢弃, 生产环境两者并存, 故非必需)
- 客户端没给 + 未传 `--system-prompt` -> 保留模板 12.3KB 人设
- 只取**第一条** system/developer; 后续被忽略; 空文本视为未给 (见 `flatten_messages:6069-6072`)
- 模板自带的 2 个 user 轮次总是被角色过滤丢弃 (`:6241-6242`)

Go 移植决策: 保留 12.3KB 人设作为默认值 (维持 Qoder agent 行为), 客户端给 system 则替换.
不要把 12.3KB 硬编码进 Go 源码; 用 `go:embed` 放一个文本文件.

## 模板缺失时的降级行为

`BASEPROMPT` 加载失败得到 `{}` (`:336-342`), 只打日志 `baseprompt.json MISSING` (`:13321-13322`),
构建器仍能产出请求体. 作者未对这条路径做过 A/B.

移植含义: 说明协议**容忍**缺字段, 但容忍不等于正确. 不要依赖这条路径.

## 顶层字段处置表

`build_qoder_body` (`:6212-6382`) 内部**没有**任何 `del` / `pop`, 只有覆写与合并.

| 字段 | 模板值 | 处置 | 行号 |
|---|---|---|---|
| `request_id` | uuid4 | 覆写 | 6267-6268 |
| `request_set_id` | uuid4 | 覆写 | 6273 |
| `chat_record_id` | uuid4 | 覆写为会话 id 或同 request_id | 6272 |
| `stream` | `true` | 硬编码 true | 6275 |
| `chat_task` | `"FREE_INPUT"` | **不动** | — |
| `chat_context` | dict | 合并改写 | 6305-6322 |
| `image_urls` | `null` | 默认不动 | 6315-6321 |
| `is_reply` | `true` | **不动** | — |
| `is_retry` | `false` | **不动** | — |
| `session_id` | uuid4 | 覆写两次 (缓存亲和) | 6274, 2519/2533 |
| `code_language` | `""` | **不动** | — |
| `source` | `1` | **不动** | — |
| `version` | `"3"` | **不动** | — |
| `chat_prompt` | `""` | **不动** | — |
| `parameters` | dict | 合并 | 6325-6347 |
| `aliyun_user_type` | 账号值 | 覆写 | 6277-6278, 8030 |
| `session_type` | `"qodercli"` | **不动** | — |
| `agent_id` | `"agent_common"` | 硬编码覆写 | 6276 |
| `task_id` | `"common"` | **不动** | — |
| `model_config` | 10 键 | 全部覆写 | 6283-6302 |
| `messages` | list | 整体重建 | 6241-6253 |
| `tools` | 14 工具 | 整体替换 | 6351, 6356 |
| `business` | dict | 合并 | 6359-6381 |

## 字段集差异 (移植必读)

构建器写入而模板没有的:
- 顶层: **只有 `business.model_key`** (计费归属, 见 `CHANGELOG.md:280` 与 `tests/_test_business_model_key.py:54-95`)
- 嵌套: `parameters.reasoning_effort`; `chat_context.extra.modelConfig` 从 2 键扩展为 10 键; `chat_context.text.text` / `extra.originalContent.text`

模板有而构建器从不写的 (纯继承):
- 顶层: `chat_task`, `source`, `version`, `session_type`, `task_id`, `is_reply`, `is_retry`, `code_language`, `chat_prompt`, `image_urls`
- 嵌套: `chat_context.chatPrompt` (`""`), `chat_context.features` (`[]`), `chat_context.extra.context` (`[]`),
  以及 `chat_context.text.type` / `extra.originalContent.type` 两个 `"text"` 判别符
- `business.product` / `type` / `stage` 用 `setdefault`, 值与模板相同, 等效继承常量
- `parameters.max_tokens = 32768` 在客户端不传时继承

**移植陷阱**: Python 的 `txt = cc.get("text") or {}` (`:6306`) 不写 `type`.
Go 版本若不从模板继承, 必须自己补 `"type": "text"`, 否则 `chat_context.text` 结构不完整.

**行为耦合**: `parameters.max_tokens` 默认 32768 来自模板. Go 版若省略 `parameters`, 上游截断行为改变.
必须显式写 `32768` 作为默认值.

## 必需 vs 装饰

| 字段 | 判定 | 依据 |
|---|---|---|
| `messages` | 必需 | 6241-6253, 总是重建 |
| `model_config.key` | 必需 | 同 key 进 `x-model-key` 头 (`qoder_sign.py:679`) |
| `model_config.{display_name,is_vl,is_reasoning,max_input_tokens}` | 功能性 | 6283-6302; 上下文窗口覆盖 6294-6299 |
| `business.model_key` | 计费必需 | `CHANGELOG.md:280` |
| `stream` | 必需 (恒 true) | 6275; 下游全部假设 SSE |
| `session_id` | 缓存亲和 | 6274, 2519/2533 |
| `request_id` / `request_set_id` | 计费与限额计数 | `CHANGELOG.md:156,198` |
| `chat_record_id` / `business.id` / `business.begin_at` | 用量面板归组, 非正确性 | 6269-6272, 6360-6368. **三者必须同会话恒定**, 见下方稳定性契约 |
| `agent_id` | 大概率必需, 且镜像到 URL query `AgentId=agent_common` | 6276, 330-331 |
| `chat_task`/`source`/`version`/`session_type`/`task_id`/`is_reply`/`is_retry`/`code_language`/`chat_prompt` | 协议样枚举, **未经验证** | 仅存在于模板, 全仓零其他命中 |
| `chat_context` 整体结构 | 大概率必需 | 6305-6322 |
| `chat_context.chatPrompt`/`features`/`extra.context`/`image_urls` | 装饰, 可省 | 6305-6321 |
| `parameters.max_tokens` | 可选输入, 但默认值 32768 | 6326-6337 |
| `parameters.reasoning_effort` | 可选, 受模型门控 | 6339-6346 |
| `business.name`/`product`/`type`/`stage` | 装饰 | 6369-6372 |
| `aliyun_user_type` | 身份相关, 影响配额档位 | 6277, 8030; 默认 `personal_professional_trial` |

### `business.id` / `business.begin_at` 的稳定性契约 (实现裁决, 2026-11-06)

参考实现 (`qoder_proxy.py:6363-6368`):

```python
if conversation:
    biz["id"] = conversation["id"]
    biz["begin_at"] = conversation["begin_at"]    # 会话开始时间, 跨回合不变
else:
    biz["id"] = str(uuid.uuid4())
    biz["begin_at"] = int(time.time() * 1000)
```

`conversation` 来自 Python 网关自己的会话登记表, 本仓库没有这个东西.
上游用量面板按 `business.id` 归组、按 `business.begin_at` 排序, 两者每回合变化会把
一个 agent 回合拆成几十条 `<1min 的对话`, 日上限 (code 110) 迅速刷爆
(参考实现 `:6359-6362` 的注释就是这条成因).

Go 实现因此约定:

- `sessionID` 非空 -> `business.id = sessionID`, `business.begin_at` 由
  **`sessionID` 的 sha256 前 8 字节**映射到固定窗口 `[anchor-30d, anchor]`
  (`anchor = 2026-01-01T00:00:00Z`), 与 `sessionID` 一一对应且**不读时钟**.
  绝对值本身只是归组键, 不与参考实现的具体值对齐, 稳定性与相对顺序才是要求.
  锚点必须固定: 若窗口上界取 `now`, 该值会随回合漂移, 稳定性就没了.
- `sessionID` 为空 -> `business.id` 用新的 uuid4, `business.begin_at` 取当前毫秒时间戳
  (请求自成一体, "现在" 就是正确答案).

测试锁定点 (`internal/runtime/executor/helps/qoder_body_test.go`):
`TestQoderBodyBusinessBeginAt` 覆盖同会话两次构建相等、跨时钟跳变相等、
不同会话不相等、值落在窗口内、空会话走时钟区间.

## 最小可行请求体 (Go 实现目标)

Tier 1 - 协议信封常量 (全部来自模板, 约 1KB):

```
request_id:        uuid4
request_set_id:    uuid4
chat_record_id:    会话 id 或 uuid4
stream:            true
chat_task:         "FREE_INPUT"
session_id:        uuid4
code_language:     ""
source:            1
version:           "3"
chat_prompt:       ""
aliyun_user_type:  账号 user_type 或 "personal_professional_trial"
session_type:      "qodercli"
agent_id:          "agent_common"
task_id:           "common"
is_reply:          true
is_retry:          false
```

Tier 2 - 计算得出:

```
messages:      [{role:"system", content:<客户端 system | 嵌入的 12.3KB 人设>}] + 压平后的轮次
tools:         []  或客户端 tools (已是 tools[].function 嵌套形态)
model_config:  {key, display_name, model:"", format:"openai", is_vl, is_reasoning,
                api_key:"", url:"", source:"system", max_input_tokens}
chat_context:  {chatPrompt:"", text:{type:"text", text:prompt},
                extra:{context:[], originalContent:{type:"text", text:prompt},
                       modelConfig:<model_config 的副本>},
                features:[], imageUrls:null}
parameters:    {max_tokens: <客户端值 | 32768>, reasoning_effort?: <门控后>}
business:      {id, name: prompt 前 30 字或 "chat", begin_at: 毫秒时间戳,
                product:"cli", type:"agent", stage:"start",
                model_key:<上游 key>, version:<COSY_VERSION>}
image_urls:    null
```

进一步瘦身的安全顺序 (已证冗余): `image_urls` -> `chat_context.imageUrls` -> `chat_context.chatPrompt`/`features`/`extra.context`.

**不要**在没有真实 A/B 的情况下删 `chat_task`/`source`/`version`/`session_type`/`task_id`/`is_reply`/`agent_id`.
**不要**删 `chat_context.text.type`.
**不要**省略 `parameters.max_tokens` 的 32768 默认值.
