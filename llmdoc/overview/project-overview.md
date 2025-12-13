# CLI Proxy API Project Overview

## 1. Identity

- **What it is:** 一个代理服务器，为命令行工具提供 OpenAI/Gemini/Claude/Codex 兼容的 API 接口。
- **Purpose:** 让用户能够通过 OAuth 认证使用多个 AI 提供商的服务，支持本地或多账户 CLI 访问，并与 Amp CLI 集成。

## 2. High-Level Description

CLI Proxy API 是一个基于 Go 语言开发的代理服务器，它将各种 AI 提供商（Google Gemini、OpenAI Codex、Anthropic Claude、Qwen Code、iFlow）的 API 转换为统一的 OpenAI 兼容接口。该系统通过 OAuth 认证流程支持多个提供商的登录，提供了多账户负载均衡、流式响应、函数调用、多模态输入支持等功能。它特别设计了 Amp CLI 集成支持，通过特定的路由模式和智能回退机制，让用户能够使用现有的订阅服务（如 Google 账户、ChatGPT Plus/Pro、Claude Pro/Max）与 Amp CLI 和 IDE 扩展程序配合使用。系统支持多种存储后端（本地文件、Git、PostgreSQL、对象存储），并提供完整的管理 API 和可嵌入的 Go SDK。