# 存储后端

> CLIProxyAPI 如何持久化配置、token 与密钥。（项目不使用 ORM，而是 `internal/store` 背后的可插拔存储后端。）

---

## 后端

| 后端 | 包 | 说明 |
|------|------|------|
| 文件（默认） | `internal/store` | 本地文件系统，位于 `auths/`、配置文件。 |
| Postgres | `internal/store/postgresstore.go` | 通过 `PGSTORE_*` 环境变量启用。`postgres_cooldown_store.go` 记录冷却。 |
| Git | `internal/store/gitstore.go` | 通过 `GITSTORE_*` 环境变量启用。版本化的配置快照。 |
| 对象存储 | `internal/store/objectstore.go` | 通过 `OBJECTSTORE_*` 环境变量启用。 |
| Bolt（KV） | `internal/kvstore` | 嵌入式键值存储（`bolt_store.go`），由 controller 管理。 |

后端选择由环境变量驱动，而非调用点处的代码分支。新的存储逻辑应放在 `internal/store`（KV 需求放 `internal/kvstore`），不要散落到 handlers 里。

## 密钥处理（关键）

`internal/store` 是配置中明文密钥被哈希/seed 的地方。注意 `config_seed_test.go` 强制的契约：

- `TestPostgresStoreSeedHashesPlaintextManagementSecret` 及对应的 `ObjectTokenStore` 用例——seed 一个 store 时**必须**对明文管理密钥做哈希，绝不原样持久化。
- `TestPostgresStoreSeedRefusesConfigThatKeepsPlaintextSecret` / `TestObjectTokenStoreSeedRefusesConfigThatKeepsPlaintextSecret`——保留明文密钥的配置会被拒绝。

新增存储后端时，请复制这一契约：seed 时哈希、拒绝携带明文密钥的配置。这是项目首要的密钥安全不变量。

## 访问模式

- 通过 `internal/store` / `internal/config` 解析密钥，不要随手读原始文件。
- 可能竞态的读写路径（冷却、token 存储）使用 `internal/kvstore/controller.go` 已有的 controller；不要自己手搓锁。

## 仅本地模式

`internal/store/disabled_login_save_test.go` 确认：当远端 store 禁用时，登录仍能到达本地 token 存储，且保存不会被错误短路。保持这一不变量：禁用远端后端不得破坏本地 token 持久化。

## 禁止事项

- 在 `internal/store` 之外引入 ORM / 裸 `database/sql` 查询。
- 持久化明文管理密钥。
- 直接读 auth 文件而非经 `internal/store` / `internal/config`。
- 在 `internal/kvstore/controller.go` 已提供访问的地方手搓互斥锁。
