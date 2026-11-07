# Qoder 目录探测实现调研 (P4)

- **Query**: P4 model catalog - upstream payload shape, Antigravity clone baseline, ownership/state, provider isolation, static seeds
- **Scope**: internal (CLIProxyAPI) + external (qoder2api-hub reference read-only)
- **Date**: 2026-11-06

参考实现只读路径: `E:/docs/github_repo/qoder2api-hub/`.

---

## A. 上游目录 payload 形状

### A.1 顶层结构: `{"chat": [...]}`

`qoder_proxy.py:3068`:

```python
chat = payload.get("chat") or []
```

顶层只读这一个键。同文件的 `read_local_models` (`:2830`) 解密本机客户端缓存后也是同一句
`plain.get("chat") or []`, `_refresh_catalog.py:75` 亦然 —— 动态接口 / 本机缓存 / 快照
三源共用同一顶层形状。

**派生量**: 顶层没有分页字段、没有 `total` / `next` / `cursor` 的读取。
`read_dynamic_models` (`:3036-3085`) 只做一次请求, 读完 `chat` 数组即返回。
整个仓库对 `cursor` 的引用只有 `qoder_accounts.py:3612,3906,3921` (账号轮询游标) 与
`qoder_proxy.py:2107-2112` (日期游标), 与目录拉取无关。

三级降级链 (`fetch_models`, `qoder_proxy.py:2969-3015`):
1. `read_dynamic_models` (网络, 需账号)
2. `_load_models_cache` (磁盘 `accounts/models-cache-<realm>.json`, TTL 7 天)
3. `read_local_models` (本机客户端 catalog-v7/v6 解密)
4. `qoder_catalog.STATIC_*_MODELS` (内嵌快照)

### A.2 单条目的字段清单

以两份 catalog JSON 全量统计 (`qoder_catalog_intl.json` 17 条, `qoder_catalog_cn.json` 14 条)
加 `model_entry()` (`qoder_proxy.py:3129-3349`) 与 `merge_catalog()` (`:2729-2778`) 的读取点。

| 字段 | 类型 | 出现率 (intl / cn) | 可缺 | 引用方默认值 |
|---|---|---|---|---|
| `key` | string | 17/17, 14/14 | 否 (缺失即丢弃条目) | `read_dynamic_models` `:3073-3075` `if not mid: continue` |
| `display_name` | string | 17/17, 14/14 | 是 | `:3146` `meta.get("name") or meta.get("display_name") or mid`; `:3080` 回填为 key |
| `enable` | bool | 17/17, 14/14 | 是 | `:3153` `meta.get("enable", True) is not False` (缺失视为 true) |
| `format` | string | 17/17, 14/14 | 是 | 恒为 `"openai"`; 不被消费 (仅在 `:6288` 写出请求时硬编码 `"openai"`) |
| `source` | string | 17/17, 14/14 | 是 | 恒为 `"system"`; 不被消费 |
| `is_vl` | bool | 17/17, 14/14 | 是 | `:3201` `bool(meta.get("is_vl") or meta.get("supportsImages"))` -> false |
| `is_reasoning` | bool | 17/17, 14/14 | 是 | `:3206` 仅用于展示层 `supports_reasoning`; **不是 thinking 判据** |
| `is_default` | bool | 17/17, 14/14 | 是 | 条目级默认档标记; 只在 `:3239` (context_config) 与 efforts 内层被读 |
| `price_factor` | float | 17/17, 14/14 | 是 | `:3286` `if price_now is not None` 才下发 |
| `max_input_tokens` | int | 16/17, 14/14 | **是** (intl `auto` 缺) | `:3228` `meta.get("maxInputTokens") or meta.get("max_input_tokens")`; 请求侧 `:6294` 缺省 180000 |
| `context_config` | object | 15/17, 13/14 | 是 | `:3229-3230` 非 dict 视为空 -> 不下发多窗口 |
| `thinking_config` | object | 14/17, 11/14 | 是 | `:3204-3205` 非 dict 视为 `{}` |
| `strategies` | array | 13/17, 0/14 | 是 | `:3194-3200` 只在 `enabled=false` 时读第一个 `disabled_message_key` |
| `is_new` | bool | 13/17, 13/14 | 是 | `:3343` `if meta.get("is_new")` |
| `is_free` | bool | 11/17, 4/14 | 是 | `:3341` `if meta.get("is_free") is not None` |
| `minimal_version` | object | 8/17, 10/14 | 是 | **零消费** (`grep '"minimal_version"' qoder_proxy.py` = 0) |
| `is_sensitive` | bool | 6/17, 9/14 | 是 | **零消费** |
| `promotion` | object | 3/17, 3/14 | 是 | `:3287` 峰谷价, 见 A.6 |
| `original_price_factor` | float | 2/17, 1/14 | 是 | `:3293` 峰价兜底 |
| `icon` | string | 0/17, 11/14 | 是 | `:3345` `if meta.get("icon")` |
| `is_editable` | bool | 15/17, 11/14 | 是 | **零消费** |
| `credits` | any | 0/17, 0/14 | 是 | `:3347` `if meta.get("credits")`。**catalog 与动态响应均未出现该键**, 只有代码留了口 |
| `description` | string | 0/17, 0/14 | 是 | `:3168` 缺则回落到 `official_description(mid)` (客户端文案表) |

**`supportsToolCall` / `supportsImages` / `supportsReasoning` / `maxInputTokens` /
`maxOutputTokens` / `disabledMultimodal` / `name`** 是 `model_entry()` 里的兼容别名,
不在官方 catalog 中。`merge_catalog` `:2754` 显式跳过 `None` 值, 防止动态响应用 null
覆盖静态快照。

**对 P4 的直接影响**: Qoder 的 ModelInfo 转换只应消费
`key` / `display_name` / `max_input_tokens` / `is_vl` / `thinking_config` / `enable`,
其余字段 (price/promotion/icon/strategies/is_*) 在本仓库没有对应落点。

### A.3 `thinking_config` 精确形状

结构 (`qoder_catalog.py:109-126`):

```json
"thinking_config": {
  "disabled": {"description": "Disable thinking"},
  "enabled": {
    "description": "Enable thinking",
    "efforts": {
      "xhigh": {},
      "high": {"is_default": true},
      "low": {},
      "max": {},
      "medium": {}
    },
    "is_default": true
  }
}
```

实测分类 (全量扫描两份 catalog):

| 形态 | 出现 |
|---|---|
| `enabled` 是 object 且 `efforts` 非空 | ultimate, performance, smodel, cmodel, qmodel_38max, qfmodel, kmodel_latest, kmodel, gmodel, gfmodel, dmodel, dfmodel, gm51model |
| `enabled` 是 object 但**无 `efforts` 键** | qmodel_latest, qmodel (intl + cn) |
| `disabled` 存在, 值为 **object** (`{}` 或 `{"description":...}`) | ultimate, performance, qmodel_38max, qfmodel, qmodel_latest, qmodel, dmodel, dfmodel, gm51model |
| `disabled` **不存在** | smodel, cmodel, kmodel_latest, kmodel, gmodel, gfmodel |
| `thinking_config` **完全缺失** | auto, efficient, mmodel (intl); auto, q37fmodel, mmodel (cn) |

关键点:
- `disabled` **从来不是 bool**, 而是 object (可为空对象 `{}`)。判据必须是"键是否存在",
  不能读值真假 —— `qmodel_38max` 的 `"disabled": {}` 是空 dict, 但语义是"可关闭"。
  参考实现的判据是 `if "disabled" in thinking_cfg` (`:3273`), 即键存在性。
- `efforts` 内部键的值是可空 object, 只读 `is_default` (`:3270`, `:3659`)。
- `enabled.is_default` 在全部样本中恒为 `true`, 不携带信息。
- `efforts` 永远是 `enabled` 的子键, 不是 `thinking_config` 的直接子键。

`is_default` 的消费点只有两处: `default_effort()` (`:3654-3665`, 在 efforts 里找
`is_default=true`) 与 `model_entry` 的 `:3270`。**`ThinkingSupport` 无对应字段**
(见 `03-thinking-mapping.md` 第五节)。

### A.4 `enable` 是 per-account 还是 global

**结论: `enable` 跟随账号/套餐, 不是全局常量; 但参考实现只在两处用它, 且都不在目录拉取主链路上。**

证据:

1. `qoder_catalog.py:16` 的模块 docstring 原文:
   "`enable` 该账号/套餐当前是否开放（展示为徽章，**不过滤**）"
