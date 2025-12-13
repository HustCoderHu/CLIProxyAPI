# CLI Proxy API 文档索引

## 📋 快速导航

### 新手入门
1. [项目概述](overview/project-overview.md) - 了解项目整体架构和目标
2. [核心代理概述](overview/core-proxy-overview.md) - 理解代理服务器的基本概念
3. [服务器初始化指南](guides/server-initialization-guide.md) - 快速启动服务器
4. [配置管理指南](guides/configuration-management-guide.md) - 基础配置说明

---

## 🏗️ 核心架构

### Core Proxy Architecture（核心代理架构）
- **概览**
  - [核心代理概述](overview/core-proxy-overview.md) - 代理服务器的高级视图
  - [核心服务器架构](architecture/core-server-architecture.md) - HTTP 服务器设计细节

- **关键组件**
  - [请求流程架构](architecture/request-flow-architecture.md) - 请求处理生命周期
  - [模型映射架构](architecture/model-mapping-architecture.md) - AI 模型映射系统
  - [多账户架构](architecture/multi-account-architecture.md) - 负载均衡与账户管理

- **相关指南**
  - [模型映射指南](guides/model-mapping-guide.md) - 配置模型映射
  - [多账户设置指南](guides/multi-account-setup-guide.md) - 配置多账户负载均衡

### OAuth Authentication System（OAuth 认证系统）
- **概览**
  - [OAuth 认证概述](overview/oauth-overview.md) - 多提供商认证系统
  - [OAuth 提供商架构](architecture/oauth-provider-architecture.md) - 认证流程设计

- **实现细节**
  - [提供商特定流程参考](reference/provider-specific-flows-reference.md) - 各提供商的认证差异
  - [令牌存储参考](reference/token-storage-reference.md) - 令牌管理机制

- **操作指南**
  - [OAuth 登录指南](guides/oauth-login-guide.md) - 完整的登录流程
  - [自定义提供商指南](guides/custom-provider-guide.md) - 扩展新的认证提供商

### Go SDK Architecture（Go SDK 架构）
- **概览**
  - [SDK 概述](overview/sdk-overview.md) - SDK 的设计理念
  - [SDK 组件架构](architecture/sdk-component-architecture.md) - 核心模块设计
  - [执行器和翻译器架构](architecture/executor-translator-architecture.md) - 请求处理管道

- **接口与配置**
  - [SDK 接口参考](reference/sdk-interfaces-reference.md) - 公共 API 接口
  - [SDK 配置参考](reference/sdk-configuration-reference.md) - 配置选项详解

- **使用指南**
  - [SDK 集成指南](guides/sdk-integration-guide.md) - 嵌入 SDK 到应用

### Amp CLI Integration（Amp CLI 集成）
- **概览**
  - [Amp 集成概述](overview/amp-overview.md) - 与 Amp CLI 的集成方案
  - [Amp 路由架构](architecture/amp-routing-architecture.md) - 智能路由系统

- **API 与安全**
  - [Amp API 路由参考](reference/amp-api-routes-reference.md) - 路由端点详情
  - [Amp 安全参考](reference/amp-security-reference.md) - 安全特性说明

- **配置指南**
  - [Amp 设置指南](guides/amp-setup-guide.md) - 完整的集成步骤

---

## 📚 文档分类

### Overview（概览文档）
- [项目概述](overview/project-overview.md) - 项目整体介绍
- [核心代理概述](overview/core-proxy-overview.md) - 代理服务核心概念
- [OAuth 认证概述](overview/oauth-overview.md) - 认证系统概览
- [SDK 概述](overview/sdk-overview.md) - Go SDK 介绍
- [Amp 集成概述](overview/amp-overview.md) - Amp CLI 集成说明

