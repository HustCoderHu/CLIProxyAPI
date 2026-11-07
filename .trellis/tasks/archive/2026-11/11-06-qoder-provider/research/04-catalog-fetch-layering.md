# 目录拉取的签名注入架构: 分层边界与落点重估

来源: 只读走查, 2026-11-06. 结论**推翻**了 QODER_RESEARCH_TODO.md 里 "D3 落点 1 (独立 updater)"
的推荐 — 原因见第五节.

## 一、所有权边界

| 关注点 | 持有者 | 证据 |
|---|---|---|
| 凭证 (`*coreauth.Auth` 含 Metadata/Attributes) | `coreauth.Manager` -> `Service.coreManager` | `sdk/cliproxy/service.go:104-105`; `sdk/cliproxy/auth/types.go:53-93` |
| 凭证枚举/变更 | `Manager.List()` / `GetByID()` / `PersistMetadataEntry()` / `WatchAuthChanges()` | `conductor_selection.go:1643,1654`; `conductor_persistence.go:302` |
| 每 auth 发布围栏 | `Manager.PublishIfRegistrationCurrent()` | `conductor_selection.go:1672-1686` |
| catalog 存储 (包级全局) | `internal/registry` | `model_updater.go:32`; `devin_models.go:31` |
| 全局模型注册表单例 | `internal/registry` | `model_registry.go:222-242` |
| catalog 拉取循环 | `internal/registry` 包级 + `StartModelCatalogUpdaters` | `catalog_sources.go:355-375, 394-401` |

**registry 够不到 auth manager**. `internal/registry` 的非测试 import 全集只有标准库 + `internal/misc`:

```
bytes, context, embed, encoding/json, errors, fmt, io, math, net/http, net/url, os,
path/filepath, reflect, slices, sort, strings, sync, sync/atomic, time,
internal/misc, github.com/sirupsen/logrus
```

registry API 里没有任何凭证参数 — `readCatalogSource(ctx, source)` 只接收 URL/路径 (`catalog_sources.go:241-279`).

## 二、现有的反转控制钩子 (全量)

| 钩子 | 方向 | 位置 |
|---|---|---|
| `ModelRefreshCallback` + `SetModelRefreshCallback` | registry -> 外层, push | `model_updater.go:34-60`; 重放缓冲 `:149-163` |
| `ModelRegistryHook` (`OnModelsRegistered/Unregistered`) + `SetHook` | registry -> 外层, push | `model_registry.go:181-184, 398-405, 410-443` |
| `RegisterClientIfEpochCurrent(..., publish func() bool)` | 外层 -> registry, pull-in | `model_registry.go:456-485` |
| 插件 `modelRegistry` 窄接口 | 消费方定义接口 (DIP) | `pluginhost/adapters.go:18-21, 242-260` |
| `RegisterRefreshLeadProvider(provider, factory)` | auth 层向 provider 包询问刷新节奏 | `auth/types.go:1497-1508, 1630-1652` |
| `PostAuthHook` | 凭证创建后回调 | `auth/types.go:28`; `builder.go:183-186` |
| `RequestAuthPreparer` (executor 可选接口) | conductor -> executor | `conductor.go:43-46` |
| `ProviderExecutor.Refresh` (由自动刷新循环调用) | conductor -> executor | `conductor_refresh.go:1369` |

**不存在** "registry 请求其他层去拉取" 这种钩子.
`catalogUpdater.fetch` / `fetchWithSource` 是无 setter 的包内私有字段 (`catalog_sources.go:32-33`).

## 三、Antigravity 先例 (唯一的 "拉取需要凭证" 实现)

全部在 `sdk/cliproxy`, 网络编解码在 `sdk/auth`.

1. **启动触发**: `go s.runAntigravityModelRefresh(ctx)` (`service_lifecycle.go:238`, Home 模式跳过)
2. **扫描循环**: `runAntigravityModelRefresh` (`antigravity_models.go:673-699`), 约每分钟一次,
   调 `refreshAntigravityModels` (`:605-638`) -> 遍历 `s.coreManager.List()` 过滤 provider
3. **按 auth 排队 (合并, 每 auth 一个 worker)**: `queueAntigravityModelRefresh`
   (`antigravity_model_probes.go:31-78`) — clone auth、读 `auth.Metadata["access_token"]`、
   算 route key、并入 `s.antigravityRefreshPending[authID]`
4. **Fetch**: `fetchAntigravityModelCapabilityHints` (`antigravity_models.go:243-356`) —
   取 token、构造 cache key (auth ID/epoch/project/endpoint/proxy/UA)、singleflight、
   `beginAntigravityAccountProbe` (`antigravity_model_probes.go:129-203`, 内部订阅 `WatchAuthChanges`)