2. `merge_catalog` (`:2734`) 原文: "enable/strategies 等状态不做任何裁剪"
3. `model_entry` (`:3153`) 把它转成 `/v1/models` 的 `enabled` 字段, 只是标注
4. 唯一的过滤式使用在 `_default_test_model` (`:6172-6173`):
   `[m for m in qoder_catalog.models_for_realm(r) if m.get("enable") is not False]`
   —— 这是选探活模型, 用的是**静态快照**, 不是当前账号的响应
5. `read_dynamic_models` (`:3070-3082`) 对 `chat` 数组**逐条全收**, 无任何 `enable` 判断

所以: 上游动态响应里的 `enable` 值反映"这个账号此刻能不能用", 参考实现**不做过滤**,
只在面板展示。本仓库 P4 的 `enable == false` 不注册是**与参考实现不同**的决策 (design 7.3),
方向正确 (INTL 侧 15/17 为 false 会让代理暴露不可用模型), 但要意识到这是本仓库
独有的过滤, 不能声称"对齐参考实现"。

同一账号不同时刻 `enable` 可能变化 (`catalog-v6` 快照与动态接口差异, 见 `qoder_catalog.py:28-43`),
因此**缓存必须按账号隔离**, 不能跨账号共享 —— 与 D3 的 per-auth 探测一致。

### A.5 分页 / 查询参数

除 `Encode=1` 外**无其他参数**:

```python
MODELS_PATH = "/algo/api/v2/model/list?Encode=1"      # qoder_proxy.py:332
```

- 无 `scene` / `page_size` / `cursor` / `offset` 参数
- 请求方法在参考实现里是 **GET 但带 body**: `urllib.request.Request(url, data=sign_body.encode("utf-8"), method="GET", headers=headers)` (`:3059-3060`), `sign_body = qoder_encode(b"{}")` (`:3050`)
- docstring (`:3039-3040`): "签名的 body 是 `qoder_encode("{}")`，请求必须**带同款 body**发出
  （服务端校验签名与 body 一致，裸 GET 会 403）"
- 首个主机失败后按 `gateway_candidates(realm)` 换域 (`:3052`); 签名只覆盖 path, 换域不影响
- 无超时以外的重试: 参考实现用 `timeout=15` (`:3061`), 但本仓库 AGENTS.md 禁止在
  连接建立后设超时, 且这是"获取凭证"之外的出站请求 —— 见风险项 20

**Qoder 侧的 headers 与推理请求同构**, 只有两处不同 (`qoder_sign.py:647-681`):
- `accept: application/json` (推理是 `text/event-stream`)
- `sse=False` 时不发 `cache-control` 与 `cosy-machinehostname`
- `model_key=""` 时不发 `x-model-key` / `x-model-source`

已实现的 `helps.CosySession.Headers(encodedBody, rawURL, modelKey, sse)` 完全覆盖
(`internal/runtime/executor/helps/qoder_cosy.go:536`+): `sse=false` + `modelKey=""`
正好产出所需头集。

### A.6 `promotion` (本仓库不需要, 记录备查)

`qoder_proxy.py:3287-3338` 从 `promotion.before_promotion_price_factor × discount_factor`
推导谷时价。本仓库无计费落点, P4 不移植。

---

## B. Antigravity 基线 (P4 的克隆源)

### B.1 `sdk/cliproxy/antigravity_models.go` (740 行)

| 关注点 | 位置 | 签名 / 语义 |
|---|---|---|
| 常量 | `:22-30` | `antigravityCapabilityCacheTTL = registry.ModelsRefreshInterval` (= 3h, `model_updater.go:17`); `antigravityCapabilityRetryBase = 1m`; `RetryMax = 30m`; `antigravityRefreshScanInterval = 1m` |
| 缓存条目 | `:40-49` | `hints / expiresAt / lastUsed / probeRevision / appliedRevision / appliedEpoch / persistedRevision / pending` |
| 失败退避 | `:54-71` | `nextAntigravityFailure(prev, now, randomInt64N)` —— 2/4/8/16/30 分钟封顶 + equal jitter; 距上次失败超 `2*TTL` 重置 |
| 包级状态 | `:73-82` | `antigravityCapabilityMu sync.RWMutex`; `antigravityCapabilityCache map[string]...`; `antigravityAuthFailureCache`; `antigravityCapabilityGroup singleflight.Group`; `antigravityProbeSequence atomic.Uint64`; `antigravityProbeSlots chan struct{}` 容量 `modelRegistrationMaxWorkersPerCategory` (= 5, `service_plugins.go:21`) |
| **cache key** | `:104-112` | `antigravityCapabilityKeyForRoute(auth, endpoint, proxy, userAgent) string` -> `fmt.Sprintf("%q/%d/%q/%q/%q/%q", identity, auth.RegistrationEpoch, project, endpoint, proxy, userAgent)`; `identity` 为 `auth.ID`, 为空时退化为 `sha256(access_token)` |
| **失败 key** | `:258` | `failureKey := cacheKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(accessToken)))` —— token 轮换即重置退避 |
| 冷启动播种 | `:123-152` | `cachedAntigravityHintsForKey` -> 缓存未命中时 `persistedAntigravityHintsForAuth` -> `seedAntigravityCapabilityCacheLocked` 分配新 `revision` |
| **fetch** | `:243-356` | `fetchAntigravityModelCapabilityHints(ctx, auth) (cacheKey string, hints antigravityModelCapabilityHints)`; 内部: 读 token -> 查缓存 -> singleflight `DoChan` -> `beginAntigravityAccountProbe` -> `probeAntigravityModelCapabilityHints` -> 结果按 `probeRevision` 围栏写回 -> 失败走 `nextAntigravityFailure` |
| 网络编解码调用 | `:358-374` | `probeAntigravityModelCapabilityHints` -> `sdkauth.FetchAntigravityAvailableModels(ctx, req)` |
| **投影** | `:513-528` | `antigravityModelsForHintsWithConfig(auth, hints, cfg) []*ModelInfo`; 顺序: `filter` -> capability 注入 -> `applyExcludedModels` -> `applyOAuthModelAliasForAuth(cfg, "antigravity", ...)` -> `appendPluginModels` -> `applyOAuthSettingsForAuth` -> `applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)` |
| **带围栏发布** | `:549-600` | `applyAntigravityModelHints(ctx, auth, providerKey string, hints, expectedKey string, expectedEpoch, expectedRegEpoch uint64)`; 三道校验: (1) `antigravityCapabilityKey(auth) != expectedKey` 直接返回, (2) `coreManager.GetByID` 后逐字段比对 (`:558`), (3) 缓存 `entry.hints.revision != hints.revision` 后放弃; 然后 `reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)` (`:579`) |
| 发布后 | `:596-599` | `if updated { coreManager.ReconcileRegistryModelStates(ctx, auth.ID); coreManager.RefreshSchedulerEntry(auth.ID) }` |
| **持久化** | `:176-216` | `persistAntigravityCapabilityHints(ctx, auth, expectedKey, endpoint, hints) bool`; `coreManager.PersistMetadataEntry(ctx, auth.ID, auth.RegistrationEpoch, antigravityPersistedHintsMetaKey, value, validate func(existing *coreauth.Auth) bool)`; 返回值 `(persisted, preempted)` |
| 持久化值构造 | `:218-234` | `buildPersistedAntigravityCapabilityHintsValue` / `parsePersistedAntigravityCapabilityHints` 委托给 `sdkauth`; metadata key = `"antigravity_capability_hints"` (`sdk/auth/antigravity_models.go:24`) |
| 扫描循环 | `:605-638` | `refreshAntigravityModels(ctx)`; 先 `cancelStaleAntigravityProbes("")`, 再遍历 `coreManager.List()` 过滤 `strings.EqualFold(auth.Provider, "antigravity")` |
| 新鲜度判定 | `:627-635` | `registered := entry.appliedRevision == entry.hints.revision && entry.appliedEpoch == epoch`; `persisted := entry.hints.ModelIDs == nil \|\| entry.persistedRevision == entry.hints.revision`; `fresh := (now.Before(expiresAt) && registered && persisted) \|\| now.Before(failureNextRetry)` |
| 调度 | `:640-671` | `scheduleAntigravityModelRefresh` / `nextAntigravityModelRefreshDelay` (取所有 auth 的 retry/expiry 最小值, 上限 1 分钟) |
| **主循环** | `:673-699` | `runAntigravityModelRefresh(ctx)`; `wake` channel 可提前 timer |
| 对账辅助 | `:475-483` | `reconcileRegisteredModelStates(ctx, auth)` —— Home 模式下对 antigravity 提前返回 |

`sdk/auth` 与 `sdk/cliproxy` 的分工 (关键分层事实):
- `sdk/auth/antigravity_models.go` 只做 HTTP + 解析 + 持久化值编解码, 不碰 `coreauth.Auth`
  的运行时状态, 不知道 Service 存在