### Architecture（架构文档）
- [核心服务器架构](architecture/core-server-architecture.md) - HTTP 服务器设计
- [请求流程架构](architecture/request-flow-architecture.md) - 请求处理流程
- [模型映射架构](architecture/model-mapping-architecture.md) - AI 模型映射
- [OAuth 提供商架构](architecture/oauth-provider-architecture.md) - 认证系统设计
- [多账户架构](architecture/multi-account-architecture.md) - 账户管理系统
- [SDK 组件架构](architecture/sdk-component-architecture.md) - SDK 模块设计
- [执行器和翻译器架构](architecture/executor-translator-architecture.md) - 请求处理管道
- [Amp 路由架构](architecture/amp-routing-architecture.md) - 路由系统设计

### Guides（操作指南）
- [服务器初始化指南](guides/server-initialization-guide.md) - 启动服务器
- [配置管理指南](guides/configuration-management-guide.md) - 管理配置
- [OAuth 登录指南](guides/oauth-login-guide.md) - OAuth 认证流程
- [模型映射指南](guides/model-mapping-guide.md) - 配置模型映射
- [多账户设置指南](guides/multi-account-setup-guide.md) - 多账户配置
- [Amp 设置指南](guides/amp-setup-guide.md) - Amp CLI 集成
- [SDK 集成指南](guides/sdk-integration-guide.md) - 嵌入 SDK
- [自定义提供商指南](guides/custom-provider-guide.md) - 扩展提供商

### Reference（参考文档）
- [API 端点参考](reference/api-endpoints-reference.md) - REST API 端点
- [配置选项参考](reference/configuration-options-reference.md) - 所有配置项
- [SDK 接口参考](reference/sdk-interfaces-reference.md) - SDK API 文档
- [SDK 配置参考](reference/sdk-configuration-reference.md) - SDK 配置项
- [提供商特定流程参考](reference/provider-specific-flows-reference.md) - 认证流程细节
- [令牌存储参考](reference/token-storage-reference.md) - 令牌管理
- [Amp API 路由参考](reference/amp-api-routes-reference.md) - Amp 路由端点
- [Amp 安全参考](reference/amp-security-reference.md) - 安全实现
- [编码规范](reference/coding-conventions.md) - 代码风格指南
- [Git 规范](reference/git-conventions.md) - 版本控制规范

---

## 🔍 按功能查找

### 认证相关
- 概览: [OAuth 认证概述](overview/oauth-overview.md)
- 架构: [OAuth 提供商架构](architecture/oauth-provider-architecture.md)
- 指南: [OAuth 登录指南](guides/oauth-login-guide.md)
- 参考: [提供商特定流程参考](reference/provider-specific-flows-reference.md)

### API 代理
- 概览: [核心代理概述](overview/core-proxy-overview.md)
- 架构: [核心服务器架构](architecture/core-server-architecture.md)
- 流程: [请求流程架构](architecture/request-flow-architecture.md)
- 端点: [API 端点参考](reference/api-endpoints-reference.md)

### SDK 开发
- 概览: [SDK 概述](overview/sdk-overview.md)
- 架构: [SDK 组件架构](architecture/sdk-component-architecture.md)
- 接口: [SDK 接口参考](reference/sdk-interfaces-reference.md)
- 集成: [SDK 集成指南](guides/sdk-integration-guide.md)

### Amp CLI 集成
- 概览: [Amp 集成概述](overview/amp-overview.md)
- 架构: [Amp 路由架构](architecture/amp-routing-architecture.md)
- 设置: [Amp 设置指南](guides/amp-setup-guide.md)
- 安全: [Amp 安全参考](reference/amp-security-reference.md)

---

## 📖 开发者资源

### 开发规范
- [编码规范](reference/coding-conventions.md) - 代码风格和约定
- [Git 规范](reference/git-conventions.md) - 提交信息和分支策略

### 配置资源
- [配置选项参考](reference/configuration-options-reference.md) - 完整配置列表
- [SDK 配置参考](reference/sdk-configuration-reference.md) - SDK 特定配置

### 扩展开发
- [自定义提供商指南](guides/custom-provider-guide.md) - 添加新的 AI 提供商
- [模型映射指南](guides/model-mapping-guide.md) - 自定义模型映射

---

*最后更新: 2025-12-13*