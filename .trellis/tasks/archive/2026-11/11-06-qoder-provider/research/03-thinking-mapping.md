# Qoder thinking_config -> registry.ThinkingSupport 映射

来源: 只读分析, 2026-11-06. 结论基于两份 catalog 全量扫描 + CLIProxyAPI 代码走查.

> **修正 (2026-11-07, 真号实测)**: 下文第五节表格的数据来自静态快照, 取自
> `qoder_catalog_cn.json` / `qoder_catalog_intl.json`. 真实账号线上
> `GET /algo/api/v2/model/list?Encode=1` 的响应**不含 `thinking_config`**, 也不含
> `context_config` / `description`: 每个 model 只有 `key` / `display_name` /
> `enable` / `is_reasoning` / `is_vl` / `max_input_tokens` 六个字段.
>
> 因此线上 catalog 不能直接作为 Thinking 的来源. 现在的契约是"静态快照打底 +
> 线上字段增量覆盖" (`sdk/cliproxy/qoder_models.go` `qoderModelInfoFromEntry`):
>
> - 基础元数据 (Thinking levels / zero_allowed、display_name、context、modalities)
>   取自同 provider 的静态 seed (`registry.GetQoderModels(provider)`).
> - 线上字段仅在其"确实存在"时覆盖: `display_name` 非空、`max_input_tokens > 0`、
>   `is_vl` 存在、`thinking_config` 非 nil.
> - `thinking_config` 为 nil 时**保留静态 seed 的 Thinking**, 不置空. 参考实现
>   `qoder2api-hub` 的 `merge_catalog` (`qoder_proxy.py:2729-2778`) 是同一策略:
>   "None 值不覆盖", 避免动态接口的 null 抹掉静态快照里的真实值.
> - 成员集合仍由线上列表 + `enable` 决定; seed 里有而线上没返回的模型**不注册**.
>
> 下文第四节 "nil Thinking 的语义" 因此只适用于 **seed 与线上都没有**的模型
> (例如线上新出现的未知 key).

## 一、ThinkingSupport 结构

`internal/registry/model_registry.go:142-159`:

```go
type ThinkingSupport struct {
    Min            int      `json:"min,omitempty"`
    Max            int      `json:"max,omitempty"`
    ZeroAllowed    bool     `json:"zero_allowed,omitempty"`
    DynamicAllowed bool     `json:"dynamic_allowed,omitempty"`
    DisableType    string   `json:"disable_type,omitempty"`
    Levels         []string `json:"levels,omitempty"`
}
```

能力判定 `internal/thinking/convert.go:162-183`:

| Min/Max | Levels | 能力 | 后果 |
|---|---|---|---|
| 均 0 | 非空 | LevelOnly | budget->level 转换 + 钳制; level 白名单生效 |
| >0 | 空 | BudgetOnly | level->budget 转换; budget 范围校验 |
| >0 | 非空 | Hybrid | 保留原格式 |
| 均 0 | 空 / 结构为 nil | None | 无转换, 无白名单 |

## 二、Qoder 是纯 level-only (关键结论)

**两份 catalog 的 `thinking_config` 内没有任何数值字段**. 唯一的数字是 `max_input_tokens`
与 `context_config.*.token_count`, 那些是上下文窗口, 与思考预算无关.

上游只接受 `parameters.reasoning_effort` (`qoder_proxy.py:6341-6346`), Anthropic 桥
只做 `budget_tokens` -> effort 的单向转换 (`qoder_anthropic.py:228-270`).

结论: **只写 `levels` + `zero_allowed`, 绝不写 `min`/`max`/`dynamic_allowed`/`disable_type`**.
写了数值会让模型变成 BudgetOnly/Hybrid, 一个纯 level 模型就会被钳进 token 预算.

## 三、字段映射规则

| Qoder 字段 | 目标 | 规则 |
|---|---|---|
| `thinking_config.enabled.efforts` 的键 | `levels` | 原样拷贝, 升序 |
| `thinking_config.disabled` **存在** | `zero_allowed: true` | catalog 明确提供关闭态 |
| `thinking_config.disabled` **不存在** | `zero_allowed: false` | 该模型无法关闭思考 |
| `efforts.<x>.is_default` | **无对应** | ThinkingSupport 无默认字段 |
| `is_reasoning` | **不要用作判据** | 见第四节 |

### 词表完全对齐

CLIProxyAPI 标准档位 `internal/thinking/validate.go:255-256`:
`minimal, low, medium, high, xhigh, max`; 特殊值 `none`, `auto`.
suffix 解析器接受 `minimal|low|medium|high|xhigh|max` + `none|auto|-1` (`suffix.go:99-113`).

