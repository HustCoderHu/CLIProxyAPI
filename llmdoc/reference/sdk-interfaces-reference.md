# SDK Core Interfaces Reference

## 1. Core Summary

本文档提供了 SDK 中核心接口的快速参考，包括认证、执行器、翻译器和访问控制相关的接口定义。

## 2. Source of Truth

- **认证接口:** `sdk/auth/interfaces.go` - 定义 Authenticator 接口和登录选项
- **执行器接口:** `sdk/cliproxy/auth/manager.go` - ProviderExecutor 接口定义
- **翻译器类型:** `sdk/translator/types.go` - RequestTransform 和 ResponseTransform 函数类型
- **访问控制接口:** `sdk/access/registry.go` - Provider 接口和工厂函数
- **核心类型定义:** `sdk/cliproxy/executor/types.go` - Request/Response/Options 类型
- **认证数据结构:** `sdk/cliproxy/auth/types.go` - Auth、ModelState、QuotaState 结构
- **客户端提供者:** `sdk/cliproxy/types.go` - TokenClientProvider 和 APIKeyClientProvider 接口
- **格式标识:** `sdk/translator/format.go` - Format 类型定义