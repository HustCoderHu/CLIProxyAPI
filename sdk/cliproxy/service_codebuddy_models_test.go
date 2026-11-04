package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestRegisterModelsForAuthCodeBuddyCNOAuthIgnoresAPIKeyModels(t *testing.T) {
	const authID = "codebuddy-cn-oauth-models"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(authID)
	})

	service := &Service{cfg: &config.Config{
		CodeBuddyCNKey: []config.CodeBuddyCNKey{
			{
				APIKey:  "api-key",
				BaseURL: "https://copilot.tencent.com/v2",
				Models: []config.CodeBuddyCNModel{
					{Name: "hy3"},
				},
			},
		},
	}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: constant.CodeBuddyCN,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			"base_url":                 "https://copilot.tencent.com/v2",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)

	got := modelRegistry.GetModelsForClient(authID)
	want := registry.GetCodeBuddyCNModels()
	if len(got) != len(want) {
		t.Fatalf("registered models = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("registered model[%d] = %q, want %q", i, got[i].ID, want[i].ID)
		}
	}

	foundStep := false
	foundSpace := false
	foundKimi28 := false
	foundHy4Free := false
	foundHy4Std := false
	for _, m := range got {
		if m.ID == "kimi-k2.7-code" {
			t.Fatal("unexpected legacy duplicate model kimi-k2.7-code found in registered models")
		}
		if m.ID == "step-5-preview" {
			foundStep = true
			if m.ContextLength != 1000000 {
				t.Fatalf("step-5-preview context_length = %d, want 1000000", m.ContextLength)
			}
			if m.Thinking == nil || len(m.Thinking.Levels) == 0 {
				t.Fatalf("step-5-preview thinking expected but missing")
			}
		}
		if m.ID == "space-bunny" {
			foundSpace = true
			if m.ContextLength != 1000000 {
				t.Fatalf("space-bunny context_length = %d, want 1000000", m.ContextLength)
			}
			if m.Thinking == nil || len(m.Thinking.Levels) == 0 {
				t.Fatalf("space-bunny thinking expected but missing")
			}
		}
		if m.ID == "kimi-k2.8-preview" {
			foundKimi28 = true
			if m.ContextLength != 1000000 {
				t.Fatalf("kimi-k2.8-preview context_length = %d, want 1000000", m.ContextLength)
			}
			if m.Thinking == nil || len(m.Thinking.Levels) == 0 {
				t.Fatalf("kimi-k2.8-preview thinking expected but missing")
			}
		}
		if m.ID == "glm-5.3-flash" {
			if len(m.SupportedInputModalities) != 1 || m.SupportedInputModalities[0] != "text" {
				t.Fatalf("glm-5.3-flash supportedInputModalities = %v, want [text]", m.SupportedInputModalities)
			}
		}
		if m.ID == "hy4free" {
			foundHy4Free = true
			assertHy4Model(t, m)
		}
		if m.ID == "hy4std" {
			foundHy4Std = true
			assertHy4Model(t, m)
		}
	}
	if !foundStep {
		t.Fatal("step-5-preview not found in registered CodeBuddy CN models")
	}
	if !foundSpace {
		t.Fatal("space-bunny not found in registered CodeBuddy CN models")
	}
	if !foundKimi28 {
		t.Fatal("kimi-k2.8-preview not found in registered CodeBuddy CN models")
	}
	if !foundHy4Free {
		t.Fatal("hy4free not found in registered CodeBuddy CN models")
	}
	if !foundHy4Std {
		t.Fatal("hy4std not found in registered CodeBuddy CN models")
	}
}

// assertHy4Model checks the Hy4 free/paid pair against the live CodeBuddy CN
// catalog: 960k input, 64k output, single "high" reasoning level that cannot
// be disabled.
func assertHy4Model(t *testing.T, m *registry.ModelInfo) {
	t.Helper()
	if m.ContextLength != 960000 {
		t.Fatalf("%s context_length = %d, want 960000", m.ID, m.ContextLength)
	}
	if m.MaxCompletionTokens != 64000 {
		t.Fatalf("%s max_completion_tokens = %d, want 64000", m.ID, m.MaxCompletionTokens)
	}
	if m.Thinking == nil || m.Thinking.ZeroAllowed || len(m.Thinking.Levels) != 1 || m.Thinking.Levels[0] != "high" {
		t.Fatalf("%s thinking = %#v, want levels [high] with zero_allowed=false", m.ID, m.Thinking)
	}
}
