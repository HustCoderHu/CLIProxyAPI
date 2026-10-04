package auth

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// CanonicalCredentialMetadataKey returns the canonical snake_case name for
// credential metadata keys that previously also accepted config-style aliases.
func CanonicalCredentialMetadataKey(key string) string {
	switch key {
	case "api-key":
		return "api_key"
	case "base-url":
		return "base_url"
	case "disable-cooling":
		return "disable_cooling"
	case "excluded-models":
		return "excluded_models"
	case "fingerprint-profile":
		return "fingerprint_profile"
	case "model-aliases":
		return "model_aliases"
	case "proxy-url":
		return "proxy_url"
	case "request-retry":
		return "request_retry"
	case "request-scoped-errors":
		return "request_scoped_errors"
	case "tool-prefix-disabled":
		return "tool_prefix_disabled"
	default:
		return key
	}
}

// NormalizeCredentialMetadata rewrites recognized legacy keys to their
// canonical snake_case names. An explicitly present canonical value wins.
// It also seamlessly normalizes nested/camelCase credentials from workbuddy2api auth files.
func NormalizeCredentialMetadata(metadata map[string]any) {
	if metadata == nil {
		return
	}
	for key, value := range metadata {
		canonical := CanonicalCredentialMetadataKey(key)
		if canonical == key {
			continue
		}
		if _, exists := metadata[canonical]; !exists {
			metadata[canonical] = value
		}
		delete(metadata, key)
	}

	normalizeWorkBuddyMetadata(metadata)
}

func normalizeWorkBuddyMetadata(metadata map[string]any) {
	// 1. Nested workbuddy2api format: {"auth": {...}, "account": {...}}
	if authObj, ok := metadata["auth"].(map[string]any); ok && authObj != nil {
		if _, exists := metadata["type"]; !exists {
			metadata["type"] = "codebuddy-cn"
		}
		if _, exists := metadata["auth_kind"]; !exists {
			metadata["auth_kind"] = "oauth"
		}

		if at, ok := authObj["accessToken"].(string); ok && strings.TrimSpace(at) != "" {
			if _, exists := metadata["access_token"]; !exists {
				metadata["access_token"] = strings.TrimSpace(at)
			}
		} else if at, ok := authObj["access_token"].(string); ok && strings.TrimSpace(at) != "" {
			if _, exists := metadata["access_token"]; !exists {
				metadata["access_token"] = strings.TrimSpace(at)
			}
		}

		if rt, ok := authObj["refreshToken"].(string); ok && strings.TrimSpace(rt) != "" {
			if _, exists := metadata["refresh_token"]; !exists {
				metadata["refresh_token"] = strings.TrimSpace(rt)
			}
		} else if rt, ok := authObj["refresh_token"].(string); ok && strings.TrimSpace(rt) != "" {
			if _, exists := metadata["refresh_token"]; !exists {
				metadata["refresh_token"] = strings.TrimSpace(rt)
			}
		}

		if _, exists := metadata["base_url"]; !exists {
			if domain, ok := authObj["domain"].(string); ok && strings.TrimSpace(domain) != "" {
				domain = strings.ToLower(strings.TrimSpace(domain))
				if strings.Contains(domain, "workbuddy.ai") {
					metadata["base_url"] = "https://www.workbuddy.ai/v2"
				} else {
					metadata["base_url"] = "https://copilot.tencent.com/v2"
				}
			} else {
				metadata["base_url"] = "https://copilot.tencent.com/v2"
			}
		}

		if _, exists := metadata["expired"]; !exists {
			if exp, ok := authObj["expiresAt"]; ok {
				if expTime := parseTimestamp(exp); !expTime.IsZero() {
					metadata["expired"] = expTime.UTC().Format(time.RFC3339)
				}
			}
		}

		if accountObj, ok := metadata["account"].(map[string]any); ok && accountObj != nil {
			if nick, ok := accountObj["nickname"].(string); ok && strings.TrimSpace(nick) != "" {
				if _, exists := metadata["label"]; !exists {
					metadata["label"] = strings.TrimSpace(nick)
				}
				if _, exists := metadata["email"]; !exists {
					metadata["email"] = strings.TrimSpace(nick)
				}
			}
			if uid, ok := accountObj["uid"].(string); ok && strings.TrimSpace(uid) != "" {
				if _, exists := metadata["account_id"]; !exists {
					metadata["account_id"] = strings.TrimSpace(uid)
				}
			}
		}
		return
	}

	// 2. Flat camelCase format: {"accessToken": ..., "refreshToken": ..., "uid": ...}
	if at, ok := metadata["accessToken"].(string); ok && strings.TrimSpace(at) != "" {
		if _, exists := metadata["access_token"]; !exists {
			metadata["access_token"] = strings.TrimSpace(at)
		}
		if _, exists := metadata["type"]; !exists {
			metadata["type"] = "codebuddy-cn"
		}
		if _, exists := metadata["auth_kind"]; !exists {
			metadata["auth_kind"] = "oauth"
		}
		if rt, ok := metadata["refreshToken"].(string); ok && strings.TrimSpace(rt) != "" {
			if _, exists := metadata["refresh_token"]; !exists {
				metadata["refresh_token"] = strings.TrimSpace(rt)
			}
		}
		if exp, ok := metadata["expiresAt"]; ok {
			if _, exists := metadata["expired"]; !exists {
				if expTime := parseTimestamp(exp); !expTime.IsZero() {
					metadata["expired"] = expTime.UTC().Format(time.RFC3339)
				}
			}
		}
		if uid, ok := metadata["uid"].(string); ok && strings.TrimSpace(uid) != "" {
			if _, exists := metadata["account_id"]; !exists {
				metadata["account_id"] = strings.TrimSpace(uid)
			}
		}
		if nick, ok := metadata["nickname"].(string); ok && strings.TrimSpace(nick) != "" {
			if _, exists := metadata["label"]; !exists {
				metadata["label"] = strings.TrimSpace(nick)
			}
			if _, exists := metadata["email"]; !exists {
				metadata["email"] = strings.TrimSpace(nick)
			}
		}
	}
}

func parseTimestamp(val any) time.Time {
	switch v := val.(type) {
	case float64:
		if v > 1e11 { // milliseconds
			return time.UnixMilli(int64(v))
		}
		if v > 1e9 { // seconds
			return time.Unix(int64(v), 0)
		}
	case int64:
		if v > 1e11 {
			return time.UnixMilli(v)
		}
		if v > 1e9 {
			return time.Unix(v, 0)
		}
	case int:
		if int64(v) > 1e9 {
			return time.Unix(int64(v), 0)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			if n > 1e11 {
				return time.UnixMilli(n)
			}
			if n > 1e9 {
				return time.Unix(n, 0)
			}
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			if n > 1e11 {
				return time.UnixMilli(n)
			}
			if n > 1e9 {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}
