# OpenCode 免费层自动化同步与网关合规实施计划

本文档汇总了 OpenCode 免费层网关的全部逆向结论、上游接口陷阱，以及**启动时动态拉取最新 Tag / 代理适配 / 工具 Schema 自动更新**的完整方案，供新会话启动后直接执行。

---

## 1. 背景与最新事实 (Background & Status)

OpenCode 官方免费层模型（如 `big-pickle`、`longcat-2.5-preview-free`、`nemotron-3-ultra-free` 等）由 `https://opencode.ai` 网关提供。**自 2026-10-14 起，网关重构升级，旧契约彻底失效，触发 403 `FreeTierError`。**

### 1.1 网关硬性约束清单（任一不满足即 403）

| 校验维度 | 规范与限制 | 违规后果 | 关键踩坑点 |
|---|---|---|---|
| **Base URL** | 必须为 `https://opencode.ai/inference/openai/v1` | 403 / 410 | 旧 `/zen/v1` 已被服务端砍死（必报 403）；裸 `/inference/v1` 报 410 Gone。 |
| **User-Agent** | 必须符合 `opencode/latest/<semver>/cli` | 403 / 426 | `<semver>` 必须是官方已发布的正式版本（当前最新为 `2.0.32`）。开发版/过期旧版本会被 403 拦截或 426 强制升级。 |
| **Session 镜像** | 必须成对携带 4 个同值 Header：`x-opencode-session`、`x-opencode-session-id`、`x-session-affinity`、`x-session-id` | 403 | 格式必须为 `ses_<12hex><14base62>`，前 12 位十六进制编码了真实的递减时间戳（`~current & 0xFFFFFFFFFFFF`），服务端会校验时间戳有效性。 |
| **Request Header** | **严禁携带** `x-opencode-request` | 403 | 只要携带立即判定为第三方客户端注入，触发 403。 |
| **Stream 传输** | Request Body 必须设置 `"stream": true` | 403 | 网关只对流式请求放行免费层。 |
| **6 大核心工具** | Request Body 必须携带官方 6 件套的**字节级精确 Schema**：`shell`, `read`, `write`, `edit`, `glob`, `grep` | 403 | 缺失任意一个（特别是 `write` 或 `read`/`shell`）或使用空占位桩都会被判定为非官方客户端。 |

### 1.2 下游 Claude Code 协议转换约束

出站发给网关的工具名为 OpenCode 官方命名，入站（SSE chunk 与聚合 completion）必须由执行器拦截并映射为 Claude Code 约定的 PascalCase 命名与参数格式：
- `shell` $\rightarrow$ `Bash`
- `read` $\rightarrow$ `Read`，且入参若为 `path` 必须映射为 `file_path`
- `write` $\rightarrow$ `Write`，且入参若为 `path` 必须映射为 `file_path`
- `edit` $\rightarrow$ `Edit`，且入参若为 `path` 必须映射为 `file_path`
- `glob` $\rightarrow$ `Glob`
- `grep` $\rightarrow$ `Grep`

---

## 2. 上游版本探测的关键踩坑点 (Upstream Traps)

要在启动时拉取最新 Tag，必须避开以下 GitHub API 陷阱：

1. **不能直接请求 `releases/latest`**：
   GitHub 的 `https://api.github.com/repos/anomalyco/opencode/releases/latest` 仍停留在 `v1.19.3`（旧 v1 分支）。OpenCode v2 的所有发布（`2.0.0` ~ `2.0.32`）均直接打在 git tag 上，**没有创建 GitHub Release 对象**。
2. **不能直接遍历 `/tags` 前几项**：
   `/tags` 包含大量 `vscode-v*` 插件标签，且 GitHub API 默认不按语义化版本排序。
