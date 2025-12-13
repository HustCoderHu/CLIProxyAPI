# 编码约定

## 1. 核心摘要

CLIProxyAPI 是一个 Go 1.24 项目，使用模块化架构构建 AI API 代理服务。项目遵循标准的 Go 编码规范，采用跨平台编译策略，支持 Linux、Windows 和 macOS (amd64/arm64)。

## 2. 真实来源

- **主模块配置**: `go.mod` - 定义模块路径 `github.com/router-for-me/CLIProxyAPI/v6`，要求 Go 1.24.0
- **构建配置**: `.goreleaser.yml` - 定义跨平台构建参数和发布流程
- **入口点**: `cmd/server/main.go` - 应用程序主入口
- **项目结构**: 遵循标准 Go 项目布局，使用 `internal/` 和 `sdk/` 分隔内部和外部代码
- **依赖管理**: 使用 Go modules，主要依赖包括 Gin (HTTP框架)、pgx (PostgreSQL)、logrus (日志) 等

### 关键约定

1. **编译设置**:
   - CGO_ENABLED=0 (静态编译)
   - 使用 ldflags 注入版本信息 (Version, Commit, BuildDate)

2. **模块组织**:
   - `internal/` - 内部包，不对外暴露
   - `sdk/` - 可被外部引用的 SDK 包
   - `cmd/` - 命令行入口点

3. **版本控制**:
   - 使用语义化版本
   - Git 提交信息排除 `^docs:` 和 `^test:` 前缀的变更