// Package qoder implements the Qoder device authorization, personal access
// token (PAT) exchange and token refresh flows for both the China-mainland and
// international realms.
//
// The upstream business endpoints are plain Bearer APIs on the openapi host;
// they are not COSY-signed. Signing only applies to the inference gateway and is
// implemented in internal/runtime/executor/helps.
package qoder

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// Token endpoint paths, all relative to the realm openapi base URL. Copied
// verbatim from the reference client (qoder_accounts.py:97-101).
const (
	pathDevicePoll    = "/api/v1/deviceToken/poll"
	pathDeviceRefresh = "/api/v1/deviceToken/refresh"
	pathJobExchange   = "/api/v1/jobToken/exchange"
	pathJobRefresh    = "/api/v1/jobToken/refresh"
	pathUserInfo      = "/api/v1/userinfo"
)

const (
	defaultPollInterval = 5 * time.Second
	maxPollDuration     = 10 * time.Minute
	maxResponseBytes    = 1 << 20
	tokenRequestTimeout = 30 * time.Second

	// devicePollUserAgent is the UA the reference client sends on the
	// unauthenticated device poll. It deliberately differs from the realm
	// desktop UA.
	devicePollUserAgent = "QoderWork"

	// PATPrefix is the personal access token prefix the exchange accepts.
	PATPrefix = "pt-"

	// AuthKindOAuth marks a credential obtained through the browser device flow.
	AuthKindOAuth = "oauth"
	// AuthKindPAT marks a credential imported from a personal access token.
	AuthKindPAT = "pat"

	// DefaultUserType is used when userinfo does not report a user type.
	DefaultUserType = "personal_professional_trial"
)

// RealmConfig holds every realm-specific constant. The two realms are separate
// upstream deployments with their own credentials, so nothing is shared.
type RealmConfig struct {
	// Name is the short realm name, "cn" or "intl".
	Name string
	// Provider is the CLIProxyAPI provider key, "qoder" or "qoder-intl".
	Provider string
	// OpenAPIBase hosts the token and userinfo endpoints.
	OpenAPIBase string
	// GatewayBase hosts the COSY-signed inference endpoint.
	GatewayBase string
	// WebsiteBase is the browser authorization host.
	WebsiteBase string
	ClientID    string
	RedirectURI string
	Domain      string
	// UserAgent is the realm desktop client UA.
	UserAgent string
	// DashedNonce selects a hyphenated UUID nonce at device authorization.
	// CN uses the dashed form, the international realm a 32-character hex one.
	DashedNonce bool
	// SendRedirectURI adds redirect_uri to the authorization URL. The
	// international realm's newer protocol omits it.
	SendRedirectURI bool
}

// RealmCN is the China-mainland deployment.
var RealmCN = RealmConfig{
	Name:            "cn",
	Provider:        "qoder",
	OpenAPIBase:     "https://openapi.qoder.com.cn",
	GatewayBase:     "https://gateway.qoder.com.cn",
	WebsiteBase:     "https://qoder.com.cn",
	ClientID:        "1c5e33e1-364d-4ce6-b02c-acaa81274a5c",
	RedirectURI:     "qoder-work-cn://",
	Domain:          "qoder.com.cn",
	UserAgent:       "QoderWork/1.1.75",
	DashedNonce:     true,
	SendRedirectURI: true,
}

// RealmINTL is the international deployment.
var RealmINTL = RealmConfig{
	Name:            "intl",
	Provider:        "qoder-intl",
	OpenAPIBase:     "https://openapi.qoder.sh",
	GatewayBase:     "https://api3.qoder.sh",
	WebsiteBase:     "https://qoder.com",
	ClientID:        "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb",
	RedirectURI:     "qoder://aicoding.aicoding-agent/login-success",
	Domain:          "qoder.com",
	UserAgent:       "Qoder/1.1.75",
	DashedNonce:     false,
	SendRedirectURI: false,
}

// RealmForProvider maps a provider key onto its realm configuration.
func RealmForProvider(provider string) (RealmConfig, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "qoder", "qoder-cn", "cn":
		return RealmCN, true
	case "qoder-intl", "qoder-international", "intl":
		return RealmINTL, true
	default:
		return RealmConfig{}, false
	}
}

// RealmByName maps a short realm name ("cn" / "intl") onto its configuration.
func RealmByName(name string) (RealmConfig, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cn", "qoder", "":
		return RealmCN, true
	case "intl", "international", "qoder-intl":
		return RealmINTL, true
	default:
		return RealmConfig{}, false
	}
}

