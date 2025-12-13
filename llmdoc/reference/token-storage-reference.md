# Token Storage Reference

## 1. Core Summary

令牌存储系统使用JSON文件持久化认证数据，支持访问令牌、刷新令牌、过期时间和元数据存储。系统提供自动刷新机制和运行时状态管理。

## 2. Source of Truth

- **认证数据结构:** `sdk/cliproxy/auth/types.go:14-63` - 定义Auth结构体，包含所有认证相关字段。
- **存储接口:** `internal/auth/base.go` - TokenStorage接口定义，各提供商实现具体存储逻辑。
- **持久化管理:** `sdk/cliproxy/auth/manager.go:1132-1147` - persist方法处理认证记录的保存。
- **过期时间解析:** `sdk/cliproxy/auth/types.go:211-271` - 从元数据中提取令牌过期时间的逻辑。
- **刷新机制:** `sdk/cliproxy/auth/manager.go:1149-1478` - 自动刷新后台任务和刷新逻辑。