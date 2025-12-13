# Amp API Routes Reference

## 1. Core Summary

Amp CLI integration provides provider alias routes and management proxy routes with intelligent fallback logic. Routes support OpenAI, Anthropic, and Google providers with automatic model mapping.

## 2. Source of Truth

- **Primary Code:** `internal/api/modules/amp/routes.go` - Route registration and middleware definitions.
- **Provider Aliases:** `routes.go:195-268` - `/api/provider/{provider}/...` route patterns.
- **Management Routes:** `routes.go:98-193` - OAuth and management endpoint proxies.
- **Fallback Logic:** `internal/api/modules/amp/fallback_handlers.go` - Smart routing implementation.
- **Configuration:** `internal/config/config.go` - AmpCode and AmpModelMapping structures.
- **Related Architecture:** `/llmdoc/architecture/amp-routing-architecture.md` - Detailed routing flow.