// NormalizeRealm returns the canonical realm name, defaulting to CN.
func NormalizeRealm(name string) string {
	realm, ok := RealmByName(name)
	if !ok {
		return RealmCN.Name
	}
	return realm.Name
}

// TokenData is a normalized token response. ExpiresIn is always seconds:
// the upstream reports milliseconds and the conversion happens at parse time.
type TokenData struct {
	AccessToken      string
	RefreshToken     string
	TokenType        string
	ExpiresIn        int
	ExpiresAt        time.Time
	UID              string
	UserType         string
	OrganizationID   string
	OrganizationName string
}

// UserInfo is the identity payload returned by /api/v1/userinfo.
type UserInfo struct {
	ID               string
	Name             string
	UserType         string
	OrganizationID   string
	OrganizationName string
}

// DeviceFlow carries everything needed to finish a pending authorization.
type DeviceFlow struct {
	// State is the local OAuth session identifier, not an upstream value.
	State    string
	AuthURL  string
	Interval time.Duration
	// ExpiresIn is the authorization window in seconds.
	ExpiresIn int

	nonce    string
	verifier string
}

// Client performs Qoder credential-acquisition requests for one realm.
type Client struct {
	realm      RealmConfig
	httpClient *http.Client
}

// NewClient creates a proxy-aware client for the given realm.
func NewClient(cfg *config.Config, realm RealmConfig) *Client {
	return NewClientWithProxyURL(cfg, realm, "")
}

// NewClientWithProxyURL creates a client with an optional per-credential proxy.
func NewClientWithProxyURL(cfg *config.Config, realm RealmConfig, proxyURL string) *Client {
	if realm.OpenAPIBase == "" {
		realm = RealmCN
	}
	// The client only ever talks to token endpoints, so the credential
	// acquisition timeout exception applies here.
	httpClient := &http.Client{Timeout: tokenRequestTimeout}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
	}
	if strings.TrimSpace(proxyURL) != "" {
		sdkCfg.ProxyURL = strings.TrimSpace(proxyURL)
	}
	httpClient = util.SetProxy(&sdkCfg, httpClient)
	return newClient(httpClient, realm)
}

// newClient is the test seam.
func newClient(httpClient *http.Client, realm RealmConfig) *Client {
	return &Client{realm: realm, httpClient: httpClient}
}

// Realm returns the realm this client is bound to.
func (c *Client) Realm() RealmConfig {
	if c == nil {
		return RealmCN
	}
	return c.realm
}

// StartDeviceFlow builds the browser authorization URL and the PKCE material
// the poll endpoint needs. Qoder does not expose a device-code endpoint: the
// nonce and verifier are generated locally and the authorization URL is
// assembled from realm constants.
func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceFlow, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	verifier, challenge, errPKCE := newPKCE()
	if errPKCE != nil {
		return nil, errPKCE
	}
	nonce, errNonce := c.newNonce()
	if errNonce != nil {
		return nil, errNonce
	}
	// Query parameter order mirrors the reference client so the URL stays
	// byte-identical for diagnostics.
	query := []string{
		"challenge=" + url.QueryEscape(challenge),
		"challenge_method=S256",
		"nonce=" + url.QueryEscape(nonce),
	}
	// CN sends redirect_uri; the international realm's protocol omits it.
	if c.realm.SendRedirectURI && strings.TrimSpace(c.realm.RedirectURI) != "" {
		query = append(query, "redirect_uri="+url.QueryEscape(c.realm.RedirectURI))
	}
	query = append(query,
		"client_id="+url.QueryEscape(c.realm.ClientID),
		"machine_id="+url.QueryEscape(uuid.NewString()),
	)
	return &DeviceFlow{
		State:     fmt.Sprintf("qd-%d", time.Now().UnixNano()),
		AuthURL:   strings.TrimRight(c.realm.WebsiteBase, "/") + "/device/selectAccounts?" + strings.Join(query, "&"),
		Interval:  defaultPollInterval,
		ExpiresIn: int(maxPollDuration / time.Second),
		nonce:     nonce,
		verifier:  verifier,
	}, nil
}