- `sdk/cliproxy/antigravity_models.go` 持有所有状态机 (缓存/围栏/退避/队列)

### B.2 `sdk/cliproxy/antigravity_model_probes.go` (408 行)

| 函数 | 位置 | 签名 / 语义 |
|---|---|---|
| 队列请求 | `:12-17` | `type antigravityModelRefreshRequest struct { ctx; auth *coreauth.Auth; provider string; registryEpoch uint64 }` |
| 持久化请求 | `:19-26` | `type antigravityHintPersistRequest struct { ctx; auth; routeKey; endpoint; hints; cancel }` |
| **入队** | `:31-78` | `queueAntigravityModelRefresh(ctx, auth, provider string)`; 早退条件: `s==nil / auth==nil / auth.ID=="" / auth.Disabled / antigravityHomeEnabled() / ctx.Err()!=nil / pluginHostHasAuthModelProvider(...) / lifetime 已取消`; 用 `antigravityProbeMu` 保护 `antigravityRefreshPending[auth.ID]`, 同一 auth 已有排队项时**替换而非累积** (`:70`), 无 worker 时 `Wg.Add(1)` + `go runAntigravityAccountRefresh` |
| worker | `:80-95` | `runAntigravityAccountRefresh(authID string)`; 循环取队列, 取空即退出 |
| 执行 | `:97-117` | `executeAntigravityModelRefresh(request)`; 派生 `probeCtx`, 绑定 `antigravityContext` 生命周期, 调 `probeAndRegisterAntigravityModels`, 成功且 `hints.ModelIDs != nil` 时 `queueAntigravityHintPersistence` |
| **探测租约** | `:129-203` | `beginAntigravityAccountProbe(parent, auth, routeKey, flightKey, token string) (context.Context, func(), bool)`; 关键: **先订阅 `WatchAuthChanges` 再校验** (`:143-147`, 注释明确说明理由); 校验通过才登记到 `antigravityAccountProbes[auth.ID]`, 后台 goroutine 消费变更通知并 `cancelStaleAntigravityProbes(auth.ID)` |
| 当前性判定 | `:206-222` | `antigravityAccountProbeCurrent(probe)`; 条件: 非 Home / `GetByID` 存在 / 未 disabled / `antigravityAuthModelSettingsEqual` / **routeKey 相同** / **token 相同** |
| 持久化入队 | `:227-283` | `queueAntigravityHintPersistence(ctx, auth, expectedKey, endpoint, hints)`; 陈旧性检查 `entry.persistedRevision >= hints.revision` -> 丢弃; 新 revision 或不同 routeKey 时 cancel 进行中的任务 (`:270-274`) |
| 持久化执行 | `:309-327` | `executeAntigravityHintPersistence`; `persistAntigravityCapabilityHints` 返回 `preempted==true` 时自动重入队 |
| **陈旧取消** | `:347-389` | `cancelStaleAntigravityProbes(authID string)`; `authID==""` 表示全量; 对每个不再 current 的 probe 调 `cancel()` 并 `antigravityCapabilityGroup.Forget(flightKey)` (`:361`, 注释: 让同 token 的新快照不必等旧 leader 收尾) |
| **唤醒** | `:391-408` | `antigravityModelRefreshWake() chan struct{}` (惰性创建, cap=1) / `signalAntigravityModelRefresh()` (非阻塞发送) |
| 设置比对 | `antigravity_models.go:542-547` | `antigravityAuthModelSettingsEqual(a, b *coreauth.Auth) bool`; 比 `Provider / Prefix / RegistrationEpoch / Disabled / Status-Disabled / AuthKind / Attributes` (不含 Generation) |

### B.3 `sdk/cliproxy/service_models.go`

- `:32` `registerModelsForAuthWithConfigAndCache(ctx, cfg, a, compatCache) bool`
- `:46` 入口即 `s.cancelStaleAntigravityProbes(a.ID)`
- `:91` `switch provider {`
- `:131-147` antigravity 分支 (冷启动模板):

```go
case "antigravity":
    if !(cfg != nil && cfg.Home.Enabled) {
        expectedRegEpoch := GlobalModelRegistry().ClientRegistrationEpoch(a.ID)
        expectedKey := s.antigravityCapabilityKey(a)
        hints := s.cachedAntigravityHints(a)
        if hints.ModelIDs != nil {
            s.applyAntigravityModelHints(ctx, a, provider, hints, expectedKey, a.RegistrationEpoch, expectedRegEpoch)
            s.asyncProbeAntigravityCapabilities(ctx, a, provider)
            return true          // 注意: 早退, 不走 :334-350 的通用注册路径
        }
    } else {
        models = registry.GetAntigravityModels()
    }
    models = applyExcludedModels(models, excluded)
```

- `:334-350` 通用尾部: `applyOAuthModelAliasForAuth` -> `appendPluginModels` ->
  `applyOAuthSettingsForAuth` -> `registerResolvedModelsForAuthFenced(... applyModelPrefixes(...))`;
  `:346-348` 与 `:353-355` 对 antigravity 额外触发 `asyncProbeAntigravityCapabilities`
- `:716-764` `applyModelPrefixes(models []*ModelInfo, prefix string, forceModelPrefix bool) []*ModelInfo`

### B.4 `sdk/cliproxy/service_lifecycle.go`

- `:34` `Run(ctx)`; `:41-44` 派生 `runCancel` 并把 `ctx` 存进 `s.antigravityContext` (cfgMu 保护)
- `:57` `s.startModelCatalogUpdaters(ctx)` —— 早于 refresh callback 安装
- `:236` `s.registerModelRefreshCallback()`
- `:237-239`:
  ```go
  if !homeEnabled {
      go s.runAntigravityModelRefresh(ctx)
  }
  ```
  Home 模式跳过, 且 `homeEnabled` 在 `:61` 由 `cfg.Home.Enabled` 求得
- `:438` `registry.StartModelCatalogUpdaters(ctx, cfg.Models, cfg.Home.Enabled)`

其他 probe 生命周期钩子 (P4 必须一并接):
- `service_config.go:165` 配置切换后 `cancelStaleAntigravityProbes("")`
- `service_auth.go:482` 登录/注册后 `cancelStaleAntigravityProbes(auth.ID)`
- `service_auth.go:659` 删除 auth 后 `cancelStaleAntigravityProbes(id)`
- `service_models.go:46` 每次模型注册入口

### B.5 `sdk/auth/antigravity_models.go` (327 行) —— 网络编解码

| 项 | 位置 |
|---|---|
| 常量 | `:18-28` `AntigravityModelBaseURLDaily` / `AntigravityModelsPath` / `AntigravityPersistedHintsMetaKey`; 上限 `antigravityMaxCatalogModels = 1024`, `antigravityMaxModelIDBytes = 256` |
| 状态枚举 | `:31-37` `AntigravityProbeStatus`; `Success / AuthError / TransientError` |
| 结果类型 | `:40-44` `AntigravityModelCapabilityHints { WebSearchModelIDs, ModelIDs map[string]struct{} }`; `ModelIDs == nil` 表示"未知", 空非 nil 表示"权威空目录" |
| 请求结构 | `:55-61` `AntigravityModelProbeRequest { BaseURLs, ProxyURL, AccessToken, ProjectID, UserAgent }` |
| **拉取** | `:65-137` `FetchAntigravityAvailableModels(ctx, req) (AntigravityModelCapabilityHints, AntigravityProbeStatus)`; `http.Client{CheckRedirect: 拒绝跟随}` (`:84-88`); `proxyutil.BuildHTTPTransport` (`:89`); **无 `http.Client.Timeout`**; 401/403 -> `AuthError`, 其他非 2xx -> `TransientError`; 响应 `8 << 20` 上限 |
| 解析 | `:161-212` `ParseAntigravityModelCapabilityHints(body []byte) (..., bool)`; 用 `ojson` |
| 持久化值 | `:216-257` `BuildPersistedAntigravityCapabilityHints(project, endpoint, hints) map[string]any`; `ModelIDs == nil` 时返回 nil |
| 持久化读 | `:260-300` `ParsePersistedAntigravityCapabilityHints(metadata, expectedProject, expectedEndpoint)`; **project/endpoint 不匹配即拒绝** (这正是 per-account 隔离的手段) |

分层结论: `sdk/auth` 不 import `internal/registry`, 不 import `sdk/cliproxy`, 也不 import
`internal/runtime/executor/helps` (实测三者 `go list -deps ./sdk/auth` 均无匹配;
反向 `sdk/cliproxy` 依赖 `sdk/auth` 且依赖 `helps`, 反向非法)。

---

## C. 所有权与状态

### C.1 `ReplaceClientModels` —— 签名与围栏语义

