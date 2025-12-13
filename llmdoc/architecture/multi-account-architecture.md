# Multi-Account Architecture

## 1. Identity

- **What it is:** 多账户管理和负载均衡系统的架构设计。
- **Purpose:** 支持每个提供商的多个账户，实现智能路由和故障转移。

## 2. Core Components

- `sdk/cliproxy/auth/manager.go` (Manager): 多账户管理核心，处理注册、更新和执行路由。
- `sdk/cliproxy/auth/selector.go` (RoundRobinSelector): 轮询选择器，实现账户间的负载均衡。
- `sdk/cliproxy/auth/types.go` (Auth, ModelState): 认证记录和模型状态跟踪。
- `internal/registry/`: 全局注册表，跟踪客户端和模型可用性。

## 3. Execution Flow (LLM Retrieval Map)

- **1. 账户注册:** `sdk/cliproxy/auth/manager.go:188-203` 注册新认证记录到内存映射。
- **2. 模型状态跟踪:** `sdk/cliproxy/auth/types.go:77-93` 每个模型的独立状态管理。
- **3. 选择器执行:** `sdk/cliproxy/auth/manager.go:1084-1130` 基于可用性和轮询选择账户。
- **4. 提供商轮换:** `sdk/cliproxy/auth/manager.go:543-576` 每个模型的起始提供商轮换。
- **5. 结果处理:** `sdk/cliproxy/auth/manager.go:696-811` 更新账户状态和冷却时间。

## 4. Design Rationale

多账户架构通过分离认证状态和执行逻辑，实现了灵活的负载均衡。轮询选择器确保请求分布均匀，而基于错误的冷却机制防止故障账户影响服务。每个模型的独立状态跟踪允许细粒度的可用性管理。