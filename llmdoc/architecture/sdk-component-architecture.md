# SDK Component Architecture

## 1. Identity

- **What it is:** SDK 核心组件的分层架构设计。
- **Purpose:** 定义各组件职责和交互方式，实现模块化和可扩展的系统架构。

## 2. Core Components

- `sdk/cliproxy/builder.go` (Builder, NewBuilder, WithConfig, Build): 提供流式配置接口，负责组装所有服务组件。
- `sdk/cliproxy/service.go` (Service, Run, Shutdown): 核心服务实现，管理完整生命周期包括认证、监听、HTTP 服务器。
- `sdk/auth/interfaces.go` (Authenticator, LoginOptions): 定义认证器接口，支持 OAuth 和 API Key 认证流程。
- `sdk/cliproxy/auth/manager.go` (Manager, RegisterExecutor, Execute): 协调认证生命周期、执行器管理和请求路由。
- `sdk/access/manager.go` (Manager, Authenticate, SetProviders): 处理入站请求的认证提供商链式调用。
- `sdk/translator/registry.go` (Registry, Register, TranslateRequest): 管理不同格式间的请求/响应转换函数。

## 3. Execution Flow (LLM Retrieval Map)

- **1. 服务构建:** Builder (`sdk/cliproxy/builder.go:155-218`) 验证配置并创建 Service 实例。
- **2. 服务启动:** Service.Run (`sdk/cliproxy/service.go:411-559`) 初始化认证、加载客户端、启动 HTTP 服务器和文件监听。
- **3. 请求认证:** accessManager.Authenticate (`sdk/access/manager.go:46-89`) 链式调用认证提供商验证请求。
- **4. 执行器选择:** coreManager.Execute (`sdk/cliproxy/auth/manager.go:247-282`) 根据模型和提供商选择合适的认证和执行器。
- **5. 请求转换:** Registry.TranslateRequest (`sdk/translator/registry.go:43-53`) 转换请求格式以适配目标提供商。
- **6. 响应处理:** 执行器返回响应后，通过转换器处理并返回给客户端。

## 4. Design Rationale

分层架构确保了各组件职责单一，通过接口定义实现了松耦合设计。Manager 模式统一管理同类资源（认证、执行器），Registry 模式支持动态注册转换函数，这种设计使得系统易于扩展新的提供商和格式。