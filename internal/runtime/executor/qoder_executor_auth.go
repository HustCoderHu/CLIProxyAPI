package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	qoderauthsvc "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qoderPATExtendedMarker records that a personal access token was already
// exchanged for a job token pair on this credential.
//
// The marker is required because config-synthesized credentials copy the raw
// "pt-..." token into access_token as a placeholder, so the metadata alone
// cannot distinguish an exchanged job token from the placeholder.
const qoderPATExtendedMarker = "pat_exchanged"

// QoderExecutor prepares PAT credentials before a request so the config API-key
// path exchanges "pt-..." tokens for the job tokens COSY actually signs with.
var _ cliproxyauth.RequestAuthPreparer = (*QoderExecutor)(nil)

// ShouldPrepareRequestAuth reports whether the credential still needs a
// personal-access-token exchange before it can sign COSY requests.
//
// A Qoder personal access token is not a COSY bearer: it must be traded at the
// job-token endpoint first. The config API-key path stores the PAT in both
// access_token (placeholder) and personal_token, which is exactly the state that
// must be prepared. OAuth sessions are never prepared: an active device-token
// login must not be replaced by a personal token.
func (e *QoderExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if e == nil || auth == nil {
		return false
	}
	personalToken := qoderMetadataString(auth, "personal_token")
	if personalToken == "" {
		return false
	}
	authKind := qoderMetadataString(auth, "auth_kind")
	if strings.EqualFold(authKind, qoderauthsvc.AuthKindOAuth) {
		return false
	}
	// The marker is written by PrepareRequestAuth and is the only proof that
	// this credential's access token came from a personal-token exchange.
	if exchanged, _ := auth.Metadata[qoderPATExtendedMarker].(bool); exchanged {
		return false
	}
	accessToken := qoderMetadataString(auth, "access_token")
	// Missing token, or the raw PAT copied into the access slot by the config
	// synthesizer: the credential was never exchanged. This is the broken state
	// the preparer exists to fix.
	if accessToken == "" || accessToken == personalToken {
		return true
	}
	// A distinct access token on a PAT credential is only known to be exchanged
	// when the exchange result was recorded. The exchange returns a job-family
	// refresh token, so its presence proves a completed exchange; anything else
	// (a hand-written auth file, for instance) is exchanged once to establish
	// provenance and is a no-op afterwards thanks to the marker.
	if strings.EqualFold(authKind, qoderauthsvc.AuthKindPAT) && qoderMetadataString(auth, "refresh_token") == "" {
		return true
	}
	return false
}

// PrepareRequestAuth exchanges the credential's personal access token for a job
// token pair and records the result on a clone. The auth manager persists the
// returned metadata and serializes concurrent preparation per credential.
//
// The failure mapping follows the auth manager contract: a rejected PAT is a
// credential fault (cool it and rotate), everything else is an upstream fault.
func (e *QoderExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e == nil || auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return nil, nil
	}
	updated := auth.Clone()
	personalToken := qoderMetadataString(updated, "personal_token")
	// A configured value that is not a personal access token can never be
	// exchanged. Reject it as a credential fault instead of letting the exchange
	// fail as an upstream error.
	if !strings.HasPrefix(personalToken, qoderauthsvc.PATPrefix) {
		return nil, statusErr{
			code:             http.StatusUnauthorized,
			msg:              fmt.Sprintf("qoder executor: configured api key is not a personal access token (want %q prefix)", qoderauthsvc.PATPrefix),
			credentialScoped: true,
		}
	}
	realm, ok := qoderauthsvc.RealmForProvider(e.qoderRealmProvider(updated))
	if !ok {
		realm = qoderauthsvc.RealmCN
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := qoderRefreshClientFactory(e.cfg, realm, updated.ProxyURL)
	token, errExchange := client.ExchangePAT(ctx, personalToken)
	if errExchange != nil {
		return nil, qoderPATExchangeError(errExchange)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "qoder executor: personal token exchange returned no access token"}
	}

	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = token.AccessToken
	if refreshToken := strings.TrimSpace(token.RefreshToken); refreshToken != "" {
		updated.Metadata["refresh_token"] = refreshToken
	}
	if tokenType := strings.TrimSpace(token.TokenType); tokenType != "" {
		updated.Metadata["token_type"] = tokenType
	}
	if token.ExpiresIn > 0 {
		updated.Metadata["expires_in"] = token.ExpiresIn
	}
	if !token.ExpiresAt.IsZero() {
		updated.Metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	updated.Metadata[qoderPATExtendedMarker] = true
	// The exchange response carries no identity, so personal_token, uid,
	// user_type and organization_* are preserved untouched.

	log.WithFields(log.Fields{
		"provider": e.Identifier(),
		"auth_id":  updated.ID,
	}).Debug("qoder executor: exchanged personal access token for a job token pair")
	return updated, nil
}

// qoderRealmProvider resolves the realm source for a credential. The provider
// key is authoritative; a credential without one falls back to the executor's
// own binding.
func (e *QoderExecutor) qoderRealmProvider(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if provider := strings.TrimSpace(auth.Provider); provider != "" {
			return provider
		}
	}
	if e != nil {
		return e.Identifier()
	}
	return constant.QoderCN
}

// qoderPATExchangeError maps a failed personal-token exchange onto the auth
// manager's error contract.
//
// The upstream body is deliberately left out of the message: exchange requests
// carry the personal token, and the body may echo it back.
func qoderPATExchangeError(err error) error {
	if err == nil {
		return nil
	}
	status := 0
	var statusErrValue *qoderauthsvc.StatusError
	if errors.As(err, &statusErrValue) && statusErrValue != nil {
		status = statusErrValue.Status
	}
	switch {
	case status == http.StatusUnauthorized:
		return statusErr{code: http.StatusUnauthorized, msg: "qoder executor: personal token exchange rejected (401)", credentialScoped: true}
	case status == http.StatusForbidden:
		return statusErr{code: http.StatusForbidden, msg: "qoder executor: personal token exchange forbidden (403)", credentialScoped: true}
	case status == http.StatusTooManyRequests:
		return statusErr{code: http.StatusTooManyRequests, msg: "qoder executor: personal token exchange rate limited (429)"}
	case status >= 400 && status < 500:
		return statusErr{code: status, msg: fmt.Sprintf("qoder executor: personal token exchange failed (status %d)", status)}
	case status >= 500:
		return statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("qoder executor: personal token exchange failed (upstream status %d)", status)}
	default:
		return statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("qoder executor: personal token exchange failed: %v", err)}
	}
}
