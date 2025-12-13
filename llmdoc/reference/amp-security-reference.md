# Amp Security Reference

## 1. Core Summary

Amp integration implements defense-in-depth security with localhost-only management routes, CORS disabling, and connection-based IP validation to prevent unauthorized access to sensitive endpoints.

## 2. Source of Truth

- **Primary Code:** `internal/api/modules/amp/routes.go:19-96` - Security middleware implementations.
- **Localhost Restriction:** `routes.go:21-61` - TCP connection-based IP validation.
- **CORS Protection:** `routes.go:63-81` - CORS header removal for management routes.
- **Configuration:** `internal/config/config.go:140-143` - RestrictManagementToLocalhost setting.
- **Hot Reload Support:** `internal/api/modules/amp/amp.go:337-342` - Dynamic security setting updates.
- **Related Architecture:** `/llmdoc/architecture/amp-routing-architecture.md` - Security design rationale.