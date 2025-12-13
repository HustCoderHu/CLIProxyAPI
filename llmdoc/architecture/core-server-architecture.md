# 核心服务器架构

## 1. 身份

- **它是：** 基于 Gin 框架的 HTTP 服务器，提供统一的 AI API 代理服务。
- **目的：** 管理路由、认证、配置热重载和请求处理。

## 2. 核心组件

- `internal/api/server.go` (Server, NewServer, setupRoutes): 主服务器实现，管理路由、中间件和端点注册
- `sdk/api/handlers/handlers.go` (BaseAPIHandler): 核心请求处理器，处理模型解析和请求委托
- `internal/api/handlers/management/` (Handler): 管理处理器，提供运行时配置管理
- `sdk/cliproxy/auth/manager.go` (Manager): 核心认证管理器，处理请求执行和负载均衡
- `sdk/access/` (Manager): 访问控制管理器，处理请求认证提供者

## 3. 执行流程（LLM 检索映射）

- **1. 请求接收：** HTTP 请求到达 `internal/api/server.go:113` 的 Server 实例
- **2. 中间件处理：** 请求经过 CORS、日志记录和认证中间件 (`internal/api/server.go:202-222`)
- **3. 路由匹配：** 根据路径模式匹配到相应的处理器 (`internal/api/server.go:310-419`)
- **4. 认证验证：** AuthMiddleware 验证请求凭据 (`internal/api/server.go:984-1014`)
- **5. 请求委托：** BaseAPIHandler 处理请求，解析模型并委托给核心管理器 (`sdk/api/handlers/handlers.go`)
- **6. 执行与响应：** 核心认证管理器执行请求并返回响应

## 4. 设计理由

模块化架构实现了关注点分离，使各个组件可以独立开发和测试。使用 Gin 框架提供了强大的路由和中间件支持。管理 API 支持运行时配置更改，无需重启服务器。