# Model Mapping Architecture

## 1. Identity

- **What it is:** Thread-safe model name mapping system for routing unavailable models.
- **Purpose:** Enables cost optimization by mapping premium models to available alternatives.

## 2. Core Components

- `internal/api/modules/amp/model_mapping.go` (ModelMapper, DefaultModelMapper): Core mapping interface and implementation.
- `internal/api/modules/amp/fallback_handlers.go` (rewriteModelInRequest): Request body model rewriting.
- `internal/api/modules/amp/response_rewriter.go` (ResponseRewriter): Response model name restoration.
- `internal/config/config.go` (AmpModelMapping): Configuration structure for mappings.

## 3. Execution Flow (LLM Retrieval Map)

- **1. Configuration Load:** `model_mapping.go:32-39` initializes mapper from config mappings.
- **2. Request Processing:** `fallback_handlers.go:129` extracts model from request body/URL.
- **3. Mapping Lookup:** `model_mapping.go:44-70` checks for valid mapping with available providers.
- **4. Request Rewrite:** `fallback_handlers.go:261-271` replaces model in request JSON body.
- **5. Response Restoration:** `response_rewriter.go` rewrites response model names back to originals.
- **6. Hot Reload:** `model_mapping.go:74-100` updates mappings without service restart.

## 4. Design Rationale

Model mapping provides transparent cost optimization by routing requests for unavailable premium models to available alternatives. The system maintains transparency by rewriting both request and response model names, ensuring Amp CLI clients receive expected model identifiers. Thread-safe implementation supports concurrent requests and runtime configuration updates.