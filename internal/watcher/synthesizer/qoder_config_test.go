package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestConfigSynthesizer_QoderKeys locks the Qoder config synthesis: each configured
// entry becomes one auth record, the realm selects the provider, the base URL falls
// back to the realm gateway, and the PAT is exposed both as the identity api_key and
// as the metadata access_token the executor and catalog probe read.
func TestConfigSynthesizer_QoderKeys(t *testing.T) {
	weight := 5
	disableCooling := true
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{
			QoderKey: []config.QoderKey{
				{
					APIKey:         "pt-cn",
					Prefix:         "cn",
					Weight:         &weight,
					DisableCooling: &disableCooling,
					ExcludedModels: []string{"qmodel-latest"},
				},
				{
					APIKey:  "pt-cn-custom",
					Prefix:  "cn-custom",
					BaseURL: "https://gateway.custom.example",
				},
			},
			QoderINTLKey: []config.QoderKey{
				{APIKey: "pt-intl", Prefix: "intl"},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 3 {
		t.Fatalf("auth count = %d, want 3", len(auths))
	}
	byKey := make(map[string]*coreauth.Auth, len(auths))
	for _, auth := range auths {
		byKey[auth.Attributes["api_key"]] = auth
	}

	tests := []struct {
		key         string
		provider    string
		baseURL     string
		prefix      string
		excluded    string
		hasWeight   bool
		disableCool bool
	}{
		{key: "pt-cn", provider: "qoder", baseURL: "https://gateway.qoder.com.cn", prefix: "cn", excluded: "qmodel-latest", hasWeight: true, disableCool: true},
		{key: "pt-cn-custom", provider: "qoder", baseURL: "https://gateway.custom.example", prefix: "cn-custom"},
		{key: "pt-intl", provider: "qoder-intl", baseURL: "https://api3.qoder.sh", prefix: "intl"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			auth, ok := byKey[test.key]
			if !ok {
				t.Fatalf("no auth synthesized for api-key %q", test.key)
			}
			if auth.Provider != test.provider {
				t.Fatalf("provider = %q, want %q", auth.Provider, test.provider)
			}
			if auth.Label != test.provider+"-apikey" {
				t.Fatalf("label = %q, want %q", auth.Label, test.provider+"-apikey")
			}
			if auth.Prefix != test.prefix {
				t.Fatalf("prefix = %q, want %q", auth.Prefix, test.prefix)
			}
			if got := auth.Attributes["base_url"]; got != test.baseURL {
				t.Fatalf("attributes[base_url] = %q, want %q", got, test.baseURL)
			}
			// The PAT is stored as the OAuth bearer so the executor can sign with it.
			if got := auth.Metadata["type"]; got != test.provider {
				t.Fatalf("metadata[type] = %v, want %q", got, test.provider)
			}
			if got := auth.Metadata["auth_kind"]; got != "pat" {
				t.Fatalf("metadata[auth_kind] = %v, want pat", got)
			}
			if got := auth.Metadata["access_token"]; got != test.key {
				t.Fatalf("metadata[access_token] = %v, want %q", got, test.key)
			}
			if got := auth.Metadata["personal_token"]; got != test.key {
				t.Fatalf("metadata[personal_token] = %v, want %q", got, test.key)
			}
			if got := auth.Metadata["source"]; got != "pat" {
				t.Fatalf("metadata[source] = %v, want pat", got)
			}
			// Config-synthesized credentials register only when they look like
			// config API keys; the PAT provenance stays in metadata.
			if got := auth.AuthKind(); got != coreauth.AuthKindAPIKey {
				t.Fatalf("runtime auth kind = %q, want apikey", got)
			}
			if !coreauth.IsConfigAPIKeyAuth(auth) {
				t.Fatal("synthesized credential is not recognized as a config API key auth")
			}
			if test.hasWeight {
				if got := auth.Attributes[coreauth.AttributeWeight]; got != "5" {
					t.Fatalf("weight = %q, want 5", got)
				}
			} else if _, exists := auth.Attributes[coreauth.AttributeWeight]; exists {
				t.Fatal("omitted weight was written")
			}
			if got := auth.Attributes["excluded_models"]; got != test.excluded {
				t.Fatalf("excluded_models = %q, want %q", got, test.excluded)
			}
			if test.disableCool {
				if got := auth.Metadata["disable_cooling"]; got != true {
					t.Fatalf("metadata[disable_cooling] = %v, want true", got)
				}
			}
		})
	}
}

// TestConfigSynthesizer_QoderKeysSkipsEmptyAPIKey locks that an entry without a
// PAT registers nothing; the realm gateway fallback alone must not synthesize a
// credential that cannot authenticate.
func TestConfigSynthesizer_QoderKeysSkipsEmptyAPIKey(t *testing.T) {
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{
			QoderKey: []config.QoderKey{
				{APIKey: "   ", BaseURL: "https://gateway.example"},
				{APIKey: "pt-real", Prefix: "cn"},
			},
			QoderINTLKey: []config.QoderKey{{Prefix: "intl"}},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if auths[0].Provider != "qoder" || auths[0].Attributes["api_key"] != "pt-real" {
		t.Fatalf("unexpected auth: provider=%q key=%q", auths[0].Provider, auths[0].Attributes["api_key"])
	}
}

// TestConfigSynthesizer_QoderRealmsDoNotCollide locks provider isolation at the
// synthesis layer: CN and INTL entries with the same api-key and prefix still
// produce distinct auth IDs, because the provider is part of the identity.
func TestConfigSynthesizer_QoderRealmsDoNotCollide(t *testing.T) {
	entry := config.QoderKey{APIKey: "pt-shared", Prefix: "team"}
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{
			QoderKey:     []config.QoderKey{entry},
			QoderINTLKey: []config.QoderKey{entry},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 2 {
		t.Fatalf("auth count = %d, want 2", len(auths))
	}
	if auths[0].ID == auths[1].ID {
		t.Fatalf("both realms synthesized the same auth ID %q", auths[0].ID)
	}
	if auths[0].Provider == auths[1].Provider {
		t.Fatalf("both realms synthesized the same provider %q", auths[0].Provider)
	}
}
