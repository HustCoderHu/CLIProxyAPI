# Qoder provider 接入: 双 provider + 拉取目录

## 需求背景

### 起因

用户要在 CLIProxyAPI 中接入 Qoder (AI 编码助手, 有国内版与国际版两个独立区域),
作为可用 provider 之一, 与已有的 claude / codex / gemini / kimi / codebuddy-cn 等并列.
目标不是做兼容层, 而是**完整支持 Qoder 的协议**, 包括其非标准的 COSY 签名与请求体编码.

### 为什么参考 qoder2api-hub

上游 Qoder 没有公开 API 文档, 协议靠逆向官方客户端得到.
`E:/docs/github_repo/qoder2api-hub` 是一个运行中的 Python 网关,
已完整逆向并验证了: 自定义 Base64、COSY 签名、设备授权、PAT 交换、
请求体骨架、SSE 信封、错误码、目录拉取、双区差异.

本任务**不复用其代码**, 只对齐协议行为. 所有结论都需在本仓库重新验证 (黄金向量测试).

### 前置工作

本任务之前已有一轮只读调研, 产出 `QODER_RESEARCH_TODO.md`:
- A 组 (高风险) / B 组 (中风险) / C 组 (收尾) 共 12 项核实
- 结论: v8 config 需显式表项、store 后端 schema 无关、Home 模式 provider 不透明、
  usage 记账需显式 marker、pluginhost 内置优先、目录刷新需 carry-forward、
  `/v1/models` 是并集、管理前端硬编码 OAuth 按钮、thinking 回退依赖模型 Type、
  排队需 executor 内处理、argv flag 有既有缺陷

该文档仍是协议层事实的权威来源. 本次的架构结论以 `research/` 下的 6 份文档为准
(其中 04 与 06 推翻了 QODER_RESEARCH_TODO 的部分结论).

### 与已有 provider 的关系

| provider | 可复用 | 不可复用 |
|---|---|---|
| codebuddy-cn | executor 体量模板、Execute 强制流式 + 聚合、auth 接入链路 16 步 | TLS 指纹、请求头、刷新方式、信封格式 |
| antigravity | per-auth 目录探测、epoch 围栏、缓存 key、持久化 | 目录格式 (对象 vs 字符串信封) |
| opencode | chunk 通道包装、空 delta 清理形状 | — |
| devin | 独立 catalog 的错误处理语义 (仅参考) | 不可注入, 只能看不能复用 |

### 已知的架构冲突

Qoder 与现有架构有三处不契合, 是本任务工作量的大头:

1. **COSY 签名与编码后 body 强绑定** — 签名覆盖 `qoder_encode(body)` 的输出,
   不是明文 JSON. 因此无法用"翻译后交给通用 executor 发送"的模式, 必须自建发送路径.
2. **SSE 信封套内层 JSON 字符串** — 全仓库没有任何 executor 解包过这种结构,
   现有 `OpenAICompatExecutor` 的帧处理会在 6 个位置误判 (见 research/05).
3. **双 provider 不能隔离同名模型 ID** — 注册表以 model ID 为唯一键,
   两个 provider 注册同名模型会合并成一条, 请求时跨区混选.
   必须靠 `prefix` + `routing.force-model-prefix` 规避.

## Goal

把 Qoder (CN / INTL) 接入为两个独立 provider, 使用自建 executor 处理 COSY 签名协议,
模型目录走上游拉取并按账号 `enable` 过滤.

## 决策 (已拍板)

| 编号 | 决策 | 值 | 拍板时间 |
|---|---|---|---|
| D1 | 双区表达 | 双 provider: `qoder` (CN) + `qoder-intl` | 2026-11-05 |
| D2 | 仓库合规 | 自用分支, 不合 main | 2026-11-05 |
| D3 | 模型目录 | 拉取. **落点是 per-auth 探测 (Antigravity 模式)**, 不是独立 updater | 2026-11-06 修正 |
| D4 | 时间约束 | 服务未上线, 允许彻底重构, 不留兼容包袱 | 2026-11-06 |
| D5 | provider 隔离 | `prefix` + `routing.force-model-prefix` | 2026-11-06 |
| D6 | thinking 校验 | 严格 (不可钳制), 即不把 `qoder` 加进 `isKnownProviderFamily` | 2026-11-06 |
| D7 | 协议样字段 | 保留 9 个未验证字段 (`chat_task`/`source`/`version` 等) | 2026-11-06 |
| D8 | 系统提示词 | 中性兜底 (`"You are a helpful assistant."`), 不内嵌 12.3KB 模板. 客户端给 system 则原样替换 | 2026-11-06 |

### D3 的修正记录

