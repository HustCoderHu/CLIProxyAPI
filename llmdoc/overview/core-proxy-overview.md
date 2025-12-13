# 核心代理总览

## 1. 身份

- **它是：** 一个统一的 API 代理服务器，为多个 AI 服务提供商提供 OpenAI/Gemini/Claude 兼容的接口。
- **目的：** 允许 CLI 模型与为标准 AI API 设计的工具和库一起使用。

## 2. 高级描述

CLI Proxy API 是一个复杂的代理服务器，提供统一的 API 端点以访问多个 AI 服务提供商。它支持多种配置后端（本地文件、Postgres、Git、对象存储）和各种 OAuth 登录流程。核心服务器使用 Gin 框架暴露 OpenAI 兼容的端点（`/v1/*`）、Gemini 端点（`/v1beta/*`）、OAuth 回调和全面的管理 API（`/v0/management/*`）。请求处理流程通过 BaseAPIHandler，该处理器解析模型、标准化思考模型后缀，并委托给核心认证管理器进行执行，在多个账户之间实现负载均衡。