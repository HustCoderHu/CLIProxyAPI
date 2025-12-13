# 配置选项参考

## 1. 核心摘要

配置系统支持 YAML 格式，包含服务器设置、API 密钥、代理配置和各提供商特定选项，支持多种存储后端。

## 2. 真实来源

- **主要配置结构：** `internal/config/config.go:20-87` - Config 结构体定义
- **服务器设置：** `internal/config/config.go:23-30` - Host、Port、TLS 配置
- **认证配置：** `internal/config/config.go:35-36` - AuthDir 和远程管理设置
- **API 密钥配置：** `internal/config/config.go:61-75` - Gemini、Claude、Codex 等 API 密钥
- **TLS 配置：** `internal/config/config.go:89-97` - TLSConfig 结构体
- **远程管理：** `internal/config/config.go:99-107` - RemoteManagement 配置
- **Amp 集成：** `internal/config/config.go:131-153` - AmpCode 配置结构
- **负载规则：** `internal/config/config.go:155-177` - PayloadConfig 规则定义
- **相关架构：** `/llmdoc/architecture/request-flow-architecture.md` - 配置加载流程
- **示例配置：** `config.example.yaml` - 配置模板文件