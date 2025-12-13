# OAuth Authentication System Overview

## 1. Identity

- **What it is:** 一个多提供商OAuth认证管理系统，为CLI Proxy API提供统一的身份验证层。
- **Purpose:** 支持多个AI提供商（OpenAI Codex、Anthropic Claude、Google Gemini、Qwen Code、iFlow）的OAuth登录流程，实现自动令牌刷新和多账户负载均衡。

## 2. High-Level Description

OAuth认证系统是CLI Proxy API的核心组件，通过标准OAuth 2.0流程和设备流为多个AI提供商提供安全的身份验证。系统使用PKCE（Proof Key for Code Exchange）增强安全性，支持本地回调服务器处理OAuth回调，并实现了智能的令牌刷新机制。每个提供商都有独特的认证特征：Codex和Claude使用PKCE流程，Gemini使用Google CLI认证，Qwen使用设备流，iFlow使用标准OAuth代码流。系统还支持多账户管理，通过轮询选择器实现负载均衡，并提供详细的配额和错误状态跟踪。