初次调研推荐 "独立 catalog updater (Devin 模式) + 按账号 enable 叠加".
复核后推翻: `catalogUpdater` 是包私有类型, `fetchWithSource` 是无 setter 的私有字段,
`loadDevinModelsFromBytes` 也未导出 — 外部包**连推送 payload 都做不到**;
且只带 `source string` 的钩子无法传达"用哪条凭证签名", 会引入跨层隐式契约.
改用 Antigravity 的 per-auth 探测 (全部在 `sdk/cliproxy` 内, 零分层例外).

## 范围

### 包含

1. **协议层**: Qoder 自定义 Base64 编码器、COSY 签名 (RSA-PKCS1v15 包裹 AES-128 会话密钥 + MD5 Bearer)。
2. **executor**: 自建 `QoderExecutor`, 不复用 `OpenAICompatExecutor` — 因为签名与编码后的 body 强绑定,
   且上游 SSE 是"信封套内层 chunk"结构。
3. **auth**: OAuth 设备授权 (PKCE S256) + PAT 导入, 双区各自的 client_id / 网关 / 域名。
4. **目录**: 拉取 `/algo/api/v2/model/list?Encode=1`, 按账号 `enable` 过滤, 双区独立目录。
5. **config**: `qoder-api-key` / `qoder-intl-api-key` 两个 v8 段。
6. **provider 隔离**: prefix + `routing.force-model-prefix`, 消除同名模型 ID 冲突。

### 不包含

- 每日签到 / 额度查询 / campaign 活动 (qoder2api-hub 有, 本仓库不需要)。
- Anthropic / Responses 入口的 Qoder 特殊处理 (走通用 translator)。
- 上游目录的自动发现公式或纠偏逻辑。
- TLS 指纹伪装 (先不做, 被风控再评估)。
- 修复与本任务无关的既有缺陷 (codebuddy/opencode 的 accounting marker、
  v8 auth_index 缺口、`argvFlagConsumesValue` 漏项) — 单列, 不混入本任务。

## 核心约束

| 编号 | 约束 | 影响 | 依据 |
|---|---|---|---|
| C1 | 双 provider 不能隔离同名模型 ID | 必须配 prefix + force-model-prefix | research/04 |
| C2 | 拉取需要 COSY 签名; registry 层够不到凭证 | 落点必须在 `sdk/cliproxy` | research/04 |
| C3 | INTL 侧 15/17 模型 `enable=false` | 必须按账号过滤 | research/03 |
| C4 | `RequestToFormat` 对复用 compat 的 executor 不可达 | 不要为该 switch 加 case | QODER_RESEARCH_TODO D6 |
| C5 | 403 排队错误会正常冷却账号 | 排队必须在 executor 内处理 | research/04 |
| C6 | upstream 连接建立后不得设超时 | 排队等待与流读取都不能用 `http.Client.Timeout` | AGENTS.md |
| C7 | 既有 SSE 循环无法解包嵌套 body 字符串 | 必须自建帧循环 | research/05 |

## Acceptance Criteria

- [ ] `qoder` 与 `qoder-intl` 可作为独立 provider 出现在 `/v1/models`, 模型 ID 不互相覆盖。
- [ ] COSY 签名通过黄金向量测试 (与 Python 参考实现逐字节比对 6 个确定性中间量)。
- [ ] OAuth 设备授权可完成登录, 生成的 auth 文件可被 reload 后重新加载。
- [ ] PAT 导入可用。
- [ ] 流式与非流式请求均能正确解包上游 SSE 信封。
- [ ] 模型目录可拉取, `enable=false` 的模型不出现在列表中。
- [ ] `thinking_config` 映射正确: 有该字段的模型可调档, 无该字段的模型参数被剥离。
- [ ] 403 + 10605 排队时 executor 内等待重试, 不冷却账号。
- [ ] `go build ./cmd/server` 通过; `go test ./...` 无新增失败。
- [ ] 管理 API 的 v8 保存往返不丢 `qoder-api-key` / `qoder-intl-api-key` 段。

## Notes

- 参考实现 `E:/docs/github_repo/qoder2api-hub` 为只读, 不复用其代码, 只对齐协议行为。
- 详细设计见 `design.md`, 执行计划见 `implement.md`。
- 调研原始材料见 `research/01-06-*.md`, 其中 04 与 06 推翻了早期结论。
- 已知既有失败 (不对比, 不修):
  - `TestClaudeCloakedDateReminderPersistsAcrossRestartWithLocalBBoltKV` (internal/runtime/executor):
    Windows 上 bbolt 句柄未在 TempDir 清理前释放, 干净 HEAD worktree 可复现。
  - `TestServiceHeartbeatFailoverDoesNotDrainBlockingScope` (sdk/cliproxy): 偶发
    (`new subscription did not start while the old scope remained active`),
    干净 HEAD worktree 同样复现。
