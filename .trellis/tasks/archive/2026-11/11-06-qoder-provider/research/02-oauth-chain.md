# OAuth authenticator 接入链路: 文件清单与陷阱

来源: 只读走查 `E:/docs/github_repo/CLIProxyAPI`, 2026-11-06. 以 `codebuddy-cn` 为样板.
目标: 双 provider `qoder` (CN) + `qoder-intl`, 每个硬编码 provider 列表都要加**两条**.

## 一、必须改的 16 个点

### 1. 低层 OAuth 客户端 (新包)

`internal/auth/<provider>/<provider>.go`, 参考 `internal/auth/codebuddycn/codebuddycn.go`.

必须导出:
- 常量: 双区各自的 openapi / gateway / website 基址, `UserAgent`, 轮询周期
- `TokenData{AccessToken, RefreshToken, TokenType, ExpiresIn, ExpiresAt}`
- `DeviceCode{State, AuthURL, Interval, ExpiresIn}`
- `NewClient(cfg)` / `NewClientWithProxyURL(cfg, proxyURL)` (后者用 `util.SetProxy`, 见 `codebuddycn.go:74`)
- `StartDeviceFlow(ctx)` / `WaitForAuthorization(ctx, device)` / `Refresh(ctx, refreshToken)`
- 包内 `newClient(...)` 测试缝 (参考 `:78`)
- 配套 `_test.go`

### 2. Authenticator (sdk 层)

`sdk/auth/<provider>.go`, 参考 `sdk/auth/codebuddycn.go`:

| 元素 | 参考行 |
|---|---|
| `<provider>RefreshLead` 包级变量 | `:16` |
| `Provider() string` | `:25` |
| `RefreshLead() *time.Duration` | `:28` |
| `New<Provider>Authenticator() Authenticator` | `:22` |
| `Login(ctx, cfg, opts)` | `:31-73` |

`Login` 产出的 `*coreauth.Auth` 必须含:
`ID` 与 `FileName` = `<provider>-<UnixMilli>.json`, `Provider`, `Label`,
`Metadata["type"]` = provider key (reload 时靠它推导 provider), `auth_kind=oauth`,
`access_token` / `refresh_token` / `token_type` / `expires_in` / `base_url` / `timestamp` / `expired`,
`Attributes{auth_kind, base_url}`. **不设 `Storage`** — 纯 metadata auth.

### 3. refresh 注册表 [易漏]

`sdk/auth/refresh_registry.go:9-20` 加 `registerRefreshLead(...)`.
不加则 `ProviderRefreshLead` 返回 nil, 该 auth **永不自动刷新** (`sdk/cliproxy/auth/types.go:1630-1651`).
同步更新 `sdk/auth/refresh_registry_test.go:8-24`.

### 4. 两个独立的 auth manager [易漏, 关键]

| 位置 | 用途 |
|---|---|
| `sdk/cliproxy/service_auth.go:21-35` (`newDefaultAuthManager`) | 服务运行时 |
| `internal/cmd/auth_manager.go:13-28` | CLI 命令行 |

两处都要注册. 漏一处则 `--<provider>-login` 报 "authenticator not registered".
调用方: `sdk/cliproxy/builder.go:232`, `sdk/cliproxy/service_lifecycle.go:148`.

### 5. 管理 API handler

`internal/api/handlers/management/auth_files_provider_oauth.go` 新增
`func (h *Handler) Request<Provider>Token(c *gin.Context)`, 参照 `:1283-1354` 的结构:
1. `PopulateAuthContext` + `oauthSessionProxyURLFromContext`
2. `StartDeviceFlow` -> `device.State` -> `ValidateOAuthState` -> `RegisterOAuthSessionWithProxy`
3. goroutine: `watchOAuthSessionCancel` (`:1357-1374`) + `WaitForAuthorization`
4. 构建 metadata-only `coreauth.Auth`
5. `h.commitAndCompleteBuiltinOAuthSession(...)` (`:536-621`)
6. 返回 `{status, url, state, flow:"device", expires_in}`

### 6. 路由 (两套)

| 版本 | 位置 | 内容 |
|---|---|---|
| v0 遗留 | `internal/api/server_management.go:196-204` | `mgmt.GET("/<provider>-auth-url", s.mgmt.Request<Provider>Token)` |
| v8 | `internal/api/handlers/management/auth_files_v8.go:403-428` | `case "<provider>": h.Request<Provider>Token(c)` |