`internal/registry/model_registry.go:487-501`:

```go
// ReplaceClientModels replaces a client's models only if its registration epoch matches.
// Epoch zero and epochs for empty catalogs are valid. A successful replacement advances
// the epoch and preserves quota, suspension state and the projection generation
// watermark for an existing registration with the same provider.
// Empty models unregister the client; a mismatched epoch leaves the registry unchanged.
func (r *ModelRegistry) ReplaceClientModels(clientID, clientProvider string, expectedEpoch uint64, models []*ModelInfo) (uint64, bool)
```

- `clientID` = `auth.ID`; `clientProvider` = provider key (`"qoder"` / `"qoder-intl"`)
- `expectedEpoch` 来自 `GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)`
  (`model_registry.go:2099-2106`, 读 `r.clientEpochs[clientID]`)
- **stale 写入返回 `(0, false)`, 注册表不变** (`:496-498`)
- 成功时 `registerClientLocked(..., preserveState=true)` (`:499`), epoch 自增 (`:550`)
- `len(uniqueModelIDs) == 0` 时走注销路径 (`:532-542`)
- **`ReplaceClientModels` 不在 `sdk/cliproxy.ModelRegistry` 接口里** (`sdk/cliproxy/model_registry.go:23-38`),
  所以调用方必须是 `registry.GetGlobalRegistry()` 返回的具体类型 —— 这正是 antigravity
  已经在做的 (`antigravity_models.go:577-579`), 非新边

同族 API:
- `RegisterClientIfEpochCurrent(clientID, clientProvider, expectedEpoch, models, publish func() bool) bool` (`:461-469`)
- `UnregisterClientIfEpochCurrent(clientID, expectedEpoch, publish func() bool) bool` (`:476-485`)

### C.2 `PersistMetadataEntry` —— 签名与 key 命名空间

`sdk/cliproxy/auth/conductor_persistence.go:297-302`:

```go
// PersistMetadataEntry updates a single metadata entry on the runtime auth for
// the given registration epoch and persists it to the backing store without
// advancing Generation or firing credential-lifecycle hooks. If a foreground
// credential mutation contends for the same auth while store I/O is in flight,
// the metadata save is preempted so foreground operations do not stall.
func (m *Manager) PersistMetadataEntry(ctx context.Context, id string, expectedEpoch uint64, key string, value any, validate func(existing *Auth) bool) (bool, bool)
```

- 返回 `(persisted, preempted)`。`preempted == true` 表示被前台变更抢占 (可重试,
  见 `antigravity_model_probes.go:324-326` 的语义)
- **key 命名空间 = `auth.Metadata` 的顶层 key**。写入点 `:352` `updated.Metadata[key] = value`
- 落盘走 `m.persistLocked` (`:224-232`) -> `FileTokenStore` -> 整个 `Auth.Metadata` 以 JSON
  写进 auth 文件 (`sdk/auth/filestore.go:126-141`, 无前缀改写)。因此 key 必须**全局唯一且带
  provider 前缀**, 避免与别的 provider 撞名
- antigravity 用的 key: `antigravity_capability_hints` (`sdk/auth/antigravity_models.go:24`)。
  Qoder 应使用 `qoder_model_catalog_hints` 之类的独立 key
- `expectedEpoch == 0` 表示不校验 (`:312` `(expectedEpoch != 0 && ...)`)
- `ctx = WithoutSkipPersist(ctx)` (`:306`), 即强制落盘, 不受 skip 标志影响
- 读回: `FileTokenStore.readAuthFiles` 把整个 JSON 反序列化进 `Metadata`
  (`sdk/auth/filestore.go:244-248`), 所以持久化值是 `map[string]any` 形态

### C.3 `ReconcileRegistryModelStates` / `RefreshSchedulerEntry`

`sdk/cliproxy/auth/conductor_selection.go:210`:

```go
func (m *Manager) ReconcileRegistryModelStates(ctx context.Context, authID string)
```

作用 (`:205-209` 注释 + 实现):
- 读 `globalReg.GetModelsAndEpochForClient(authID)` (`:237`)
- 把 `auth.ModelStates` 的 key 按 `selectionModelKeyForAuth` 迁移到注册表的 route key
- **删除已从目录消失的模型的 `ModelStates`** (`:319-330`), 也删除不再支持的 alias state
- 保留仍在支持的模型的 quota / suspension 状态

对 Qoder 的必要性: 目录会随账号 `enable` 变化而收缩,**必须**调用, 否则被删模型的冷却
状态会残留并影响后续调度。

`sdk/cliproxy/auth/conductor_selection.go:170`:

```go
func (m *Manager) RefreshSchedulerEntry(authID string)
```

作用 (`:174-184`): 取 `GetByID` -> `Clone` -> `m.structuralEpoch.Add(1)` -> `m.scheduler.upsertAuth(snapshot)`。
不调用则 scheduler 的 model meta 不会看到新目录 —— 请求会因
`supportsModel` 失败 (`scheduler.go:1189-1200`) 而选不到 auth。

antigravity 的调用条件 (`antigravity_models.go:596-599`): **仅当 `updated == true`**。
`ReplaceClientModels` 返回 false 时两个都不调。

配套事实: `upsertAuth` 只对已存在的 shard 生效 (`scheduler.go:1094-1111`),
新模型 shard 由 `ensureModelLocked` (`:1164-1187`) 在首次请求时惰性构建, 从
`p.auths` 里按 `supportsModel` 补登。所以 `RefreshSchedulerEntry` 更新的是 `p.auths` 里的 meta,
新模型能被后续 `ensureModel` 看到 —— 不需要额外的 shard 预热。

---

## D. Provider 隔离 (design 第 8 节核实)

### D.1 `applyModelPrefixes` 与 `rewriteModelForAuth`

**注册侧** `sdk/cliproxy/service_models.go:716-764`:

```go
func applyModelPrefixes(models []*ModelInfo, prefix string, forceModelPrefix bool) []*ModelInfo
```

行为逐行:
- `:717-720` prefix 为空或 models 为空 -> **原样返回, 不做任何事** (force 参数都没读)
- `:748-750` `if !forceModelPrefix || trimmedPrefix == baseID { addModel(model) }` ——
  即 `force=false` 时**保留原始 ID**; `force=true` 时**丢弃原始 ID**, 只留带前缀变体
- `:751-761` 无条件追加一个 `<prefix>/<baseID>` 克隆; `MetadataModelID` 缺省填 `baseID`;
  `Name` 经 `rewriteModelInfoName` 改写
- 去重靠 `seen` map (`:723-738`)

注意: `force=false` 时两个 ID 都注册 (`baseID` + `prefix/baseID`), `force=true` 时只注册
`prefix/baseID`。这条分支是 `force-model-prefix` 的**全部**注册语义。

**请求侧** `sdk/cliproxy/auth/conductor_models.go:710-723`:

```go
func rewriteModelForAuth(model string, auth *Auth) string {
	if auth == nil || model == "" { return model }
	prefix := strings.TrimSpace(auth.Prefix)
	if prefix == "" { return model }
	needle := prefix + "/"
	if !strings.HasPrefix(model, needle) { return model }
	return strings.TrimPrefix(model, needle)
}
```

即: **只有当 auth 有 prefix 且模型名以 `prefix/` 开头时才剥**。没有 prefix 的 auth 永远
拿到原始模型名。调用点 8 处 (`api_key_model_capabilities.go:158,338`;
`conductor_home.go:606`; `conductor_models.go:172,207,343,377,530`; `home_selection.go:348`),
全部在 auth 已选定之后。

### D.2 `routing.force-model-prefix` 的语义与影响面

定义 `internal/config/sdk_config.go:85-88`:

