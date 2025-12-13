# Executor and Translator Architecture

## 1. Identity

- **What it is:** 执行器和翻译器子系统的架构设计。
- **Purpose:** 实现请求的格式转换和提供商执行，支持多种 AI 服务的统一接口。

## 2. Core Components

- `sdk/cliproxy/executor/types.go` (Request, Response, Options, StreamChunk): 定义执行器的请求/响应类型和控制选项。
- `sdk/translator/format.go` (Format, FromString): 标识代理内部使用的请求/响应模式。
- `sdk/translator/types.go` (RequestTransform, ResponseTransform): 定义请求和响应转换函数类型。
- `sdk/translator/registry.go` (Registry, TranslateStream, TranslateNonStream): 管理并执行格式转换函数。

## 3. Execution Flow (LLM Retrieval Map)

- **1. 请求封装:** Request (`sdk/cliproxy/executor/types.go:11-20`) 封装模型、载荷和格式信息。
- **2. 格式检测:** Format (`sdk/translator/format.go:4`) 识别源和目标格式。
- **3. 请求转换:** Registry.TranslateRequest (`sdk/translator/registry.go:43-53`) 查找并执行请求转换函数。
- **4. 执行器调用:** ProviderExecutor.Execute (`sdk/cliproxy/auth/manager.go:26`) 处理转换后的请求。
- **5. 响应转换:**
   - 流式响应: Registry.TranslateStream (`sdk/translator/registry.go:69-79`)
   - 非流式响应: Registry.TranslateNonStream (`sdk/translator/registry.go:82-92`)
- **6. 结果返回:** Response (`sdk/cliproxy/executor/types.go:41-46`) 包装转换后的响应数据。

## 4. Design Rationale

通过将转换逻辑与执行逻辑分离，系统可以灵活支持新的提供商格式。转换函数的注册机制使得添加新格式变得简单，而不需要修改核心执行逻辑。这种设计实现了格式无关的执行器架构。