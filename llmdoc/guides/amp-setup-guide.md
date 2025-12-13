# How to Set Up Amp CLI Integration

1. **Configure Upstream URL:** Set `amp-upstream-url` in config.yaml to your Amp control plane (e.g., `https://ampcode.com`).

2. **Add API Key:** Optionally configure `amp-upstream-api-key` for authentication with Amp upstream services.

3. **Security Settings:** Keep `restrict-management-to-localhost: true` (default) to secure management endpoints.

4. **Verify Registration:** Check logs for "amp provider alias routes registered" message on startup.

5. **Test Integration:** Use Amp CLI to make requests - they should route through local providers when available.

**Note:** Provider aliases work even without upstream URL configured, enabling local-only operation.