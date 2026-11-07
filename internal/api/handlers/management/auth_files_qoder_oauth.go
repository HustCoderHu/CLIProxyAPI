package management

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestQoderToken starts Qoder (China mainland) browser authorization and
// polls for tokens.
func (h *Handler) RequestQoderToken(c *gin.Context) {
	h.requestQoderToken(c, qoder.RealmCN)
}

// RequestQoderINTLToken starts Qoder (international) browser authorization and
// polls for tokens.
func (h *Handler) RequestQoderINTLToken(c *gin.Context) {
	h.requestQoderToken(c, qoder.RealmINTL)
}

// requestQoderToken runs one realm's login. Qoder always returns HTTP 200 on
// the inference handshake but its token endpoints use ordinary status codes, so
// the poll loop is a plain device flow.
func (h *Handler) requestQoderToken(c *gin.Context, realm qoder.RealmConfig) {
	ctx := PopulateAuthContext(context.Background(), c)
	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration unavailable"})
		return
	}
	client := qoder.NewClientWithProxyURL(cfg, realm, sessionProxyURL)

	// A personal token import is a synchronous one-shot exchange: there is no
	// browser step, so no OAuth session is registered.
	if personalToken := qoderPersonalTokenFromRequest(c); personalToken != "" {
		h.importQoderPersonalToken(ctx, c, realm, sessionProxyURL, client, personalToken)
		return
	}

	flow, errStart := client.StartDeviceFlow(ctx)
	if errStart != nil {
		// The error may wrap the session proxy URL, which can carry credentials.
		log.Errorf("Failed to start %s authorization: %s", realm.Provider, redactSessionProxyInString(errStart.Error(), sessionProxyURL))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start Qoder authorization"})
		return
	}
	state := strings.TrimSpace(flow.State)
	if errState := ValidateOAuthState(state); errState != nil {
		log.WithError(errState).Errorf("%s returned invalid authorization state", realm.Provider)
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid authorization state"})
		return
	}
	RegisterOAuthSessionWithProxy(state, realm.Provider, sessionProxyURL)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, realm.Provider)
		token, errWait := client.WaitForAuthorization(pollCtx, flow)
		if errWait != nil {
			if !IsOAuthSessionPending(state, realm.Provider) {
				return
			}
			log.Errorf("%s authentication failed: %s", realm.Provider, redactSessionProxyInString(errWait.Error(), sessionProxyURL))
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			return
		}
		if !IsOAuthSessionPending(state, realm.Provider) {
			return
		}
		info := h.fetchQoderUserInfo(ctx, realm, client, token.AccessToken)
		record := toQoderCoreAuth(qoder.BuildAuthRecord(realm, token, info, qoder.AuthKindOAuth))
		h.commitAndCompleteBuiltinOAuthSession(ctx, state, realm.Provider, sessionProxyURL, record, "Failed to save authentication tokens", false)
	}()

	response := gin.H{
		"status":     "ok",
		"url":        flow.AuthURL,
		"state":      state,
		"flow":       "device",
		"expires_in": flow.ExpiresIn,
	}
	bindOAuthSessionTarget(c, state, response)
	c.JSON(http.StatusOK, response)
}

// importQoderPersonalToken exchanges a personal access token for a job token
// pair and saves it as a credential.
func (h *Handler) importQoderPersonalToken(ctx context.Context, c *gin.Context, realm qoder.RealmConfig, sessionProxyURL string, client *qoder.Client, personalToken string) {
	token, errExchange := client.ExchangePAT(ctx, personalToken)
	if errExchange != nil {
		// StatusError echoes the upstream body, which may quote the request back.
		// Log only the status so a personal token can never reach the log.
		log.Errorf("%s personal token import failed: status %d", realm.Provider, qoderStatusFromError(errExchange))
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to import personal token"})
		return
	}
	info := h.fetchQoderUserInfo(ctx, realm, client, token.AccessToken)
	recordData := qoder.BuildAuthRecord(realm, token, info, qoder.AuthKindPAT)
	recordData.Metadata["personal_token"] = personalToken
	record := toQoderCoreAuth(recordData)
	applySessionProxyToAuthRecord(record, sessionProxyURL)
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		log.Errorf("%s failed to save authentication tokens: %v", realm.Provider, errSave)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save authentication tokens"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"auth_id":   record.ID,
		"saved":     savedPath != "",
		"auth_kind": qoder.AuthKindPAT,
	})
}

// fetchQoderUserInfo fills in the identity, tolerating a lookup failure: the
// uid from the token response is enough for the COSY device fingerprint.
func (h *Handler) fetchQoderUserInfo(ctx context.Context, realm qoder.RealmConfig, client *qoder.Client, accessToken string) *qoder.UserInfo {
	info, errInfo := client.FetchUserInfo(ctx, accessToken)
	if errInfo != nil {
		log.Warnf("%s userinfo lookup failed: %v", realm.Provider, errInfo)
		return nil
	}
	return info
}

// qoderPersonalTokenFromRequest reads the personal token from the query string
// or from the v8 request body extras.
func qoderPersonalTokenFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if raw := strings.TrimSpace(c.Query("personal_token")); raw != "" {
		return raw
	}
	if extra := oauthSessionExtraMetadataFromContext(c); extra != nil {
		if raw, ok := extra["personal_token"].(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

// qoderStatusFromError extracts the upstream status for logs without echoing the
// response body, which may quote the submitted personal token back.
func qoderStatusFromError(err error) int {
	var statusErrValue *qoder.StatusError
	if errors.As(err, &statusErrValue) && statusErrValue != nil {
		return statusErrValue.Status
	}
	return 0
}

// toQoderCoreAuth turns the shared credential payload into a metadata-only auth
// entry. Storage stays nil so the token store serializes Metadata directly.
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
