# How to Configure Model Mappings

1. **Edit Configuration:** Add `amp-model-mappings` section to config.yaml with From/To pairs.

2. **Map Unavailable Models:** Configure mappings for models not in your local providers (e.g., `claude-opus-4.5` → `claude-sonnet-4`).

3. **Verify Target Providers:** Ensure mapped "To" models have configured providers in the system.

4. **Force Mapping Mode:** Set `force-model-mappings: true` to prioritize mappings over local API keys.

5. **Hot Reload:** Update configuration without restart - mappings apply automatically to new requests.

**Example Configuration:**
```yaml
amp-model-mappings:
  - from: "claude-opus-4.5"
    to: "claude-sonnet-4"
```