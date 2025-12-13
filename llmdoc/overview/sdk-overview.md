# Go SDK Overview

## 1. Identity

- **What it is:** 一个可嵌入的 Go SDK，提供 CLI Proxy API 的核心功能。
- **Purpose:** 让外部应用程序能够嵌入并控制 CLI Proxy 服务，支持多提供商 AI 服务的统一访问。

## 2. High-Level Description

Go SDK 采用了分层架构设计，通过 Builder 模式提供灵活的服务配置。核心组件包括 Service（服务生命周期管理）、Manager（认证管理）、Executor（请求执行）、Translator（格式转换）和 Registry（模型注册）。SDK 支持多种认证方式（OAuth、API Key），提供完整的请求/响应转换机制，内置重试和配额管理，并支持热重载配置。通过模块化设计，开发者可以轻松扩展新的 AI 提供商和自定义认证逻辑。