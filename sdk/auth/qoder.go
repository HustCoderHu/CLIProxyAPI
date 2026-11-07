package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qoderRefreshLead refreshes 30 minutes before expiry. Device-family tokens are
// long-lived (30 days), so a short lead keeps clock skew from invalidating an
// in-flight request without refreshing needlessly.
var qoderRefreshLead = 30 * time.Minute

// QoderAuthenticator implements the Qoder device authorization and personal
// access token flows. One implementation serves both realms; the realm decides
// the provider key and every upstream constant.
type QoderAuthenticator struct {
	realm qoder.RealmConfig
}

// NewQoderAuthenticator constructs the China-mainland authenticator.
func NewQoderAuthenticator() Authenticator {
	return &QoderAuthenticator{realm: qoder.RealmCN}
}

// NewQoderINTLAuthenticator constructs the international authenticator.
func NewQoderINTLAuthenticator() Authenticator {
	return &QoderAuthenticator{realm: qoder.RealmINTL}
}

// Provider returns the provider key for this realm.
func (a QoderAuthenticator) Provider() string {
	if a.realm.Provider != "" {
		return a.realm.Provider
	}
	return qoder.RealmCN.Provider
}

// RefreshLead instructs the runtime to refresh shortly before expiry.
func (QoderAuthenticator) RefreshLead() *time.Duration { return &qoderRefreshLead }

// Login runs the browser device authorization flow, or imports a personal
// access token when LoginOptions.Metadata carries "personal_token".
func (a QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	realm := a.realm
	if realm.OpenAPIBase == "" {
		realm = qoder.RealmCN
	}
	client := qoder.NewClientWithProxyURL(cfg, realm, opts.Metadata["proxy_url"])

	if personalToken := strings.TrimSpace(opts.Metadata["personal_token"]); personalToken != "" {
		return loginQoderWithPAT(ctx, client, realm, personalToken)
	}

	fmt.Printf("Starting %s authentication...\n", qoderDisplayName(realm))
	flow, errStart := client.StartDeviceFlow(ctx)
	if errStart != nil {
		return nil, errStart
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", flow.AuthURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(flow.AuthURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	token, errWait := client.WaitForAuthorization(ctx, flow)
	if errWait != nil {
		return nil, errWait
	}
	return toQoderCoreAuth(qoder.BuildAuthRecord(realm, token, fetchQoderUserInfo(ctx, client, token.AccessToken), qoder.AuthKindOAuth)), nil
}

// loginQoderWithPAT exchanges a personal access token for a job token pair.
func loginQoderWithPAT(ctx context.Context, client *qoder.Client, realm qoder.RealmConfig, personalToken string) (*coreauth.Auth, error) {
	token, errExchange := client.ExchangePAT(ctx, personalToken)
	if errExchange != nil {
		return nil, errExchange
	}
	record := qoder.BuildAuthRecord(realm, token, fetchQoderUserInfo(ctx, client, token.AccessToken), qoder.AuthKindPAT)
	record.Metadata["personal_token"] = personalToken
	return toQoderCoreAuth(record), nil
}

// fetchQoderUserInfo fills in the identity. It is best effort: a userinfo
// failure must not discard an otherwise valid credential, because the uid from
// the token response is enough for the COSY device fingerprint.
func fetchQoderUserInfo(ctx context.Context, client *qoder.Client, accessToken string) *qoder.UserInfo {
	info, errInfo := client.FetchUserInfo(ctx, accessToken)
	if errInfo != nil {
		log.Warnf("Qoder userinfo lookup failed: %v", errInfo)
		return nil
	}
	return info
}

// toQoderCoreAuth turns the shared record into a metadata-only auth entry.
// Storage stays nil: the file token store serializes Metadata directly.
func toQoderCoreAuth(record qoder.AuthRecord) *coreauth.Auth {
	attributes := make(map[string]string, len(record.Attributes))
	for key, value := range record.Attributes {
		attributes[key] = value
	}
	return &coreauth.Auth{
		ID:         record.FileName,
		Provider:   record.Provider,
		FileName:   record.FileName,
		Label:      record.Label,
		Metadata:   record.Metadata,
		Attributes: attributes,
	}
}

func qoderDisplayName(realm qoder.RealmConfig) string {
	if realm.Name == qoder.RealmINTL.Name {
		return "Qoder (International)"
	}
	return "Qoder"
}
