# 如何管理配置

1. **选择配置后端：** 设置环境变量以选择存储后端（PGSTORE_DSN、GITSTORE_GIT_URL、OBJECTSTORE_ENDPOINT） (`cmd/server/main.go:164-212`)。

2. **加载初始配置：** 使用 `config.LoadConfigOptional` 从选定后端加载配置 (`cmd/server/main.go:249`)。

3. **注册令牌存储：** 根据后端类型注册相应的令牌存储实现（Postgres、Git、对象或文件存储） (`cmd/server/main.go:432-440`)。

4. **处理热重载：** 调用 `UpdateClients` 方法应用配置更改，更新客户端列表和设置 (`internal/api/server.go:815-970`)。

5. **验证配置：** 检查必需字段（端口、认证目录、API 密钥）是否正确设置。

6. **持久化更改：** 通过管理 API (`/v0/management/config.yaml`) 保存配置更改到选定的后端。