5. **网络编解码在 `sdk/auth`**: `sdk/auth/antigravity_models.go:65-95`
6. **投影**: `antigravityModelsForHintsWithConfig` (`antigravity_models.go:513-528`)
7. **带围栏发布**: `applyAntigravityModelHints` (`:549-600`) 重新校验 `coreManager.GetByID`,
   然后 `reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)` (`:579`)
8. **发布后对账**: `ReconcileRegistryModelStates` + `RefreshSchedulerEntry` (`:596-599`)
9. **冷启动播种**: `service_models.go:131-147` 用缓存 hints 走同一围栏路径, 再触发异步探测
10. **持久化**: `persistAntigravityCapabilityHints` (`:176-216`) 写 `auth.Metadata`

分层技巧: `sdk/cliproxy` 调用具体的 `registry.GetGlobalRegistry()` (`:577`) 而非接口 —
因为 `sdk/cliproxy/model_registry.go:23-38` 定义的 `ModelRegistry` 接口**不含** `ReplaceClientModels`.

## 四、Devin updater 完全不可注入

```go
// internal/registry/catalog_sources.go:20-35
type catalogUpdater struct {          // 包私有类型
    ...
    fetch           func(context.Context, string) ([]byte, error)          // 私有字段
    fetchWithSource func(context.Context, string) ([]byte, string, error)  // 私有字段
    publish         func([]byte) ([]string, error)                         // 私有字段
}
```

Devin 实例是未导出的包级全局 (`:366-375`). 后果:
- `catalogUpdater`, `configure`, `refresh`, `refreshOnDemand`, `fetchCatalog` 全部未导出
- Devin 导出的只有 `ValidateDevinModelsJSON` / `GetDevinModels` / `LookupDevinModel` /
  `WithDevinBuiltins` / `Get/Snapshot/Revision` / `StartDevinModelsUpdater` (不接受 fetcher)
- `loadDevinModelsFromBytes` (真正的发布入口) 未导出 — 外部包**连推送 payload 都做不到**
- `fetchDevinModelsFromRemote` / `tryRefreshDevinModels` 是**死代码**, 无测试外调用者

**额外风险**: `fetchCatalog` 读 `u.fetchWithSource` 时**不持 `u.mu`** (`:37-43`).
任何导出的 setter 若直接改该字段, 会与 `configure` 启动的刷新 goroutine 竞争.

## 五、三个落点的实际评估

### 落点 (a) — 在 registry 加 fetch 钩子 ❌ 架构错误

需要新增: 回调类型 + setter + 同步改造 (copy-on-write 或 atomic.Value) + 可能还要扩展
`CatalogSources` / `Validate` / `effectiveCatalogSources` / `RefreshModelCatalogsWithPolicy` /
管理 handler 的三键 JSON (`model_definitions.go:44-58`).

致命问题: **只带 `source string` 的钩子无法传达"用哪条凭证签名"**.
外层只能通过带外方式塞一个"当前凭证"进 registry — 这就是跨层隐式契约,
正是 `.trellis/spec/guides/cross-layer-thinking-guide.md` 警告的泄漏抽象.

另外: `startModelCatalogUpdaters` 在 `service_lifecycle.go:57` 执行,
比 `registerModelRefreshCallback` (`:236`) 早约 180 行. 钩子若在 `Run` 内安装会错过启动拉取.

### 落点 (b) — sdk/cliproxy 的 Service 方法 (Antigravity 式) ✅ 推荐

**按 auth 发布时零新增导出 API**:
- `registry.GetGlobalRegistry()` 返回具体 `*registry.ModelRegistry` (`model_registry.go:226`)
- `ReplaceClientModels` 已导出 (`:493-501`)
- `sdk/cliproxy` 已 import `internal/registry` (`model_registry.go:7` 等)
- `antigravity_models.go:577-579` 已在跨包使用

若确实需要**跨账号共享目录**, 才需在 registry 加**一个**导出函数:
`PublishQoderModelsJSON(data []byte) (bool, error)`, 包装一个 Devin 式的
`loadQoderModelsFromBytes`. 但不要导出 `catalogUpdater` 本身.

本层已有的全部机制: `List()/GetByID()`, `WatchAuthChanges`, `PersistMetadataEntry`,
`PublishIfRegistrationCurrent`, 以及发布后处理:
`applyExcludedModels` (`service_models.go:668`), `applyModelPrefixes` (`:716`),
`applyOAuthModelAliasForAuth` (`:1246`), `applyOAuthSettingsForAuth` (`:1405`),
`modelRegistrationFence` (`model_registry.go:55-148`).

分层: 合规, `cliproxy -> registry` 是既有方向, 无新边.