// WaitForAuthorization polls until the browser authorization completes.
func (c *Client) WaitForAuthorization(ctx context.Context, flow *DeviceFlow) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if flow == nil || strings.TrimSpace(flow.nonce) == "" || strings.TrimSpace(flow.verifier) == "" {
		return nil, fmt.Errorf("qoder: device flow state is required")
	}
	interval := flow.Interval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := time.Now().Add(maxPollDuration)
	if flow.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(flow.ExpiresIn) * time.Second)
	}
	for {
		token, pending, errPoll := c.pollToken(ctx, flow)
		if errPoll != nil {
			return nil, errPoll
		}
		if !pending {
			return token, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("qoder: authorization expired")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("qoder: authorization cancelled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (c *Client) pollToken(ctx context.Context, flow *DeviceFlow) (*TokenData, bool, error) {
	query := "nonce=" + url.QueryEscape(flow.nonce) +
		"&verifier=" + url.QueryEscape(flow.verifier) +
		"&challenge_method=S256"
	endpoint := strings.TrimRight(c.realm.OpenAPIBase, "/") + pathDevicePoll + "?" + query
	status, raw, errDo := c.do(ctx, http.MethodGet, endpoint, nil, map[string]string{
		"Accept":     "application/json",
		"User-Agent": devicePollUserAgent,
	})
	if errDo != nil {
		return nil, false, errDo
	}
	// 404 and 202 both mean "the user has not finished authorizing yet".
	if status == http.StatusNotFound || status == http.StatusAccepted {
		return nil, true, nil
	}
	if status != http.StatusOK {
		return nil, false, &StatusError{Operation: pathDevicePoll, Status: status, Body: string(raw)}
	}
	doc := ojson.ParseBytes(raw)
	token := firstNonEmpty(strings.TrimSpace(doc.Get("token").String()), strings.TrimSpace(doc.Get("device_token").String()))
	if token == "" {
		return nil, true, nil
	}
	data := parseTokenDocument(doc, token)
	data.UID = strings.TrimSpace(doc.Get("user_id").String())
	return data, false, nil
}

// StatusError reports a non-2xx upstream status. It exposes StatusCode so the
// credential conductor can classify a dead refresh token without parsing text.
type StatusError struct {
	Operation string
	Status    int
	Body      string
}

// Error implements error.
func (e *StatusError) Error() string {
	if e == nil {
		return "qoder: status error"
	}
	body := strings.TrimSpace(e.Body)
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	return fmt.Sprintf("qoder: %s failed with status %d: %s", e.Operation, e.Status, body)
}

// StatusCode exposes the upstream HTTP status for error classification.
func (e *StatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Status
}

// sessionDeadMarkers are the upstream strings that mean the session can never
// be refreshed again and the user has to sign in. Copied from the reference
// implementation (qoder_accounts.py:935).
var sessionDeadMarkers = []string{"TOKEN_EXPIRE", "12153", "Offline user session not found"}

// IsSessionDead reports whether an upstream body means the credential must be
// re-created rather than refreshed.
func IsSessionDead(body string) bool {
	for _, marker := range sessionDeadMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// Refresh rotates a token pair. The routed endpoint depends on the refresh
// token prefix; a PAT is only ever used as a fallback for job tokens because an
// active OAuth session must not be replaced by the personal token.
func (c *Client) Refresh(ctx context.Context, refreshToken, personalToken string) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch RefreshRoute(refreshToken, personalToken) {
	case RefreshDevice:
		return c.refreshDevice(ctx, refreshToken)
	case RefreshJob:
		token, errJob := c.refreshJob(ctx, refreshToken)
		if errJob == nil {
			return token, nil
		}
		if strings.TrimSpace(personalToken) == "" {
			return nil, errJob
		}
		token, errPAT := c.ExchangePAT(ctx, personalToken)
		if errPAT != nil {
			return nil, fmt.Errorf("qoder: job token refresh failed (%v) and PAT exchange failed: %w", errJob, errPAT)
		}
		return token, nil
	case RefreshPAT:
		return c.ExchangePAT(ctx, personalToken)
	default:
		return nil, ErrNoRefreshCredential
	}
}

func (c *Client) refreshDevice(ctx context.Context, refreshToken string) (*TokenData, error) {
	return c.postToken(ctx, pathDeviceRefresh, map[string]any{
		"refresh_token": strings.TrimSpace(refreshToken),
	}, true)
}

func (c *Client) refreshJob(ctx context.Context, refreshToken string) (*TokenData, error) {
	return c.postToken(ctx, pathJobRefresh, map[string]any{
		"refresh_token": strings.TrimSpace(refreshToken),
	}, false)
}

// ExchangePAT trades a personal access token for a job token pair.
func (c *Client) ExchangePAT(ctx context.Context, personalToken string) (*TokenData, error) {
	personalToken = strings.TrimSpace(personalToken)
	if personalToken == "" {
		return nil, fmt.Errorf("qoder: personal token is required")
	}
	if !strings.HasPrefix(personalToken, PATPrefix) {
		return nil, fmt.Errorf("qoder: personal token must start with %q", PATPrefix)
	}
	return c.postToken(ctx, pathJobExchange, map[string]any{
		"personal_token": personalToken,
	}, false)
}

func (c *Client) postToken(ctx context.Context, path string, payload map[string]any, deviceFamily bool) (*TokenData, error) {
	body, errBuild := encodeJSON(payload)
	if errBuild != nil {
		return nil, errBuild
	}
	endpoint := strings.TrimRight(c.realm.OpenAPIBase, "/") + path
	status, raw, errDo := c.do(ctx, http.MethodPost, endpoint, body, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   c.realm.UserAgent,
	})
	if errDo != nil {
		return nil, errDo
	}
	if status != http.StatusOK {
		return nil, &StatusError{Operation: path, Status: status, Body: string(raw)}
	}
	doc := ojson.ParseBytes(raw)
	token := strings.TrimSpace(doc.Get("token").String())
	if deviceFamily {
		token = firstNonEmpty(token, strings.TrimSpace(doc.Get("device_token").String()))
	}
	if token == "" {
		return nil, fmt.Errorf("qoder: %s returned no token", path)
	}
	return parseTokenDocument(doc, token), nil
}

// FetchUserInfo reads the authenticated identity.
func (c *Client) FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("qoder: access token is required")
	}
	endpoint := strings.TrimRight(c.realm.OpenAPIBase, "/") + pathUserInfo
	status, raw, errDo := c.do(ctx, http.MethodGet, endpoint, nil, map[string]string{
		"Accept":        "application/json",
		"User-Agent":    c.realm.UserAgent,
		"Authorization": "Bearer " + accessToken,
	})
	if errDo != nil {
		return nil, errDo
	}
	if status != http.StatusOK {
		return nil, &StatusError{Operation: pathUserInfo, Status: status, Body: string(raw)}
	}
	doc := ojson.ParseBytes(raw)
	info := &UserInfo{
		ID:               strings.TrimSpace(doc.Get("id").String()),
		Name:             strings.TrimSpace(doc.Get("name").String()),
		UserType:         strings.TrimSpace(doc.Get("user_type").String()),
		OrganizationID:   firstNonEmpty(strings.TrimSpace(doc.Get("organization_id").String()), strings.TrimSpace(doc.Get("organizationId").String())),
		OrganizationName: firstNonEmpty(strings.TrimSpace(doc.Get("organization_name").String()), strings.TrimSpace(doc.Get("organizationName").String())),
	}
	if info.ID == "" {
		info.ID = strings.TrimSpace(doc.Get("user_id").String())
	}
	if info.UserType == "" {
		info.UserType = DefaultUserType
	}
	return info, nil
}