Qoder 实际出现的键: `{low, medium, high, xhigh, max}` — **全部可直接识别, 无需翻译**.
`minimal` 从未出现; `none`/`auto` 不作为 effort 键出现 (由 `disabled` 块表达).

## 四、`is_reasoning` 不能当判据 [重要]

(本节及下节的分析对象是**静态快照**; 线上响应见文首修正. 结论仍然成立: 判据是
`thinking_config` 是否存在, 而不是 `is_reasoning`.)

INTL catalog 里两个集合**不是**同一个:

**`is_reasoning: false` 但有 `thinking_config`** (必须给非 nil 的 Thinking):
`performance`, `qmodel_latest`, `qmodel`, `kmodel_latest`, `kmodel`.
CN 额外: `dfmodel`, `mmodel` (CN 版).

**`is_reasoning: false` 且无 `thinking_config`** (真正无思考):
INTL: `auto`, `efficient`, `mmodel`. CN 额外: `q37fmodel`.

若按 `is_reasoning` 门控, 会把**可用的档位控制从 5 个模型上剥掉**.
**正确判据: `thinking_config` 是否存在.**

### nil Thinking 的语义

`internal/thinking/apply.go:355-377`: `Thinking == nil` 时, 若请求带 `reasoning_effort`/`reasoning`,
会 `stripThinkingFieldsDoc` 剥掉并返回; 若没带则透传.
openai 格式的剥离目标见 `internal/thinking/strip.go:75-80`.

结论: 无 `thinking_config` 的模型 **emit `thinking: nil`** (省略该键), 客户端参数被确定性剥离.
不要用 `"thinking": {}` — 那会分配一个全零的 ThinkingSupport, 能力 None 但不剥离,
导致不支持的参数泄漏到上游.

## 五、逐模型映射表 (INTL, 17 项, 源自静态快照)

| key | efforts | disabled | is_reasoning | emit thinking |
|---|---|---|---|---|
| `auto` | — | — | false | `nil` |
| `ultimate` | low, medium, high, xhigh, max | 有 | true | `{zero_allowed:true, levels:[low,medium,high,xhigh,max]}` |
| `performance` | 同上 | 有 | **false** | 同上 |
| `efficient` | — | — | false | `nil` |
| `smodel` | all 5 | **无** | true | `{zero_allowed:false, levels:[low,medium,high,xhigh,max]}` |
| `cmodel` | all 5 | **无** | true | 同上 |
| `qmodel_38max` | low, medium, xhigh | 有 | true | `{zero_allowed:true, levels:[low,medium,xhigh]}` |
| `qfmodel` | low, medium, xhigh | 有 | true | 同上 |
| `qmodel_latest` | **无** | 有 | false | `{zero_allowed:true, levels:[high]}` |
| `qmodel` | **无** | 有 | false | 同上 |
| `kmodel_latest` | low, high, max | **无** | false | `{zero_allowed:false, levels:[low,high,max]}` |
| `kmodel` | low, high, max | **无** | false | 同上 |
| `gmodel` | low, high, max | **无** | true | 同上 |
| `gfmodel` | high, max | **无** | true | `{zero_allowed:false, levels:[high,max]}` |
| `dmodel` | high, max | 有 | true | `{zero_allowed:true, levels:[high,max]}` |
| `dfmodel` | low, high, max | 有 | true | `{zero_allowed:true, levels:[low,high,max]}` |
| `mmodel` | — | — | false | `nil` |

CN 额外: `q37fmodel` -> `nil`; `gm51model` -> `{zero_allowed:true, levels:[high,max]}`.

### `qmodel` / `qmodel_latest` 的 `levels:["high"]` 是表达手段, 不是数据

**数据事实** (2026-11-06 复核两份 catalog): 这两个模型的
`thinking_config.enabled` 存在但**没有 `efforts` 键**, 同时 `disabled` 存在.
上游没有提供任何档位表, 所以 `levels:["high"]` **不是从上游数据读出来的**.

参考实现遇到这种形态时 (`qoder_proxy.py:3690-3695`) 的行为是:

```python
if not sup:                      # 有 thinking_config 但无档位表
    return None, "dropped (model has no effort levels; use none to disable)"
```

即"除 `none` 外一律不下发 `reasoning_effort`".

