# OAuth Provider Architecture

## 1. Identity

- **What it is:** 各AI提供商OAuth认证实现的架构设计。
- **Purpose:** 定义统一的认证接口和提供商特定的实现细节。

## 2. Core Components

- `sdk/auth/manager.go` (Manager): 中央认证管理器，协调所有OAuth提供商并处理登录流程。
- `sdk/auth/codex.go` (CodexAuthenticator): OpenAI Codex OAuth实现，使用PKCE和端口1455。
- `sdk/auth/claude.go` (ClaudeAuthenticator): Anthropic Claude OAuth实现，使用PKCE和端口54545。
- `sdk/auth/gemini.go` (GeminiAuthenticator): Google CLI认证实现，无需OAuth服务器。
- `sdk/auth/qwen.go` (QwenAuthenticator): Qwen设备流认证实现，需要邮箱输入。
- `sdk/auth/iflow.go` (IFlowAuthenticator): iFlow OAuth代码流实现。
- `sdk/auth/refresh_registry.go` (registerRefreshLead): 提供商特定的刷新提前时间注册表。
- `sdk/cliproxy/auth/types.go` (Auth, TokenStorage): 认证数据结构和令牌存储接口定义。

## 3. Execution Flow (LLM Retrieval Map)

- **1. 初始化:** `sdk/auth/manager.go:19-28` 创建管理器并注册所有认证器。
- **2. 登录流程:** `sdk/auth/manager.go:47-76` 调用特定提供商的Login方法。
- **3. OAuth处理:** 各提供商文件实现PKCE/设备流（如 `sdk/auth/codex.go:50-146`）。
- **4. 令牌存储:** 认证成功后创建 `coreauth.Auth` 记录并持久化。
- **5. 刷新注册:** `sdk/auth/refresh_registry.go:9-17` 自动注册各提供商的刷新时间。

## 4. Design Rationale

每个提供商都有独特的认证要求，系统通过统一的Authenticator接口抽象了这些差异，同时允许提供商特定的实现细节。PKCE的使用增强了授权码流的安全性，而设备流则为无浏览器环境提供了替代方案。