3. **推荐的三级探测源（高可用链条）**：
   - **第一优先级（官方更新端点）**：
     `GET https://opencode.ai/update/api/latest/cli/npm`
     - 返回结构：`{"channel":"latest","name":"cli","version":"2.0.32",...}`
     - 优点：官方 CDN，无鉴权要求，无 GitHub API 限流风险。
   - **第二优先级（GitHub Git Matching Refs API）**：
     `GET https://api.github.com/repos/anomalyco/opencode/git/matching-refs/tags/v2.`
     - 返回所有匹配 `refs/tags/v2.*` 的标签列表，本地取最大的 semver。
     - 支持携带 `GitHubToken` 避免未认证的 60次/小时 速率限制。
   - **第三优先级（NPM 官方注册表镜像）**：
     `GET https://registry.npmjs.org/@opencode/cli/latest`
     - 返回结构提取 `version: "2.0.32"`，全球 CDN 兜底。

---

## 3. 详细架构设计方案 (Architecture Design)

### 3.1 组件与文件划分

在 `internal/runtime/executor/helps/` 目录下新增并改造以下组件：

```
internal/runtime/executor/helps/
├── opencode_updater.go       # [新增] 启动拉取最新 Tag、后台刷新、代理配置、动态 UA 管理
├── opencode_updater_test.go  # [新增] 单元测试（Mock HTTP、代理检测、版本比较）
├── opencode_tools.go         # [改造] 将 openCodeToolsMap 改为 RWMutex 保护，支持外部文件覆盖与动态更新
└── opencode_tools.json       # [已就绪] 2.0.32 基线 Schema（6件套）作为内置兜底
```

### 3.2 核心状态与线程安全设计

在 `opencode_updater.go` 中维护全局原子状态：

```go
type openCodeRuntimeState struct {
	mu        sync.RWMutex
	version   string        // 当前有效版本，默认 "2.0.32"
	userAgent string        // "opencode/latest/2.0.32/cli"
	lastSync  time.Time     // 上次同步成功时间
}
```

- **初始值**：内置固化版本 `2.0.32`，保证网络离线或探测失败时系统绝对可用。
- **并发读取**：执行器在构造请求头时调用 `helps.CurrentOpenCodeUserAgent()`，通过 `RLock` 读取，零锁竞争。

### 3.3 网络与代理适配规范（严格遵守项目标准）

拉取请求必须支持代理，代理优先级严格遵循项目标准（`sdk/proxyutil`）：
1. **显式配置代理**：
   - OpenCode Provider 配置级别的 `proxy-url`（若有）
   - 全局配置 `cfg.ProxyURL`
   - 使用 `proxyutil.BuildHTTPTransport(proxyURL)` 构建 Transport（支持 socks5/http/https）。
2. **环境变量代理（继承模式）**：
   - 若未配置显式代理，Transport 必须使用 `http.ProxyFromEnvironment`，以正确响应系统的 `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY`。
3. **GitHub Token 注入**：
   - 当请求 GitHub API 时，若 `cfg.GitHubToken` 或系统 `GITHUB_TOKEN` 存在，注入 `Authorization: Bearer <token>` 和 `User-Agent: CLIProxyAPI`。

### 3.4 运行时启动与刷新逻辑

1. **启动时同步（阻塞超时防护）**：
   - 在 `NewOpenCodeExecutor(cfg *config.Config)` 或服务初始化时启动 `StartOpenCodeUpdater(ctx context.Context, cfg *config.Config)`。
   - 使用 `sync.Once` 确保只启动一个 updater。
   - 启动时立即在一个带 5 秒超时的子 context 中执行一次拉取：
     - 若成功，日志记录 `opencode updater: synced upstream version to 2.0.xx`。
     - 若超时或失败，日志输出 warning 并继续使用内置基线，**绝不阻塞服务器主进程启动**。
2. **定时轮询**：
   - 启动后台 goroutine，以 `time.NewTicker(6 * time.Hour)` 定时静默刷新。
   - 当检测到新版本发布时（semver 递增），原子更新 `userAgent`。
3. **外部 Schema 覆盖机制**：
   - 检查工作目录或配置目录下是否存在 `opencode_tools.json`。
   - 存在则优先加载并校验，不存在则回退至 `//go:embed opencode_tools.json`。

---

## 4. 时序流程图 (Sequence Flow)

