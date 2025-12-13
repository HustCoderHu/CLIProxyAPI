# How to Set Up Multiple Accounts

1. **首次登录:** 使用OAuth登录指南为第一个账户创建认证记录。

2. **添加额外账户:** 重复登录流程，系统会自动为每个账户创建唯一的文件名（如 `claude-user1@example.com.json`、`claude-user2@example.com.json`）。

3. **加载所有账户:** 启动时调用 `sdk/cliproxy/auth/manager.go:224-243` 的Load方法从存储中加载所有认证记录。

4. **配置负载均衡:** 系统自动使用RoundRobinSelector在多个账户间分配请求（`sdk/cliproxy/auth/selector.go`）。

5. **监控状态:** 通过Manager的List方法查看所有账户状态，系统会自动跟踪每个账户的可用性和配额状态。