```go
// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
// to target prefixed credentials. When false, unprefixed model requests may use prefixed
// credentials as well.
ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`
```

v8 路径 `internal/config/config_v8.go:82`: `{"force-model-prefix", "routing.force-model-prefix"}`。
默认值 `config.example.yaml:205` 为 `false`。

**影响面实测 (全部非测试调用点, 共 7 处)**:
- `antigravity_models.go:527`
- `service_executors.go:713`
- `service_models.go:275,279,298,302,322,345`

全部是 `applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)`, 且都在**单个 auth** 的
模型注册路径上。没有全局的请求侧闸门。

因此回答: **`force-model-prefix` 只改变"有 prefix 的 auth" 注册什么 ID, 不影响无 prefix 的
auth**。因为:
1. `applyModelPrefixes` 在 `prefix == ""` 时立即返回 (`:717-720`)
2. 请求侧唯一读 prefix 的地方是 `rewriteModelForAuth`, 同样在无 prefix 时原样返回
3. 唯一的额外读点是 `sdk/api/handlers/openai/openai_images_handlers.go:354` (xAI 图像专用),
   与 qoder 路径无关

所以 design 8 的"副作用范围只影响有 prefix 的 auth"结论**成立**。若 qoder 独占 `cn` / `intl`
两个前缀, 不会有其它 provider 使用它们。

**但有一个设计未提及的后果**: 打开 `force-model-prefix` 后,
**所有其它 provider 的带 prefix auth 也会失去无前缀 ID**。如果用户的配置里存在其它带
prefix 的 auth (例如 `teamA`), 打开这个开关会让 `teamA/gemini-3-pro` 之前的无前缀别名消失。
这是全局开关, 是此设计的真实 blast radius, 不是"只影响 qoder"。

### D.3 两个无 prefix auth (`qoder` / `qoder-intl`) 注册同名 `qmodel` 的实际行为

追踪 `registerClientLocked` (`model_registry.go:504-735`) 的共享 ID 路径:

1. 第一个 auth 注册 `qmodel`: `r.models["qmodel"]` 不存在 -> `addModelRegistration` 建
   `ModelRegistration{Info: clone, Count: 1, Providers: {"qoder": 1}, InfoByProvider: {...}}` (`:776-789`)
2. 第二个 auth 注册 `qmodel`: `exists` 命中 -> `existing.Count++`, `existing.Info = cloneModelInfo(model)`
   **覆盖为后注册者的 Info** (`:751-756`), `existing.Providers["qoder-intl"]++` (`:763-771`)
3. `/v1/models` 只出现**一条** `qmodel` (`buildAvailableModelsLocked:1564-1584` 按 `r.models` map 迭代)
4. `GetModelProviders("qmodel")` (`:1793-1842`) 返回 `["qoder", "qoder-intl"]`, 按 count 降序、
   count 相同按名字升序 —— 于是 `util.GetProviderName` (`internal/util/provider.go:46-79`)
   返回两个 provider, `handlers_routing.go:201` 把它交给 `AuthManager.Execute(ctx, providers, ...)`
5. `executeMixedOnce` (`conductor_execution.go:519-...`) -> `pickNextMixed` -> 遍历
   **所有** provider 的 auth (`conductor_selection.go:2365-2395`, `providerSet` 含两者)
6. 结果: **跨区随机/轮转混选**。CN 的 auth 和 INTL 的 auth 都可能收到 `qmodel` 请求,
   且模型元数据 (Info) 是最后注册者覆盖的。

**带 prefix `cn` / `intl` 时**:
1. CN auth 注册 `cn/qmodel` (+ force=false 时还有 `qmodel`); INTL auth 注册 `intl/qmodel`
2. 客户端请求 `cn/qmodel`:
   - `util.GetProviderName("cn/qmodel")` -> `GetModelProviders("cn/qmodel")` -> `["qoder"]`
     (只有 CN auth 注册了这个 ID)
   - `authSupportsRouteModel` (`conductor_selection.go:1060-1073`) 用
     `selectionModelKeyForAuth` = `rewriteModelForAuth` 后 `canonicalModelKey` =
     `qmodel`, 再查 `ClientSupportsModel(auth.ID, "qmodel")`
   - CN auth 的 `clientModels` 里含 `cn/qmodel`, 剥前缀后是 `qmodel` -> 命中
   - `GetModelProviders("cn/qmodel")` 只返回 `qoder`, 所以 INTL auth 不在候选 provider 里
3. 请求侧 `rewriteModelForAuth("cn/qmodel", cnAuth)` -> `qmodel`, 上游收到正确 ID

**关键约束**: 上述隔离**只在 `force-model-prefix = true` 时严格成立**。`force=false` 时
两个 auth 都还注册了无前缀 `qmodel`, 冲突依旧存在 (第 4-6 步的混选复现)。
design 第 8 节把两者绑在一起是对的。

**另一个约束 (本次新发现)**: `GetModelProviders` 依赖 `r.clientProviders[clientID]`, 而
`ReplaceClientModels` 的 `clientProvider` 参数决定它 (`model_registry.go:729-733`)。所以
Qoder 发布时必须传**正确的 provider key** (`"qoder"` vs `"qoder-intl"`), 不能统一传 `"qoder"`,
否则 `GetModelProviders` / `GetAvailableModelsByProvider` 的区分失效。

---

## E. 静态种子

### E.1 `GetStaticModelDefinitionsByChannel` 必须有 channel case

`internal/registry/model_definitions.go:575-605` 是硬编码 switch, 无 channel 就返回 `nil`。

管理端点 `internal/api/handlers/management/model_definitions.go:61-86`:

```go
models := registry.GetStaticModelDefinitionsByChannel(channel)
if len(models) == 0 {
    models = h.pluginModelDefinitionsByChannel(channel)
}
if len(models) == 0 {
    c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
    return
}
```

走的是 `pluginModelDefinitionsByChannel` 兜底 (`:88-113`):
1. `host.ModelsForProvider(channel)` (插件)
2. `registry.GetGlobalRegistry().GetAvailableModelsByProvider(channel)` (`:100`)

**结论**: 没有 channel case **不一定** 400。只要运行时 registry 里已经有该 provider 的注册
模型, `:100` 会返回它们, 端点就正常。**只有在"未注册任何模型"的冷启动瞬间才会 400**。

这修正了 design.md:517 的表述 ("补静态种子快照，避免管理端点 400") —— 准确说法是
"避免冷启动/无 auth 时管理端点 400"。

### E.2 P4 能否不带静态种子

**能, 但有三处可观测退化**:

1. **管理端点冷启动 400**。无 auth / auth 尚未探测成功时, `GetStaticModelDefinitionsByChannel`
   返回 nil, `GetAvailableModelsByProvider` 也返回 nil -> 400 `unknown channel`。
   影响: 管理前端模型列表页在无 auth 时打不开 qoder 段。运行时不影响。
2. **离线启动无模型**。`service_models.go:131-147` 的 antigravity 模板是
   `hints := s.cachedAntigravityHints(a); if hints.ModelIDs != nil { ... }` —— 冷启动靠
   `auth.Metadata` 里的持久化快照播种。**首次登录、无持久化快照、且上游不可达时, 该 auth
   注册 0 个模型**。此时不走该分支, 会落到 `:352` 的 `unregisterModelsForAuthFenced`,
   该 auth 在 `/v1/models` 里完全不可见。这与 antigravity 行为一致 (`models.json` 里的
   antigravity 段只在 Home 模式用, `:144-146`)。
3. **thinking 映射失效**。`LookupModelInfo` (`model_registry.go:272-288`) 先查动态 registry
   (`GetModelInfo`), 命中即返回; 未命中才回落 `LookupStaticModelInfo`。
   只要目录探测成功, 动态 registry 里有 `Thinking` 就不需要静态种子。

**因此 P4 的种子策略应是**: 静态种子**可选**, 价值在 (a) 管理端点冷启动, (b) 首次探测失败时的
降级。若不做, 必须在 P4 的验收里明确"无 auth 时 `/v1/models` 为空是预期"。

### E.3 若要做静态种子, 需要的改动 (design.md:517 只写了第 1 处)

design.md:517 只提到 `GetStaticModelDefinitionsByChannel` 补分支。实际需要 **4 处**:

1. `staticModelsJSON` struct 加字段 (`model_definitions.go:30-45`) —— 不加则 `json.Unmarshal`
   静默丢弃未知键 (这是 `03-thinking-mapping.md` 第七节第 1 条已指出的陷阱)
2. 新增 getter (如 `GetQoderModels`), 仿 `GetAntigravityModels` (`:97-100`)
3. `GetStaticModelDefinitionsByChannel` switch 加 case (`:577-604`)
4. `LookupStaticModelInfo` 的 `allModels` 列表加一段 (`:645-657`)

可选第 5 处: `models.json` 加 `"qoder"` / `"qoder-intl"` 段。
**注意**: `validateModelsCatalog` 的 `requiredSections` (`model_updater.go:222-238`) **不应**
加 qoder —— 加了会让缺 qoder 段的远端 catalog 整个校验失败。

**另一个陷阱**: `publishCatalogBytes` (`catalog_sources.go:322-341`) 对远端 catalog 做
carry-forward 时只补 `Meta` 与 `CodeBuddyCN`。若 qoder 段只在本地 `models.json` 里有、
远端没有, 一次远端刷新就会把 qoder 段清空。这正是 `04-catalog-fetch-layering.md:185`
说"不要给 carry-forward 加 qoder 段"的原因 —— 一旦加静态种子并依赖它, 就必须同时加
carry-forward, 否则会在运行时被静默清掉。

---

## 复核裁决 (main session, 2026-11-06)

本文件交付后, 三处冲突经实测复核, 结论如下。实施时以本节为准。

### 冲突 #1: `sdk/auth` 不能 import `helps` -> **不成立**

实测: 在 `sdk/auth` 内引用 `helps.StreamScannerBuffer` 后 `go build ./sdk/auth` 通过
(exit 0)。`go list -deps` 确认两条边均无环:

- `sdk/cliproxy/auth` **不**依赖 `sdk/auth`
- `helps` **不**依赖 `sdk/auth`

本文件把 `sdk/auth` 与 `sdk/cliproxy/auth` 混为一谈。`sdk/auth` 确实依赖
`sdk/cliproxy/auth` (单向), 但那与 `helps` 无关。

**结论**: design.md 原方案 (`sdk/auth/qoder_models.go` 直接复用 `helps.CosySession`) 成立。
不需要"HTTP 解码 + headers 回调注入"的绕行, 也不需要把 `CosySession` 迁到
`internal/auth/qoder`。

### 冲突 #2: 静态种子需 4 处 -> **成立**

已更新 `design.md` 第 9 节。实施时按 4 处执行 (struct 字段 / getter / channel case /
`LookupStaticModelInfo`), 并且**不要**给 `requiredSections` 或 carry-forward 加 qoder 段。

### 冲突 #3: `qmodel` / `qmodel_latest` 的 `levels:["high"]` -> **保留该值, 修正理由**

数据事实确认: 这两个模型 `enabled` 存在但**无 `efforts` 键**。`levels:["high"]` 是表达手段,
不是从上游数据读出。已更新 `03-thinking-mapping.md` 记录:

- 写成 `levels: []` 会让 `detectModelCapability` 返回 `CapabilityNone` 但 `Thinking` 非 nil,
  客户端任意档位原样透传 —— 比参考实现更差。
- `levels:["high"]` + `zero_allowed:true` 让 `none` 可用, 其余档位上行为与参考实现等价
  (上游对不认识的档位静默忽略), 唯一差异是越界档位在本仓库报
  `ErrLevelNotSupported` 而非静默丢弃, 这是 D6 严格校验决策的后果。

### 另需注意: `force-model-prefix` 是全局开关 -> **已记录**

已更新 `design.md` 第 8 节, 写明真实 blast radius: 影响所有带 prefix 的 auth, 不只 qoder。

## 克隆地图

| Antigravity 符号 (文件:行) | Qoder 目标符号 | 改动点 |
|---|---|---|
| `antigravityCapabilityCacheEntry` (`antigravity_models.go:40-49`) | `qoderCatalogCacheEntry` | 字段同构; 去掉 `WebSearchModelIDs` 概念 |
| `antigravityFailureState` + `nextAntigravityFailure` (`:54-71`) | `qoderFailureState` + `nextQoderFailure` | 完全同构, 常量改名 |
| `antigravityCapabilityKeyForRoute` (`:104-112`) | `qoderCatalogKeyForRoute` | `project` -> provider 名 (`qoder`/`qoder-intl`); **必须把 provider 拼进 key**, 否则双区同 uid 会撞 key |
| 包级 `antigravityCapabilityMu/Cache/AuthFailureCache/Group/ProbeSequence/ProbeSlots` (`:73-82`) | 同名 `qoder*` | 槽位复用 `modelRegistrationMaxWorkersPerCategory` |
| `cachedAntigravityHintsForKey` (`:123-138`) | `cachedQoderCatalogForKey` | 播种逻辑同构 |
| `seedAntigravityCapabilityCacheLocked` (`:140-152`) | `seedQoderCatalogCacheLocked` | 同构 |
| `persistedAntigravityHintsForAuth` (`:154-174`) | `persistedQoderCatalogForAuth` | 校验维度: 用 `endpoint` 而非 `project` |
| `persistAntigravityCapabilityHints` (`:176-216`) | `persistQoderCatalogHints` | metadata key 换 `qoder_model_catalog_hints` |
| `fetchAntigravityModelCapabilityHints` (`:243-356`) | `fetchQoderModelCatalog` | 返回 `(cacheKey, *sdkauth.QoderCatalog)`; **不需要 `filterAntigravityModels` 这一步** (Qoder 目录本身就是模型列表) |
| `probeAntigravityModelCapabilityHints` (`:358-374`) | `probeQoderModelCatalog` | 调用 `sdkauth.FetchQoderModels`; **需要 COSY 头集**, 见下节签名 |
| `antigravityModelBaseURLs` / `resolveAntigravityModelBaseURL` (`:392-431`) | `qoderGatewayBaseURLs` | CN 单主机, INTL 三主机候选 (design 5.3); 同样接受 `auth.Attributes["base_url"]` 覆盖 |
| `antigravityModelFetchProxyURL` (`:376-390`) | `qoderModelFetchProxyURL` | 完全同构 |
| `antigravityModelsForHintsWithConfig` (`:513-528`) | `qoderModelsForCatalog` | 跳过 `filter` 与 capability 注入; 保留 `applyExcludedModels` / `applyOAuthModelAliasForAuth` / `appendPluginModels` / `applyOAuthSettingsForAuth` / `applyModelPrefixes` 五步 |
| `applyAntigravityModelHints` (`:549-600`) | `applyQoderCatalog` | 三个围栏条件保留; `reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)`; 失败分支 (`hints.ModelIDs == nil` 的 capability-only 路径) **整个删除** —— Qoder 没有这种形态 |
| `refreshAntigravityModels` (`:605-638`) | `refreshQoderCatalogs` | 过滤条件改为 `provider ∈ {qoder, qoder-intl}`; 新鲜度判定同构 |
| `nextAntigravityModelRefreshDelay` (`:647-671`) | `nextQoderCatalogRefreshDelay` | 同构 |
| `runAntigravityModelRefresh` (`:673-699`) | `runQoderModelRefresh` | 同构 |
| `queueAntigravityModelRefresh` (`antigravity_model_probes.go:31-78`) | `queueQoderModelRefresh` | 同构; 早退条件里的 `antigravityHomeEnabled` 换成 qoder 侧的 Home 判断 |
| `runAntigravityAccountRefresh` (`:80-95`) | `runQoderAccountRefresh` | 同构 |
| `executeAntigravityModelRefresh` (`:97-117`) | `executeQoderModelRefresh` | 同构 |
| `antigravityAccountProbe` + `beginAntigravityAccountProbe` (`:121-203`) | `qoderAccountProbe` + `beginQoderAccountProbe` | **同构, 含"先订阅后校验"的顺序**; token 比对是关键 |
| `antigravityAccountProbeCurrent` (`:206-222`) | `qoderAccountProbeCurrent` | 同上 |
| `queueAntigravityHintPersistence` / `runAntigravityHintPersistence` / `executeAntigravityHintPersistence` / `antigravityHintPersistCurrent` (`:227-345`) | `queueQoderCatalogPersistence` 等 | 同构; `hints.ModelIDs == nil` 的早退条件改为 `catalog == nil` |
| `cancelStaleAntigravityProbes` (`:347-389`) | `cancelStaleQoderProbes` | 同构 |
| `antigravityModelRefreshWake` / `signalAntigravityModelRefresh` (`:391-408`) | `qoderModelRefreshWake` / `signalQoderModelRefresh` | 同构 |
| `antigravityAuthModelSettingsEqual` (`antigravity_models.go:542-547`) | `qoderAuthModelSettingsEqual` | 同构 |
| `Service` 字段块 (`service.go:142-153`) | 同名 `qoder*` | 独立字段块, 各 8 个 |
| `service_lifecycle.go:237-239` | 同位置 | `if !homeEnabled { go s.runQoderModelRefresh(ctx) }` |
| `service_models.go:131-147` (case 分支) | `case "qoder", "qoder-intl":` | 冷启动: `cachedQoderCatalogForAuth(a)` 非 nil 则 `applyQoderCatalog` + `asyncProbeQoderCatalog` + `return true` |
| `service_models.go:346-348 / 353-355` | 加 qoder 双 provider | 通用尾部触发异步探测 |
| `service_models.go:46` | `s.cancelStaleQoderProbes(a.ID)` | 与 antigravity 并列 |
| `service_config.go:165` | `s.cancelStaleQoderProbes("")` | 并列 |
| `service_auth.go:482 / 659` | 同上 | 并列 |
| `sdk/auth/antigravity_models.go` (整个文件) | `sdk/auth/qoder_models.go` | 结构对应, 但**解码目标不同**: 见下节 |
| `sdk/auth/antigravity_models.go:216-300` 持久化编解码 | `sdk/auth/qoder_models.go` 同段 | **Qoder 持久化的是完整模型列表 (key + displayName + contextLength + thinking), 不是 ID 集合** |

---

## Qoder 需要的精确函数签名

### 网络编解码 (`sdk/auth/qoder_models.go`)

`sdk/auth` 不能 import `internal/registry` (`go list -deps ./sdk/auth` 无该依赖)。
Antigravity 的返回类型是自有的 `AntigravityModelCapabilityHints`, 不是 `[]*registry.ModelInfo`。
**Qoder 必须照做**: 在 `sdk/auth` 定义自己的 catalog 条目类型。

```go
// 常量
const (
    QoderModelsPath              = "/algo/api/v2/model/list?Encode=1"
    QoderPersistedCatalogMetaKey = "qoder_model_catalog_hints"
)