// do issues one credential-acquisition request and returns the status plus the
// raw body. Non-2xx responses are reported to the caller, not turned into
// transport errors, so each flow can apply its own status semantics.
func (c *Client) do(ctx context.Context, method, endpoint string, body []byte, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, errReq := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if errReq != nil {
		return 0, nil, fmt.Errorf("qoder: create request: %w", errReq)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, errDo := c.httpClient.Do(req)
	if errDo != nil {
		return 0, nil, fmt.Errorf("qoder: %s %s failed: %w", method, endpoint, errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder: close response body: %v", errClose)
		}
	}()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if errRead != nil {
		return 0, nil, fmt.Errorf("qoder: read response: %w", errRead)
	}
	return resp.StatusCode, raw, nil
}

func (c *Client) newNonce() (string, error) {
	id, errUUID := uuid.NewRandom()
	if errUUID != nil {
		return "", fmt.Errorf("qoder: generate nonce: %w", errUUID)
	}
	if c.realm.DashedNonce {
		return id.String(), nil
	}
	return hex.EncodeToString(id[:]), nil
}

// newPKCE returns an RFC 7636 S256 verifier/challenge pair.
func newPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", "", fmt.Errorf("qoder: generate pkce verifier: %w", errRead)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// AuthRecord is the credential payload produced by a completed login. It is a
// plain value so both the CLI authenticator and the management API can turn it
// into an SDK auth record without importing each other.
type AuthRecord struct {
	Provider   string
	FileName   string
	Label      string
	Metadata   map[string]any
	Attributes map[string]string
}

// BuildAuthRecord merges the token response and the optional userinfo into the
// auth-file shape. The metadata "type" must equal the provider key exactly: the
// file synthesizer derives the provider from it on reload.
//
// organization_id is always written as a string. The auth-file read path
// decodes JSON numbers as float64, which silently loses digits of the 19-digit
// organization identifiers Qoder issues.
func BuildAuthRecord(realm RealmConfig, token *TokenData, info *UserInfo, authKind string) AuthRecord {
	label := "Qoder"
	if realm.Name == RealmINTL.Name {
		label = "Qoder (International)"
	}
	if info != nil && strings.TrimSpace(info.Name) != "" {
		label = info.Name
	}

	uid := ""
	userType := DefaultUserType
	orgID := ""
	orgName := ""
	if token != nil {
		uid = strings.TrimSpace(token.UID)
		if strings.TrimSpace(token.UserType) != "" {
			userType = token.UserType
		}
		orgID = strings.TrimSpace(token.OrganizationID)
		orgName = strings.TrimSpace(token.OrganizationName)
	}
	if info != nil {
		// userinfo is authoritative when present (the reference implementation
		// does the same); the poll response only supplies the user id.
		uid = firstNonEmpty(strings.TrimSpace(info.ID), uid)
		if strings.TrimSpace(info.UserType) != "" {
			userType = info.UserType
		}
		orgID = firstNonEmpty(strings.TrimSpace(info.OrganizationID), orgID)
		orgName = firstNonEmpty(strings.TrimSpace(info.OrganizationName), orgName)
	}

	source := "oauth"
	if authKind == AuthKindPAT {
		source = "pat"
	}

	metadata := map[string]any{
		"type":            realm.Provider,
		"auth_kind":       authKind,
		"source":          source,
		"base_url":        realm.GatewayBase,
		"domain":          realm.Domain,
		"timestamp":       time.Now().UnixMilli(),
		"user_type":       userType,
		"organization_id": orgID,
	}
	if orgName != "" {
		metadata["organization_name"] = orgName
	}
	if uid != "" {
		metadata["uid"] = uid
	}
	if info != nil && strings.TrimSpace(info.Name) != "" {
		metadata["nickname"] = info.Name
	}
	if token != nil {
		metadata["access_token"] = token.AccessToken
		metadata["token_type"] = token.TokenType
		if strings.TrimSpace(token.RefreshToken) != "" {
			metadata["refresh_token"] = token.RefreshToken
		}
		if token.ExpiresIn > 0 {
			metadata["expires_in"] = token.ExpiresIn
		}
		if !token.ExpiresAt.IsZero() {
			metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}

	return AuthRecord{
		Provider: realm.Provider,
		FileName: fmt.Sprintf("%s-%d.json", realm.Provider, time.Now().UnixMilli()),
		Label:    label,
		Metadata: metadata,
		Attributes: map[string]string{
			// Plain keys keep this package free of an sdk dependency; they match
			// coreauth.AttributeAuthKind and the conventional base_url/domain keys.
			// Both credential kinds are refreshable bearer tokens, so the runtime
			// kind is OAuth; the upstream provenance lives in metadata auth_kind.
			"auth_kind": AuthKindOAuth,
			"base_url":  realm.GatewayBase,
			"domain":    realm.Domain,
		},
	}
}

// parseTokenDocument normalizes a token response. Qoder reports expires_in in
// milliseconds; a missed conversion pushes expiry roughly 80 years out and the
// automatic refresh loop would never fire.
func parseTokenDocument(doc ojson.Result, accessToken string) *TokenData {
	data := &TokenData{
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(doc.Get("refresh_token").String()),
		TokenType:    strings.TrimSpace(doc.Get("token_type").String()),
	}
	if data.TokenType == "" {
		data.TokenType = "Bearer"
	}
	expiresIn := doc.Get("expires_in").Int()
	if expiresIn > 0 {
		data.ExpiresIn = int(expiresIn / 1000)
		if data.ExpiresIn <= 0 {
			// Sub-second lifetimes do not occur upstream, but rounding one down
			// to zero would report an already-expired credential.
			data.ExpiresIn = 1
		}
		data.ExpiresAt = time.Now().Add(time.Duration(data.ExpiresIn) * time.Second)
	} else if rawExpiry := strings.TrimSpace(doc.Get("expires_at").String()); rawExpiry != "" {
		if parsed, errParse := time.Parse(time.RFC3339, rawExpiry); errParse == nil {
			data.ExpiresAt = parsed
			if remaining := time.Until(parsed); remaining > 0 {
				data.ExpiresIn = int(remaining / time.Second)
			}
		} else if parsed, errParse = time.Parse("2006-01-02T15:04:05", rawExpiry); errParse == nil {
			data.ExpiresAt = parsed
		}
	}
	return data
}

// encodeJSON renders a small request payload with ojson.
func encodeJSON(payload map[string]any) ([]byte, error) {
	doc := ojson.NewDocString("{}")
	for key, value := range payload {
		if errSet := doc.Set(key, value); errSet != nil {
			return nil, fmt.Errorf("qoder: encode request: %w", errSet)
		}
	}
	return doc.Bytes(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
