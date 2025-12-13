# How to Add a Custom Provider

1. **实现 ProviderExecutor 接口:** 创建结构体实现 `ProviderExecutor` (`sdk/cliproxy/auth/manager.go:22-33`) 的方法：
   - `Identifier()` 返回提供商唯一标识
   - `Execute()` 处理非流式请求
   - `ExecuteStream()` 处理流式请求
   - `Refresh()` 刷新认证凭据

2. **注册执行器:** 使用 `coreManager.RegisterExecutor(executor)` (`sdk/cliproxy/auth/manager.go:167-175`) 将执行器注册到管理器。

3. **添加转换函数（如需要）:**
   - 实现 `RequestTransform` 和 `ResponseTransform` (`sdk/translator/types.go:6-34`)
   - 使用 `translator.Default().Register()` (`sdk/translator/registry.go:115-117`) 注册转换函数

4. **配置认证:** 创建 Auth 实例并设置必要的 Attributes 和 Metadata，通过 `coreManager.Register()` (`sdk/cliproxy/auth/manager.go:189-203`) 注册。

5. **注册访问提供商（如需要）:** 实现 `Provider` 接口并使用 `access.RegisterProvider()` (`sdk/access/registry.go:34-41`) 注册。

**验证方法:** 发送测试请求到新提供商的模型，确认请求能够正确转换和执行。查看日志输出验证执行器被正确调用。