type QoderProbeStatus int
const (
    QoderProbeStatusSuccess QoderProbeStatus = iota
    QoderProbeStatusAuthError
    QoderProbeStatusTransientError
)

// 单条目录项的解码形态 (sdk/auth 自有类型, 不依赖 registry)
type QoderCatalogEntry struct {
    Key            string
    DisplayName    string
    MaxInputTokens int
    IsVL           bool
    Enable         bool
    Thinking       *QoderThinkingConfig   // nil = 上游无 thinking_config
}

type QoderThinkingConfig struct {
    Efforts     []string  // thinking_config.enabled.efforts 的键, 升序
    ZeroAllowed bool      // "disabled" 键是否存在
}

type QoderCatalog struct {
    Entries []QoderCatalogEntry   // 已按 enable 过滤
}

// 探测请求: 只做 HTTP, COSY 头集由调用方注入 (与 antigravity 分层一致)
type QoderModelProbeRequest struct {
    BaseURLs    []string                              // 网关候选, 见 design 5.3
    ProxyURL    string
    EncodedBody []byte                                // qoder_encode("{}"), 由 helps 产出
    Headers     func(rawURL string) map[string]string // 按主机生成 COSY 头
}

// 单一入口: 依次尝试 BaseURLs, 解码 {"chat":[...]}
func FetchQoderModels(ctx context.Context, req QoderModelProbeRequest) (*QoderCatalog, QoderProbeStatus)

