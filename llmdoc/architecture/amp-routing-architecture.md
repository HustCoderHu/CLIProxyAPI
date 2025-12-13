# Amp Routing Architecture

## 1. Identity

- **What it is:** Core routing system that directs Amp CLI requests to appropriate handlers.
- **Purpose:** Implements intelligent request routing with fallback logic and security controls.

## 2. Core Components

- `internal/api/modules/amp/amp.go` (AmpModule, Register, OnConfigUpdated): Main module orchestrator with hot-reload support.
- `internal/api/modules/amp/routes.go` (registerProviderAliases, registerManagementRoutes): Route registration and middleware setup.
- `internal/api/modules/amp/fallback_handlers.go` (FallbackHandler, WrapHandler): Smart routing logic with three-tier fallback.
- `internal/api/modules/amp/proxy.go` (createReverseProxy): Reverse proxy implementation for upstream forwarding.
- `internal/api/modules/amp/secret.go` (MultiSourceSecret): Multi-source API key management.

## 3. Execution Flow (LLM Retrieval Map)

- **1. Route Registration:** `amp.go:116-158` calls `registerProviderAliases` and `registerManagementRoutes`.
- **2. Request Ingestion:** `routes.go:201-268` registers provider-specific routes under `/api/provider/{provider}`.
- **3. Security Check:** `routes.go:21-61` applies localhost-only middleware for management routes.
- **4. Fallback Logic:** `fallback_handlers.go:113-246` wraps handlers with smart routing decisions.
- **5. Model Mapping:** `fallback_handlers.go:147-194` applies mappings when local providers unavailable.
- **6. Proxy Fallback:** `fallback_handlers.go:197-209` forwards to ampcode.com if no local options.

## 4. Design Rationale

The architecture prioritizes local provider usage to minimize costs while maintaining full Amp CLI compatibility. Hot-reload support enables runtime configuration updates without service interruption. Security follows defense-in-depth with localhost restrictions and CORS disabling for management endpoints.