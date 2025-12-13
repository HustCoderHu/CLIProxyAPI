# 请求流程架构

## 1. 身份

- **它是：** 请求从接收到响应的完整处理流程。
- **目的：** 展示请求如何在各个组件之间流动和转换。

## 2. 核心组件

- `cmd/server/main.go` (main): 应用程序入口点，处理命令行参数和配置加载
- `internal/cmd/run.go` (StartService): 服务启动逻辑，初始化并启动代理服务
- `sdk/cliproxy/builder.go` (Builder, Build): 服务构建器，使用依赖注入组装所有组件
- `internal/api/server.go` (Server, UpdateClients): HTTP 服务器，管理客户端更新和配置变更

## 3. 执行流程（LLM 检索映射）

- **1. 应用启动：** `cmd/server/main.go:53` 解析命令行标志，确定操作模式
- **2. 配置加载：** 根据配置源（文件/Postgres/Git/对象存储）加载配置 (`cmd/server/main.go:221-380`)
- **3. 服务构建：** Builder 模式组装服务依赖 (`sdk/cliproxy/builder.go:155-218`)
- **4. 服务器初始化：** 创建带有所有中间件和路由的 Server 实例 (`internal/api/server.go:175-308`)
- **5. 请求处理：** 请求流经认证、路由到处理器、执行并返回响应
- **6. 配置热更新：** UpdateClients 方法处理运行时配置变更 (`internal/api/server.go:815-970`)

## 4. 设计理由

使用 Builder 模式实现了清晰的依赖注入和组件组装。支持多种配置后端提供了部署灵活性。配置热重载机制允许零停机更新。请求流程的模块化设计便于维护和扩展。