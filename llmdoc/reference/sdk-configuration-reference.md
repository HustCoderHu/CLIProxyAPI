# SDK Configuration Reference

## 1. Core Summary

SDK 配置通过 Builder 模式进行，支持自定义认证管理器、访问管理器、客户端提供者、监听器工厂和服务器选项。配置项包括必需的配置实例和路径，以及可选的各种组件覆盖。

## 2. Source of Truth

- **Builder 配置:** `sdk/cliproxy/builder.go` - 所有 WithXXX 方法的参数说明
- **服务配置:** `sdk/cliproxy/service.go` - Service 结构体字段和默认值设置
- **认证配置:** `sdk/cliproxy/auth/manager.go` - 重试配置和刷新间隔设置
- **访问提供者配置:** `sdk/access/registry.go` - ProviderFactory 和配置结构
- **令牌存储配置:** `sdk/auth/interfaces.go` - LoginOptions 和认证器配置
- **相关架构:** `/llmdoc/architecture/sdk-component-architecture.md` - 组件配置关系
- **外部文档:** `internal/config/` - 配置结构体定义