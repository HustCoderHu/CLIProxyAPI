package auth

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
)

func TestQoderAuthenticatorProviderKeys(t *testing.T) {
	cn := NewQoderAuthenticator()
	intl := NewQoderINTLAuthenticator()
	if got := cn.Provider(); got != "qoder" {
		t.Fatalf("CN Provider() = %q, want qoder", got)
	}
	if got := intl.Provider(); got != "qoder-intl" {
		t.Fatalf("INTL Provider() = %q, want qoder-intl", got)
	}
	if lead := cn.RefreshLead(); lead == nil || *lead != qoderRefreshLead {
		t.Fatalf("RefreshLead() = %v, want %v", lead, qoderRefreshLead)
	}
}

// TestQoderCoreAuthRealmSelection locks the realm -> provider/base_url mapping
// and the metadata contract the file synthesizer relies on.
func TestQoderCoreAuthRealmSelection(t *testing.T) {
	tests := []struct {
		name        string
		realm       qoder.RealmConfig
		wantType    string
		wantBaseURL string
	}{
		{name: "cn", realm: qoder.RealmCN, wantType: "qoder", wantBaseURL: "https://gateway.qoder.com.cn"},
		{name: "intl", realm: qoder.RealmINTL, wantType: "qoder-intl", wantBaseURL: "https://api3.qoder.sh"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := &qoder.TokenData{
				AccessToken:    "dt-1",
				RefreshToken:   "drt-1",
				TokenType:      "Bearer",
				ExpiresIn:      3600,
				UID:            "uid-1",
				OrganizationID: "1234567890123456789",
			}
			record := toQoderCoreAuth(qoder.BuildAuthRecord(test.realm, token, nil, qoder.AuthKindOAuth))
			if record.Provider != test.wantType {
				t.Fatalf("Provider = %q, want %q", record.Provider, test.wantType)
			}
			// The auth file "type" drives provider inference on reload, so it must
			// match the provider key exactly.
			if got, _ := record.Metadata["type"].(string); got != test.wantType {
				t.Fatalf("metadata type = %q, want %q", got, test.wantType)
			}
			if got, _ := record.Metadata["base_url"].(string); got != test.wantBaseURL {
				t.Fatalf("metadata base_url = %q, want %q", got, test.wantBaseURL)
			}
			if got := record.Attributes["base_url"]; got != test.wantBaseURL {
				t.Fatalf("attribute base_url = %q, want %q", got, test.wantBaseURL)
			}
			// Both credential kinds are refreshable bearer tokens, so the runtime
			// attribute is always OAuth; the PAT provenance lives in metadata.
			if got := record.Attributes["auth_kind"]; got != "oauth" {
				t.Fatalf("attribute auth_kind = %q, want oauth", got)
			}
			// The organization id must be stored as a string: the auth-file read
			// path decodes JSON numbers as float64 and a 19-digit id loses digits.
			orgID, ok := record.Metadata["organization_id"].(string)
			if !ok {
				t.Fatalf("organization_id type = %T, want string", record.Metadata["organization_id"])
			}
			if orgID != "1234567890123456789" {
				t.Fatalf("organization_id = %q", orgID)
			}
			if !strings.HasSuffix(record.FileName, ".json") || !strings.HasPrefix(record.FileName, test.wantType+"-") {
				t.Fatalf("FileName = %q, want %s-<unixmilli>.json", record.FileName, test.wantType)
			}
			if record.ID != record.FileName {
				t.Fatalf("ID = %q, want it to equal FileName", record.ID)
			}
			if record.Storage != nil {
				t.Fatal("Qoder credentials are metadata-only; Storage must stay nil")
			}
		})
	}
}

func TestQoderCoreAuthPATSource(t *testing.T) {
	token := &qoder.TokenData{AccessToken: "jt-1", TokenType: "Bearer"}
	record := toQoderCoreAuth(qoder.BuildAuthRecord(qoder.RealmCN, token, nil, qoder.AuthKindPAT))
	if got, _ := record.Metadata["auth_kind"].(string); got != "pat" {
		t.Fatalf("auth_kind = %q, want pat", got)
	}
	if got, _ := record.Metadata["source"].(string); got != "pat" {
		t.Fatalf("source = %q, want pat", got)
	}
	if _, has := record.Metadata["refresh_token"]; has {
		t.Fatal("refresh_token must be omitted when the response did not carry one")
	}
}

func TestQoderCoreAuthUserInfoOverridesToken(t *testing.T) {
	token := &qoder.TokenData{
		AccessToken: "dt-1",
		UID:         "uid-from-token",
		UserType:    "personal_professional_trial",
	}
	info := &qoder.UserInfo{
		ID:               "uid-from-userinfo",
		Name:             "alice",
		UserType:         "personal_professional",
		OrganizationID:   "org-1",
		OrganizationName: "Acme",
	}
	record := toQoderCoreAuth(qoder.BuildAuthRecord(qoder.RealmCN, token, info, qoder.AuthKindOAuth))
	if got, _ := record.Metadata["uid"].(string); got != "uid-from-userinfo" {
		t.Fatalf("uid = %q, want the userinfo value", got)
	}
	if got, _ := record.Metadata["user_type"].(string); got != "personal_professional" {
		t.Fatalf("user_type = %q", got)
	}
	if got, _ := record.Metadata["nickname"].(string); got != "alice" {
		t.Fatalf("nickname = %q", got)
	}
	if record.Label != "alice" {
		t.Fatalf("Label = %q, want the userinfo name", record.Label)
	}
	if got, _ := record.Metadata["organization_name"].(string); got != "Acme" {
		t.Fatalf("organization_name = %q", got)
	}
}

func TestQoderCoreAuthDefaultsUserType(t *testing.T) {
	record := toQoderCoreAuth(qoder.BuildAuthRecord(qoder.RealmCN, &qoder.TokenData{AccessToken: "dt-1"}, nil, qoder.AuthKindOAuth))
	if got, _ := record.Metadata["user_type"].(string); got != qoder.DefaultUserType {
		t.Fatalf("user_type = %q, want %q", got, qoder.DefaultUserType)
	}
	if _, has := record.Metadata["uid"]; has {
		t.Fatal("uid must be omitted when unknown so the executor falls back to the auth id")
	}
}
