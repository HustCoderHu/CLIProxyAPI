# API 端点参考

## 1. 核心摘要

CLI Proxy API 提供了统一的端点集合，支持 OpenAI、Gemini 和 Claude API 兼容性，以及全面的管理接口。

## 2. 真实来源

- **主要路由定义：** `internal/api/server.go:310-419` - 定义所有 API 端点和路由规则
- **OpenAI 兼容端点：** `internal/api/server.go:320-330` - `/v1/*` 路径下的端点
- **Gemini 端点：** `internal/api/server.go:332-339` - `/v1beta/*` 路径下的端点
- **管理 API：** `internal/api/server.go:458-585` - `/v0/management/*` 路径下的端点
- **OAuth 回调：** `internal/api/server.go:354-416` - 各提供商的 OAuth 回调端点
- **相关架构：** `/llmdoc/architecture/core-server-architecture.md` - 服务器架构详细信息
- **外部文档：** `https://platform.openai.com/docs/api-reference` - OpenAI API 参考文档