// 响应解析 (供单测直接调用, 不需要网络)
func ParseQoderModelList(body []byte) (*QoderCatalog, bool)

// 持久化编解码
func BuildPersistedQoderCatalog(endpoint string, catalog *QoderCatalog) map[string]any
func ParsePersistedQoderCatalog(metadata map[string]any, expectedEndpoint string) (*QoderCatalog, bool)
```

**分层约束 (design.md:33 需要修正)**: `sdk/auth` 不能 import
`internal/runtime/executor/helps` (实测 `go list -deps ./sdk/auth | grep executor/helps` = 0),
而 `CosySession` 就在 `helps` (`internal/runtime/executor/helps/qoder_cosy.go:422-573`)。
所以 `sdk/auth/qoder_models.go` **不能"纯 COSY 拉取"** —— 它只能做 HTTP + 解码,
COSY 会话由 `sdk/cliproxy` 侧构造后以 headers 回调注入。

可行性已验证: `sdk/cliproxy` 已 import `internal/runtime/executor/helps`
(`go list -f '{{join .Imports "\n"}}' ./sdk/cliproxy` 命中), 所以 `sdk/cliproxy` 可以直接
`helps.NewCosySession(...)` 再向 `FetchQoderModels` 传闭包。

备选方案 (改动更大): 把 `CosySession` 从 `helps` 移到 `internal/auth/qoder`, 由
`sdk/auth` 与 `helps` 双向复用 —— 但会动 P0 已完成的文件。

### 编排 (`sdk/cliproxy/qoder_models.go` + `qoder_model_probes.go`)

```go
// 缓存条目
type qoderCatalogCacheEntry struct {
    catalog           *sdkauth.QoderCatalog
    expiresAt         time.Time
    lastUsed          time.Time
    probeRevision     uint64
    appliedRevision   uint64
    appliedEpoch      uint64
    persistedRevision uint64
    pending           bool
}

// key: 必须含 provider, 否则双区同 ID 撞车
func qoderCatalogKeyForRoute(auth *coreauth.Auth, provider, endpoint, proxy string) string
func (s *Service) qoderCatalogKey(auth *coreauth.Auth) string
func (s *Service) cachedQoderCatalog(auth *coreauth.Auth) *sdkauth.QoderCatalog
func (s *Service) cachedQoderCatalogForKey(auth *coreauth.Auth, endpoint, key string) *sdkauth.QoderCatalog
func (s *Service) persistedQoderCatalogForAuth(auth *coreauth.Auth, endpoint string) (*sdkauth.QoderCatalog, bool)
func (s *Service) fetchQoderModelCatalog(ctx context.Context, auth *coreauth.Auth) (string, *sdkauth.QoderCatalog)
func (s *Service) probeQoderModelCatalog(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL, accessToken string) (*sdkauth.QoderCatalog, sdkauth.QoderProbeStatus)
func qoderGatewayBaseURLs(auth *coreauth.Auth) []string
func (s *Service) qoderModelFetchProxyURL(auth *coreauth.Auth) string

// 投影 (五步, 无 filter)
func (s *Service) qoderModelsForCatalog(auth *coreauth.Auth, catalog *sdkauth.QoderCatalog, cfg *config.Config) []*ModelInfo
// 目录项 -> ModelInfo
func qoderModelInfoFromEntry(entry sdkauth.QoderCatalogEntry, provider string) *ModelInfo
// thinking 映射 (design 7.4)
func qoderThinkingSupport(cfg *sdkauth.QoderThinkingConfig) *registry.ThinkingSupport

// 围栏发布
func (s *Service) applyQoderCatalog(ctx context.Context, auth *coreauth.Auth, providerKey string, catalog *sdkauth.QoderCatalog, expectedKey string, expectedEpoch, expectedRegEpoch uint64)
func (s *Service) persistQoderCatalogHints(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, catalog *sdkauth.QoderCatalog) bool

// 循环
func (s *Service) refreshQoderCatalogs(ctx context.Context)
func (s *Service) nextQoderCatalogRefreshDelay() time.Duration
func (s *Service) runQoderModelRefresh(ctx context.Context)
func (s *Service) asyncProbeQoderCatalog(ctx context.Context, auth *coreauth.Auth, providerKey string)

// 队列/探测 (probes 文件)
type qoderModelRefreshRequest struct { ctx context.Context; auth *coreauth.Auth; provider string; registryEpoch uint64 }
type qoderCatalogPersistRequest struct { ctx context.Context; auth *coreauth.Auth; routeKey, endpoint string; catalog *sdkauth.QoderCatalog; cancel context.CancelFunc }
type qoderAccountProbe struct { auth *coreauth.Auth; routeKey, flightKey, token string; cancel context.CancelFunc }