### 落点 (c) — executor 层 ❌ 实践上被封死

- **import 循环**: `sdk/cliproxy` 已 import `internal/runtime/executor` (`service_auth.go:10` 等),
  反向非法. 因此 executor 无法复用 `applyExcludedModels` / `applyModelPrefixes` /
  `applyOAuthSettingsForAuth` / `modelRegistrationFence` — 四个都要重写, 正是跨层指南警告的
  "Each Consumer Parses The Same Payload" 错误
- **目录规则**: AGENTS.md:52 与 `.trellis/spec/backend/directory-structure.md:41` 都规定
  `internal/runtime/executor/` 只放 executor 与其单测
- **会成为首个写 registry 的 executor**: 现有 executor 只读
  (`claude_executor_cloaking.go:4282,4285`; `devin_executor.go:434`); 唯一的非 cliproxy 写入者是 pluginhost
- **`Refresh` 不会自动跑**: 只有 `ProviderRefreshLead` 非 nil 才调度
  (`conductor_refresh.go:192-194`; `auto_refresh_loop.go:502`)

## 六、import 方向与循环分析

`internal/registry` 不 import 任何 `sdk/` — 已用 `go list -deps` 确认.

若加 `internal/registry -> sdk/cliproxy` 会**必然成环**, 两条独立路径:

1. `internal/registry` -> `sdk/cliproxy` -> `internal/registry`
   (`sdk/cliproxy/model_registry.go:7`, `antigravity_models.go:15`, `service_models.go:11` 等)
2. `internal/registry` -> `sdk/cliproxy/auth` -> `internal/registry`
   (`conductor_selection.go:16`, `conductor_models.go:12`, `scheduler.go:12`)

第三条间接路径: `sdk/cliproxy/auth` -> `internal/config` -> `internal/registry`
(`internal/config/config_types.go:11`, `model_catalogs.go:3`).

反过来 `cliproxy -> registry` 与 `executor -> registry` 合法且广泛使用 (71 个非测试文件).

## 七、最终推荐: 落点 (b), 即 per-auth 探测

**推翻 QODER_RESEARCH_TODO 的 "落点 1 + 落点 3 叠加"**. 理由按优先级:

1. **零分层例外**. 需要的调用已存在且已跨包使用:
   `registry.GetGlobalRegistry().ReplaceClientModels(...)` (`antigravity_models.go:577-579`)
2. **凭证留在拥有它的层**. Antigravity 的 keying/fencing/backoff 机制
   (`antigravity_models.go:97-112`; `antigravity_model_probes.go:31-117, 129-222`)
   可直接套用于 Qoder 的"每会话 RSA 包裹 AES 密钥" — 绑到 route key, token 轮换即重建
3. **(a) 对本问题是架构性错误**: 只带 source 的钩子无法表达用哪条凭证
4. **(c) 实践不可行**: import 循环 + 目录规则 + 首个写入者
5. **数据本身就要求 per-auth**: `enable` 是按账号的 (D8), 共享目录仍需 per-auth 叠加层,
   直接做 per-auth 避免两套并存

### 具体落点

- 新增 `sdk/cliproxy/qoder_models.go` + `qoder_model_probes.go`,
  克隆 Antigravity 的队列/epoch/围栏模式.
  cache key 应含: `auth.ID`, `RegistrationEpoch`, provider (`qoder` vs `qoder-intl`),
  endpoint, `access_token` 的 hash (参照 `antigravityCapabilityKeyForRoute` `:104-112`
  与失败 key `:258`)
- 新增 `sdk/auth/qoder_models.go`: 纯 COSY/RSA/AES 编解码 + `FetchQoderModels(ctx, req)`.
  与 Antigravity 同样拆分 (`sdk/auth/antigravity_models.go:65` 被
  `sdk/cliproxy/antigravity_models.go:363` 调用).
  `sdk/auth` 只 import `sdk/proxyutil` 与 moego, 不成环
- 在 `baselineExecutorAuths` (`service_executors.go:211-229`) 与 provider switch
  (`:300-354`) 注册 `qoder` **和** `qoder-intl`
- 在 `service_models.go` 的 provider switch 加 `case "qoder", "qoder-intl":`,
  模板是 `antigravity` 分支 (`:131-147`) — 冷启动用缓存播种 + 触发异步探测
- 用 `coreManager.PersistMetadataEntry` (`conductor_persistence.go:302`) 持久化上次目录,
  参照 `persistAntigravityCapabilityHints` (`antigravity_models.go:176-216`)
- **不要**把 qoder 加进 `validateModelsCatalog` 的 required sections (`model_updater.go:222-246`)
- **不要**给共享 `models.json` 的 carry-forward 加 qoder 段 (`catalog_sources.go:323-343`)
