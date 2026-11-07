package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

const qoderKeysV8YAML = `config-version: 8
api-keys:
  qoder:
    - name: qoder-cn-1
      prefix: cn
      base-url: https://gateway.qoder.example
      headers:
        X-Custom: value
      excluded-models: ["QModel_Latest", " efficient ", "qmodel_latest"]
      keys:
        - api-key: " pt-cn-1 "
          weight: 3
    - name: qoder-cn-2
      prefix: cn-trial
      keys:
        - api-key: pt-cn-1
  qoder-intl:
    - name: qoder-intl-1
      prefix: intl
      base-url: https://api3.qoder.example
      excluded-models: ["qmodel_latest"]
      keys:
        - api-key: pt-intl-1
`

// TestQoderKeysV8RoundTrip locks the config save round-trip: both realm sections
// must survive LoadConfig -> SaveConfig -> reload with identical values, including
// the prefix, base-url and excluded-models that carry realm isolation.
func TestQoderKeysV8RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(qoderKeysV8YAML), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	weight := 3
	wantCN := []QoderKey{
		{
			APIKey:         "pt-cn-1",
			Prefix:         "cn",
			BaseURL:        "https://gateway.qoder.example",
			Weight:         &weight,
			Headers:        map[string]string{"X-Custom": "value"},
			ExcludedModels: []string{"qmodel_latest", "efficient"},
		},
		// Same api-key as the first entry but a different prefix registers a
		// different model namespace, so both must survive sanitization.
		{APIKey: "pt-cn-1", Prefix: "cn-trial"},
	}
	if !reflect.DeepEqual(cfg.QoderKey, wantCN) {
		t.Fatalf("qoder-api-key = %#v, want %#v", cfg.QoderKey, wantCN)
	}
	wantINTL := []QoderKey{{
		APIKey:         "pt-intl-1",
		Prefix:         "intl",
		BaseURL:        "https://api3.qoder.example",
		ExcludedModels: []string{"qmodel_latest"},
	}}
	if !reflect.DeepEqual(cfg.QoderINTLKey, wantINTL) {
		t.Fatalf("qoder-intl-api-key = %#v, want %#v", cfg.QoderINTLKey, wantINTL)
	}

	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(saved); err != nil {
		t.Fatalf("saved config is not valid v8: %v", err)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	for provider, groups := range map[string]int{"qoder": 2, "qoder-intl": 1} {
		node := yamlPath(doc.Content[0], "api-keys."+provider)
		if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) != groups {
			t.Fatalf("api-keys.%s group count = %v, want %d", provider, node, groups)
		}
	}

	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if !reflect.DeepEqual(reloaded.QoderKey, wantCN) {
		t.Fatalf("qoder-api-key after save = %#v, want %#v", reloaded.QoderKey, wantCN)
	}
	if !reflect.DeepEqual(reloaded.QoderINTLKey, wantINTL) {
		t.Fatalf("qoder-intl-api-key after save = %#v, want %#v", reloaded.QoderINTLKey, wantINTL)
	}
}

// TestQoderKeysSanitizerParity locks the two load entry points together: a file
// loaded from disk and the same bytes parsed in memory must produce identical
// Qoder sections. A sanitizer registered on only one path breaks this.
func TestQoderKeysSanitizerParity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(qoderKeysV8YAML), 0600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	fromBytes, err := ParseConfigBytes([]byte(qoderKeysV8YAML))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !reflect.DeepEqual(fromFile.QoderKey, fromBytes.QoderKey) {
		t.Fatalf("qoder-api-key differs between load paths:\nfile  = %#v\nbytes = %#v", fromFile.QoderKey, fromBytes.QoderKey)
	}
	if !reflect.DeepEqual(fromFile.QoderINTLKey, fromBytes.QoderINTLKey) {
		t.Fatalf("qoder-intl-api-key differs between load paths:\nfile  = %#v\nbytes = %#v", fromFile.QoderINTLKey, fromBytes.QoderINTLKey)
	}
	if len(fromBytes.QoderKey) != 2 || len(fromBytes.QoderINTLKey) != 1 {
		t.Fatalf("unexpected sanitized counts: cn=%d intl=%d", len(fromBytes.QoderKey), len(fromBytes.QoderINTLKey))
	}
}

// TestSanitizeQoderKeys covers the sanitizer rules directly: empty keys are
// dropped, identity fields are trimmed and normalized, and duplicates are
// deduplicated by (api-key, base-url, prefix).
func TestSanitizeQoderKeys(t *testing.T) {
	cfg := &Config{
		QoderKey: []QoderKey{
			{APIKey: "   "},
			{APIKey: ""},
			{
				APIKey:         " key-1 ",
				Prefix:         " cn/ ",
				BaseURL:        " https://gateway.example ",
				ProxyURL:       " direct ",
				Headers:        map[string]string{" X-Test ": " value ", " ": "dropped"},
				ExcludedModels: []string{" A ", "a", "", "B*"},
			},
			// Duplicate of the previous entry after normalization.
			{APIKey: "key-1", Prefix: "cn", BaseURL: "https://gateway.example"},
			// Same key and base URL but another prefix must survive.
			{APIKey: "key-1", Prefix: "cn-trial", BaseURL: "https://gateway.example"},
			// A multi-segment prefix is rejected by normalization (empty).
			{APIKey: "key-2", Prefix: "a/b"},
		},
		QoderINTLKey: []QoderKey{{APIKey: "intl-key"}},
	}
	cfg.SanitizeQoderKeys()
	cfg.SanitizeQoderINTLKeys()

	want := []QoderKey{
		{
			APIKey:         "key-1",
			Prefix:         "cn",
			BaseURL:        "https://gateway.example",
			ProxyURL:       "direct",
			Headers:        map[string]string{"X-Test": "value"},
			ExcludedModels: []string{"a", "b*"},
		},
		{APIKey: "key-1", Prefix: "cn-trial", BaseURL: "https://gateway.example"},
		{APIKey: "key-2"},
	}
	if !reflect.DeepEqual(cfg.QoderKey, want) {
		t.Fatalf("QoderKey = %#v, want %#v", cfg.QoderKey, want)
	}
	if len(cfg.QoderINTLKey) != 1 || cfg.QoderINTLKey[0].APIKey != "intl-key" {
		t.Fatalf("QoderINTLKey = %#v", cfg.QoderINTLKey)
	}
}