本仓库无法用 `ThinkingSupport` 直接表达"只能开/关":
- `levels: []` -> `detectModelCapability` 返回 `CapabilityNone`
  (`internal/thinking/convert.go:162-183`), 此时 `stripThinkingFieldsDoc` 只在
  `Thinking == nil` 时剥离; `Thinking` 非 nil 但 Levels 为空会让客户端任意字符串
  **原样透传**, 比参考实现更差.
- `levels:["high"]` + `zero_allowed:true` -> `CapabilityLevelOnly`, 白名单生效.

**保留 `levels:["high"]` 的理由 (行为等价性, 不是数据保真)**:

| 客户端请求 | 本仓库行为 | 上游结果 | 参考实现行为 | 上游结果 |
|---|---|---|---|---|
| `thinking: none` | `ModeNone` -> 下发 `"none"` | 关闭思考 | 下发 `"none"` | 关闭思考 |
| `thinking: high` | 白名单命中 -> 下发 `"high"` | 忽略, 回落默认档 | 不下发 | 回落默认档 |
| `thinking: medium` | 严格模式 -> `ErrLevelNotSupported` | 客户端得到明确错误 | 不下发 | 回落默认档 |

差异只在第三行: 本仓库对越界档位报错而非静默丢弃. 这是 D6 "严格校验" 决策的直接后果,
已在 PRD 记录. 上游对不认识的档位静默忽略, 所以前两行与参考实现**结果等价**.

**不要**写成 `levels: []` — 那会放行任意客户端字符串.

## 六、JSON/YAML 输出形态

`models.json` 风格 (需新增 struct 字段, 见第七节):

```json
{
  "id": "qfmodel",
  "object": "model",
  "owned_by": "qoder",
  "type": "qoder",
  "display_name": "Qwen3.8-Flash",
  "context_length": 180000,
  "thinking": {
    "zero_allowed": true,
    "levels": ["low", "medium", "xhigh"]
  }
}
```

JSON 键名确认 (`model_registry.go:146-158`): `min`, `max`, `zero_allowed`,
`dynamic_allowed`, `disable_type`, `levels`.
config.yaml 用 `zero-allowed` / `dynamic-allowed` / `disable-type`.

现有同类样板:
- `models.json:4768-4773` codebuddy `hy4free`: `{zero_allowed:false, levels:["high"]}`
- `models.json:4816-4823` codebuddy `hy3`: `{zero_allowed:true, levels:[low,medium,high]}`
- `models.json:3756-3762` kimi `kimi-k2-thinking`: `{zero_allowed:true, levels:[low,high]}`

## 七、实现注意事项

1. **未知 catalog 段会被静默丢弃**. `staticModelsJSON` (`model_definitions.go:29-45`) 没有
   `qoder` 字段, `json.Unmarshal` 忽略未知键. 只往 models.json 加段而不加结构体字段 +
   `GetStaticModelDefinitionsByChannel` case (`:575-605`) + `LookupStaticModelInfo` 列表 (`:644-657`),
   模型永远不会注册.

2. **models.json 不做归一化, config 做**. `NormalizeThinkingSupport`
   (`internal/modelconfig/model_info.go:36-61`) 把 `levels` 里的 `"none"` 提升为 `ZeroAllowed`,
   `"auto"` 提升为 `DynamicAllowed`, 但只对 config-file 模型运行
   (`sdk/cliproxy/service_models.go:893, :947`). 静态 catalog 里 `zero_allowed` 必须显式写.

3. **严格还是钳制取决于 `modelInfo.Type`**. `type: "qoder"` 不在 `isKnownProviderFamily`
   (`validate.go:426-433`), 且 OpenAI->OpenAI 同族 -> `strictBudget=true`,
   `allowClampUnsupported=false` (`validate.go:77,83`).
   客户端发不支持的档位会得到 `ErrLevelNotSupported` 而不是被钳制.
   对 `qmodel`(仅 high) 来说, 任何 low/medium 请求都会报错.
   **决策点**: 是否把 `"qoder"` 加进 `isKnownProviderFamily` 换取钳制语义.

4. **无 disabled 的模型如何关闭**. `zero_allowed:false` + levels 无 `"none"` 时,
   `thinking: none` 被静默提升到最低档 (`validate.go:198-203`).
   这与 Qoder catalog 一致 (无 disabled 块 = 无关闭开关).

5. **`is_default` 无法表达**. Qoder 各模型的默认档 (high/medium/xhigh/max 各异) 无法编码,
   客户端会落到 CLIProxyAPI 自己的 `convertAutoToMidRange` (medium, `validate.go:221-231`).
   若需保真, 只能给 ThinkingSupport 加字段 — 当前没有.