v0 路径名**必须**等于 provider key — 回调时从路径反推
(`auth_files_oauth_callback.go:157-171`).

### 7. CLI 登录命令

- `internal/cmd/<provider>_login.go`: `Do<Provider>Login(cfg, options)`, 参考 `internal/cmd/codebuddycn_login.go:13-33`
- `cmd/server/main.go`: `:116` 变量, `:147` flag, `:663` commandMode, `:740` 分支
- `cmd/server/main.go:968-976` `argvFlagConsumesValue` **必须**加新 flag [既有缺陷: codebuddy-cn-login 就漏在这里]

### 8. executor [强制]

`internal/runtime/executor/<provider>_executor.go` 实现 `ProviderExecutor`.
**`Refresh` 是唯一能让 token 轮换的路径** — `sdk/auth.Authenticator` 接口**没有** Refresh 方法
(`sdk/auth/interfaces.go:24-29` 只有 `Provider`/`Login`/`RefreshLead`).

刷新调度链:
1. `Service.Start` -> `coreManager.StartAutoRefresh(ctx, 15m)` (`sdk/cliproxy/service_lifecycle.go:105`)
2. `nextRefreshCheckAt` (`auto_refresh_loop.go:404-521`) 用 `ProviderRefreshLead` 算到期 (`:502`)
3. `exec.Refresh(ctx, base.Clone())` (`conductor_refresh.go:1369`)
4. `UpdateRefreshedAuth` -> `persistLocked` (`conductor_lifecycle.go:546`) -> `store.Save`
   — `FileName` 决定覆盖哪个文件, 自动完成
5. 另需 `s.coreManager.RefreshSchedulerEntry(targetAuth.ID)` (`sdk/cliproxy/service_auth.go:638`)

### 9. executor 注册

`sdk/cliproxy/service_executors.go`:
- `baselineExecutorAuths` (`:211-242`) 加两个 provider
- `registerExecutorForAuth` 的 switch (`:300-369`) 加两个 case

### 10. 常量

`internal/constant/constant.go:28-29` 加 `QoderCN` / `QoderINTL`.

### 11. provider 名归一化

`internal/api/handlers/management/oauth_sessions.go:645-664` `NormalizeOAuthProvider`.
纯 `[a-z0-9-]` 的 key 靠 plugin fallback (`:673-688`) 也能通, 但**别名必须显式加**.

### 12. 无身份 provider 标记 [易漏]

`internal/api/handlers/management/auth_files_provider_oauth.go:423-426`
`isIdentitylessOAuthProvider` 现含 `kimi`, `kimi-ai`, `codebuddy-cn`.
若 Qoder 的 auth 文件不含 `email`/`account_id`, **必须**加进去, 否则带 `?name=` / `auth_index=`
的定向重新登录会在 `:466` 报 "Authenticated account does not match target credential".

Qoder 的 `/api/v1/userinfo` 返回 `id`/`name`/`user_type`/`organization_id`, 可写入 `account_id`,
但为稳妥仍应加入该白名单.

### 13. auth 文件合成

`internal/watcher/synthesizer/file.go:238-246` 的 codebuddy 分支是样板:
若 executor 需要 `attributes["base_url"]` / `auth_kind`, 加同构块.
provider 本身由 `metadata["type"]` 推导 (`:109-111`), 不需要单独的 parser.

### 14. 认证文件落盘

`FileTokenStore.Save` (`sdk/auth/filestore.go:75-175`), 当 `record.Storage == nil` 时走
`:130-157`: 注入 `metadata["disabled"]`, `json.Marshal(auth.Metadata)`, 写 `<AuthDir>/<FileName>` 0600.

CodeBuddy 的 auth JSON 形态 (作为 Qoder 模板):
```json
{
  "type": "codebuddy-cn",
  "auth_kind": "oauth",
  "access_token": "...",
  "refresh_token": "...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "base_url": "https://copilot.tencent.com/v2",
  "timestamp": 1762345678901,
  "expired": "RFC3339",
  "disabled": false
}
```

Qoder 需额外字段: `uid`, `nickname`, `domain`, `realm`(可选, provider 名已表达),
`user_type`, `organization_id`, `organization_name`, `source`(oauth/pat), `personal_token`.

