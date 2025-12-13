# Provider-Specific Authentication Flows Reference

## 1. Core Summary

每个AI提供商都有独特的认证流程和配置要求。系统支持PKCE OAuth流程、设备流和Google CLI认证等多种认证方式。

## 2. Source of Truth

- **OpenAI Codex:** `sdk/auth/codex.go` - 使用PKCE OAuth流程，回调端口1455，刷新提前5天。
- **Anthropic Claude:** `sdk/auth/claude.go` - 使用PKCE OAuth流程，回调端口54545，刷新提前4小时。
- **Google Gemini:** `sdk/auth/gemini.go` - 使用Google CLI认证，无需OAuth服务器，无自动刷新。
- **Qwen Code:** `sdk/auth/qwen.go` - 使用OAuth设备流，需要邮箱输入，刷新提前3小时。
- **iFlow:** `sdk/auth/iflow.go` - 使用标准OAuth代码流，回调端口动态，刷新提前24小时。
- **刷新时间注册:** `sdk/auth/refresh_registry.go` - 集中管理所有提供商的刷新时间配置。