func (s *Service) queueQoderModelRefresh(ctx context.Context, auth *coreauth.Auth, provider string)
func (s *Service) runQoderAccountRefresh(authID string)
func (s *Service) executeQoderModelRefresh(request *qoderModelRefreshRequest)
func (s *Service) beginQoderAccountProbe(parent context.Context, auth *coreauth.Auth, routeKey, flightKey, token string) (context.Context, func(), bool)
func (s *Service) qoderAccountProbeCurrent(probe *qoderAccountProbe) bool
func (s *Service) queueQoderCatalogPersistence(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, catalog *sdkauth.QoderCatalog)
func (s *Service) cancelStaleQoderProbes(authID string)
func (s *Service) qoderModelRefreshWake() chan struct{}
func (s *Service) signalQoderModelRefresh()
func qoderAuthModelSettingsEqual(a, b *coreauth.Auth) bool
func (s *Service) WaitQoderProbes()
```

`Service` 新增字段 (仿 `service.go:142-153` 八个一组):
`qoderProbeWg sync.WaitGroup` / `qoderContext context.Context` / `qoderProbeMu sync.Mutex` /
`qoderAccountProbes map[string]map[*qoderAccountProbe]struct{}` / `qoderRefreshWake chan struct{}` /
`qoderRefreshPending map[string]*qoderModelRefreshRequest` /
`qoderPersistPending map[string]*qoderCatalogPersistRequest` /
`qoderPersistActive map[string]*qoderCatalogPersistRequest`

---

## 打开的风险 / 未能确认

1. **`chat` 之外是否还有其它顶层键**。参考实现只读 `chat`, 从不查看其它键,
   所以无法证明上游响应里没有 `scene=responses` 之类的第二个数组。**未验证**。
   若真实响应是 `{"chat": [...], "completion": [...]}`, 本仓库只取 `chat` 与参考实现一致,
   但无法确认这是"全部模型"。
2. **`enable` 的完整语义**。参考实现 docstring 说"该账号/套餐当前是否开放", 但代码从
   不使用它做过滤, 也没有注释说明为什么。**无法确认** `enable=false` 的模型是否真的
   调用会失败 —— design 7.3 的"不注册"是保守选择。
3. **`qmodel_latest` / `qmodel` 没有 `efforts` 键**。`enabled` 存在但 `efforts` 缺失。
   若映射成 `Thinking{Levels: nil, ZeroAllowed: true}`, 则 `detectModelCapability`
   (`convert.go:162-183`) 返回 `CapabilityNone`, `ApplyThinking` 走 `Thinking != nil` 的
   正常路径但 `len(support.Levels) == 0`, 行为等同"无档位但有开关"。
   `03-thinking-mapping.md:95` 写的是 `levels: ["high"]` —— 这是**推导**而非上游数据。
   若采用 `levels: ["high"]`, 客户端只能发 `high`; 若不采用就变成纯开关。
   **design 7.4 与 research 03 在这一点上不一致, 需 main agent 定夺**。
4. **`disabled` 为 `{}` 的空对象**。判据是"键存在" (`qoder_proxy.py:3273`),
   已确认。但 `{}` 与 `{"description": ...}` 两种形态是否语义相同 **未能确认**
   (无法判断空对象是否意味着"关闭态未开放")。
5. **`access_token` 轮换与 COSY 会话的关系**。`CosySessionCache` (`qoder_sign.py:684-712`)
   按 `uid` 缓存, token 变化即重建。P4 的 `failureKey` 含 token hash, 但 **`cacheKey`
   (route key) 不含 token** —— 与 antigravity 一致。这意味着 token 轮换后:
   - 失败退避会重置 (failureKey 变)
   - 已成功的目录**不会**失效重建
   对 antigravity 这是有意设计 (注释 `antigravity_models.go:95-96` "Token rotation alone
   does not invalidate a successful catalog")。**Qoder 是否适用未验证** —— 若 `enable`
   与 token 的 scope 绑定, token 轮换可能改变可见模型集。
6. **`/algo/api/v2/model/list` 是否接受 `POST`**。参考实现用 `method="GET"` + body。
   Go 的 `http.NewRequestWithContext(ctx, http.MethodGet, url, bytes.NewReader(body))`
   会正常发送 GET body, 但**部分中间层会丢弃 GET body**。上游是否例外 **未验证**。
7. **网关候选顺序在 CN 侧**。`qoder_accounts.py:42-61` 的 CN 配置**没有 `gateway_fallbacks`**,
   即 CN 只有 `gateway.qoder.com.cn` 单主机。design 5.3 也这么写, 但 **CN 没有备用域**
   意味着单点失败即目录不可用 (推理请求同样)。这是既有事实, 不是 P4 能解决的。
8. **`staticModelsJSON` 加字段后远端 catalog 的行为**。若加 `Qoder` 字段并写进
   `models.json`, 远端刷新 (`publishCatalogBytes`, `catalog_sources.go:322-341`) 不带 qoder 段
   时会把本地段清空 (只 carry-forward `Meta` / `CodeBuddyCN`)。**未验证**远端
   `router-for-me/models` 是否已有 qoder 段。
9. **`applyModelPrefixes` 对 `MetadataModelID` 的处理**。前缀变体的 `MetadataModelID`
   被设为 `baseID` (`service_models.go:753-755`), 但**原始 ID** (force=false 时保留的那条)
   的 `MetadataModelID` 保持原样。这会影响 `applyOAuthSettingsForAuth` 的配置匹配
   (`service_models.go:1436` `ResolveOAuthModelSetting(settings, model.ID, model.MetadataModelID, model.Name)`)。
   对 qoder 的 `cn/qmodel` 场景, `MetadataModelID = "qmodel"` 才能匹配到针对 `qmodel` 的配置。
   **这条路径对 new provider 是否成立未做端到端验证**。
10. **`GetModelProviders` 的排序**。同名模型由两个 auth 注册时, provider 顺序按 count 降序、
    名字升序 (`model_registry.go:1830-1835`)。这意味着 round-robin 的起点不是配置顺序,
    而是字典序。对 `cn` / `intl` 前缀方案无影响 (各自 ID 独立), 但对无前缀方案会影响
    首选 provider 的确定性。
11. **`force-model-prefix` 是全局开关** (config 单个 bool)。打开后会影响**所有**带 prefix 的
    auth, 不只是 qoder。design 8 的"不会波及别的 provider"表述只在"别的 provider 都没有
    prefix"这一前提下成立。
12. **P0 已实现的 `helps.CosySession` 只导出 `NewCosySession` / `UID()` / `Bearer()` / `Headers()`**
    (`qoder_cosy.go:436,503,509,536`), 内部量 (`cosyKey` / `info` / `tempKey`) 均未导出。
    若采用"把 COSY 移进 sdk/auth"的方案, 需要导出更多内部量或整块迁移。当前接口对
    "headers 回调注入"方案已足够。
13. **`antigravityHomeEnabled` 的复用**。Qoder 是否需要独立的 Home 判断, 还是直接复用
    `s.antigravityHomeEnabled()` (它只读 `cfg.Home.Enabled`, 实现见
    `antigravity_models.go:485-492`)。语义上应复用, 但函数名带 antigravity 前缀, 直接调会很怪。
    **需 main agent 决定是否重命名为 `homeModeEnabled`** —— 重命名会动 10 处调用点:
    `antigravity_models.go:244,479,550,606,650`、
    `antigravity_model_probes.go:32,207,228,330`、`service_executors.go:628`。
14. **包循环已确认安全**。`sdk/cliproxy -> internal/runtime/executor/helps` 与
    `sdk/cliproxy -> sdk/auth` 都已是既存边; `helps` 与 `sdk/auth` 互不依赖。
    但 design.md:33 的"`sdk/auth/qoder_models.go` 纯 COSY 拉取编解码"表述需要修正为
    "纯 HTTP 拉取 + 解码, COSY 头集由调用方注入"。
15. **`enable` 过滤会让 `qmodel` 在 INTL 侧完全消失**。INTL catalog 17 条里 15 条
    `enable=false`, 只有 `qmodel_38max` 与 `qfmodel` 为 true。若某账号全部 15 条都 false,
    该 auth 注册 0 个模型 -> `unregisterModelsForAuthFenced` -> 该 auth 在 `/v1/models` 不可见。
    **这是预期行为还是需要保留"最后成功的非空目录"未决策**。antigravity 的对应策略是
    "空目录是权威结果, 会替换"(见 `sdk/auth/antigravity_models.go:42` 注释)。
16. **`chat` 数组元素含非 chat 场景的模型**。参考实现有 `is_chat_model`
    (`qoder_proxy.py:2713-2722`) 过滤 `lite` / `codewise-*` / `completion-*` /
    `*-image-alpha*` / `*-taco-completion`。这些键在两份快照里**均未出现**
    (17 + 14 条全扫描无匹配), 所以**无法确认**动态接口是否会返回它们。
    若不移植该过滤器, 可能把图像/补全模型注册成 chat 模型。
17. **`qoder_encode` 对 GET body 的必要性**。参考实现强调"裸 GET 会 403"(`:3040`)。
    这已验证在 Python 侧, 但 **Go 的 GET-with-body 行为**与 urllib 是否一致未验证
    (传输层可能对 GET body 做不同处理)。建议 P4 用 mock 上游断言 body 确实被发送。
18. **`Registry` 的 `clientProviders` 只存一个 provider 字符串**
    (`model_registry.go:729-733`)。若同一个 `auth.ID` 因配置错误同时被两个 provider
    注册 (理论上不可能, 但 `ReplaceClientModels` 不校验), 后写入者覆盖前者。
    无防护。
19. **`staticModelsJSON` 的 `Qoder` 字段若加, `detectChangedProviders`
    (`model_updater.go:79-120`) 的 `sections` 列表也要加**, 否则远端 catalog 更新不会
    触发 qoder 的 auth 重注册 (`registerModelRefreshCallback`, `service_plugins.go:349-420`)。
    design.md 未提及这一处。
20. **目录拉取的超时策略**。参考实现用 `timeout=15` (`qoder_proxy.py:3061`)。本仓库
    AGENTS.md 规定"超时只允许在凭证获取阶段, 上游连接建立后不得设超时"。
    目录拉取属于"非用户请求的后台探测", 是否适用该豁免条款 **未明确**。
    antigravity 的对应实现完全依赖 `ctx` 取消, 不设 `http.Client.Timeout`
    (`sdk/auth/antigravity_models.go:84-88`)。建议 P4 照抄 antigravity (依赖
    `qoderContext` 生命周期取消), 但这需要 main agent 确认合规解读。