### 15. 模型注册 (B 组, 见 design.md)

`sdk/cliproxy/service_models.go:91` switch 加两个 case;
`internal/registry/model_definitions.go` 加字段/getter/channel;
`internal/registry/models/models.json` 加 section;
`model_updater.go:104` 与 `catalog_sources.go:335-337` (仅落点 2 需要).

### 16. config (A 组, 见 design.md)

`internal/config/config.go:165-166` 加两个字段;
`config_types.go` 加两个 Key 类型;
`config_v8.go:67-73` `v8KeyFamilies` 加两行;
`config_normalization.go` 两个 sanitizer;
`config_load.go:312-313` 与 `parse.go:114-129` **两处都**注册.

## 二、不可直接套用的 provider 特有逻辑

| 特性 | CodeBuddy 实现 | Qoder 差异 |
|---|---|---|
| TLS 指纹 | `helps/codebuddycn_utls.go` (`:36`, `:144`), 经 `httpClientFactory` 挂载 | 需自建 JA3 profile, 或先不挂 |
| 上游特有头 | `X-Domain` / `X-Product: SaaS` / `X-No-Authorization` (`codebuddycn.go:286-301`) | Qoder 是 COSY 头族, 完全不同 |
| 刷新方式 | header `X-Refresh-Token` + body `{}` | **按 token 前缀分流**: `drt-` -> `/api/v1/deviceToken/refresh`; `jrt-` 或 PAT -> `/api/v1/jobToken/refresh` (`qoder_accounts.py:2198-2213`) |
| pending 语义 | HTTP 200 + `{"code":11217}` | HTTP 404 / 202 表示未授权 (`qoder_accounts.py:4113-4126`) |
| 轮询传输 | GET `?state=` | GET `?nonce=&verifier=&challenge_method=S256` |
| 登录 URL | 服务端下发 | 客户端自拼: `<website>/device/selectAccounts?...` (`:4040-4065`) |
| WorkBuddy 归一化 | `metadata_keys.go:64-170` 硬编码 | 不适用 |

**Qoder 的 token 前缀分流必须在 executor 的 `Refresh` 内实现**, 不能只用一个 refresh endpoint.

## 三、双 provider 的额外负担

上表每个硬编码 provider 列表都要加**两条**. 若共用一个 authenticator 实现,
用 `LoginOptions.Metadata` 携带 `realm`/`platform` (接口定义 `sdk/auth/interfaces.go:14-22`,
用法先例 `sdk/auth/codex_device.go:58-61`).

`executorKeyFromAuth` (`conductor_execution.go:1861-1891`) 硬编码了 `kimi.com`->`kimi` 之类的归一化.
`qoder` / `qoder-intl` 若需要归一化, 改这里.

## 四、Qoder 登录流程的具体形态

来自 `qoder_accounts.py`:

1. **PKCE S256**: `verifier, challenge = _make_pkce()`; `nonce` CN 带横线 / INTL 32-hex (`:4045-4050`)
2. **授权 URL**: `<website>/device/selectAccounts?challenge=..&challenge_method=S256&nonce=..`
   - CN 额外带 `redirect_uri` + `client_id` + `machine_id`
   - INTL 只带 `client_id` + `machine_id` (`:4055-4062`)
3. **轮询**: `GET <openapi>/api/v1/deviceToken/poll?nonce=..&verifier=..&challenge_method=S256`
   - 404 / 202 = pending; 成功返回 `{token, refresh_token, user_id, ...}` (`:4078-4160`)
4. **补全身份**: `GET <openapi>/api/v1/userinfo` (Bearer), 取 `id`/`name`/`user_type`/`organization_id`/`organization_name`
5. **PAT 导入**: `POST /api/v1/jobToken/exchange` with `{personal_token}`

双区 client_id:
- CN: `1c5e33e1-364d-4ce6-b02c-acaa81274a5c`, redirect `qoder-work-cn://`, UA `QoderWork/1.1.75`
- INTL: `e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb`, redirect `qoder://aicoding.aicoding-agent/login-success`, UA `Qoder/1.1.75`

网关候选:
- CN: `https://gateway.qoder.com.cn`
- INTL: `https://api3.qoder.sh` -> `api2` -> `api1`
