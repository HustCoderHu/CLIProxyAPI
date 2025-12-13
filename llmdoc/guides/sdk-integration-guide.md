# How to Integrate the Go SDK

1. **创建 Builder 实例:** 使用 `cliproxy.NewBuilder()` (`sdk/cliproxy/builder.go:69-71`) 创建一个新的构建器。

2. **配置基本参数:** 调用 `WithConfig(cfg)` 和 `WithConfigPath(path)` (`sdk/cliproxy/builder.go:80-95`) 设置配置实例和配置文件路径。

3. **自定义组件（可选）:**
   - 使用 `WithTokenClientProvider(provider)` (`sdk/cliproxy/builder.go:98-101`) 自定义令牌客户端提供者。
   - 使用 `WithAuthManager(manager)` (`sdk/cliproxy/builder.go:122-125`) 覆盖默认认证管理器。

4. **构建服务:** 调用 `Build()` (`sdk/cliproxy/builder.go:155-218`) 验证配置并创建 Service 实例。

5. **启动服务:** 使用 `service.Run(ctx)` (`sdk/cliproxy/service.go:411-559`) 启动服务，该方法会阻塞直到上下文取消。

6. **优雅关闭:** 在适当的时候调用 `service.Shutdown(ctx)` (`sdk/cliproxy/service.go:571-623`) 清理资源。

**验证方法:** 服务启动后会打印 "API server started successfully on: host:port"，确认服务正常运行。