```
Server 启动 / Executor 注册
         │
         ├── 1. helps.InitOpenCodeTools()
         │      ├── 检查本地是否存在 ./opencode_tools.json (优先)
         │      └── 不存在则加载 //go:embed 内置的 6 件套 Schema (兜底)
         │
         ├── 2. helps.StartOpenCodeUpdater(ctx, cfg) [sync.Once]
         │      │
         │      └── 异步执行 fetchLatestVersion(proxyURL, token)
         │           ├── Source 1: https://opencode.ai/update/api/latest/cli/npm
         │           ├── Source 2: https://api.github.com/repos/anomalyco/opencode/git/matching-refs/tags/v2.
         │           └── Source 3: https://registry.npmjs.org/@opencode/cli/latest
         │           │
         │           └── 若探测到新版本 (如 2.0.33)
         │                └── 原子更新 CurrentUserAgent = "opencode/latest/2.0.33/cli"
         │
         └── 3. 正常接收 Claude Code 请求
                ├── EnsureOpenCodeAnonymousTools 注入 6 件套 Schema
                ├── Header 注入动态 CurrentUserAgent
                └── 回写层将 upstream 结果归一化为 Claude Code 工具格式
```

---

## 5. 新会话开发实施清单 (Implementation Checklist)

在新会话启动后，可直接按以下步骤执行落地：

- [x] **Step 1: 新建 `internal/runtime/executor/helps/opencode_updater.go`**
  - 构建代理 HTTP 客户端：
    ```go
    func buildUpdaterHTTPClient(proxyURL, githubToken string, timeout time.Duration) *http.Client
    ```
    - 若 `proxyURL != ""`，通过 `proxyutil.BuildHTTPTransport(proxyURL)` 构造 transport。
    - 否则使用带 `http.ProxyFromEnvironment` 的 Transport。
  - 实现三级版本探测函数：
    1. `fetchVersionFromOfficial(ctx, client)`: 访问 `https://opencode.ai/update/api/latest/cli/npm` 解析 `.version`。
    2. `fetchVersionFromGitHub(ctx, client, token)`: 访问 `https://api.github.com/repos/anomalyco/opencode/git/matching-refs/tags/v2.`，正则提取最大 `v2.x.y`。
    3. `fetchVersionFromNPM(ctx, client)`: 访问 `https://registry.npmjs.org/@opencode/cli/latest` 解析 `.version`。
  - 实现语义化版本比较：若探测到的版本号大于当前版本，更新 `version` 与 `userAgent`。
  - 实现 `StartOpenCodeUpdater(ctx context.Context, cfg *config.Config)`：带 `sync.Once`，启动时拉一次，并启动后台 ticker（6 小时）。
  - 提供 `CurrentOpenCodeUserAgent() string` 与 `SetOpenCodeVersion(v string)`。

- [x] **Step 2: 改造 `internal/runtime/executor/helps/opencode_tools.go`**
  - 将 `openCodeToolsMap` 的访问加上 `sync.RWMutex` 保护。
  - 改造 `initOpenCodeTools()`：启动时先探测当前目录或配置路径是否存在 `opencode_tools.json`，若有则读取，若无或读取失败则解析 `openCodeToolsJSON`（`//go:embed`）。
  - 导出 `UpdateOpenCodeTools(data []byte) error` 允许运行时热重载。

- [x] **Step 3: 改造 `internal/runtime/executor/opencode_executor.go`**
  - 在 `NewOpenCodeExecutorWithProvider` 中接收 `cfg` 并调用 `helps.StartOpenCodeUpdater(context.Background(), cfg)`。
  - `prepareOpenCodeAuth` 中的 `header:User-Agent` 由原来的常量 `OpenCodeDefaultUserAgent` 改为调用 `helps.CurrentOpenCodeUserAgent()`。

- [x] **Step 4: 单元测试与验证**
  - 在 `internal/runtime/executor/helps/opencode_updater_test.go` 中编写：
    - `TestBuildUpdaterHTTPClient_Proxy`: 验证代理配置与环境变量继承。
    - `TestVersionComparison`: 验证语义化版本比较逻辑。
    - `TestParseOfficialUpdateResponse`: 验证官方端点响应解析。
    - `TestParseGitHubMatchingRefs`: 验证 git refs 标签排序。
  - 验证门禁：
    ```bash
    gofmt -w .
    go test ./internal/runtime/executor/...
    go build -o test-output ./cmd/server && rm -f test-output
    ```
