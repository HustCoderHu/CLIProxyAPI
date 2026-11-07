package qoder

import (
	"errors"
	"strings"
)

// RefreshKind selects which upstream endpoint rotates a credential.
type RefreshKind int

const (
	// RefreshNone means there is nothing to rotate.
	RefreshNone RefreshKind = iota
	// RefreshDevice is the OAuth device-token family (drt- refresh tokens).
	RefreshDevice
	// RefreshJob is the job-token family (jrt- refresh tokens).
	RefreshJob
	// RefreshPAT re-exchanges a personal access token for a job token.
	RefreshPAT
)

// ErrNoRefreshCredential is returned when a refresh is requested without any
// refresh token or personal token.
var ErrNoRefreshCredential = errors.New("qoder: no refresh token or personal token")

// Token prefixes used for routing. Copied from the reference client
// (qoder_accounts.py:2198-2219): device tokens carry drt- refresh tokens, job
// tokens carry jrt- refresh tokens.
const (
	deviceRefreshPrefix = "drt-"
	jobRefreshPrefix    = "jrt-"
)

// RefreshRoute decides how to rotate a credential.
//
// Precedence is fixed and deliberate: an active OAuth session must never be
// replaced by a personal access token. A drt- refresh token always routes to the
// device-token endpoint even when a PAT is also present; the PAT only acts as a
// fallback when the job-token refresh token is missing or rejected (the caller
// implements that fallback after a RefreshJob error).
func RefreshRoute(refreshToken, personalToken string) RefreshKind {
	refreshToken = strings.TrimSpace(refreshToken)
	personalToken = strings.TrimSpace(personalToken)

	switch {
	case strings.HasPrefix(refreshToken, deviceRefreshPrefix):
		return RefreshDevice
	case strings.HasPrefix(refreshToken, jobRefreshPrefix):
		return RefreshJob
	case refreshToken != "":
		// Unknown prefix: the upstream only issues the two families above, but a
		// legacy or rotated token shape still belongs to the job family in
		// practice (the reference client posts anything non-drt- to jobToken).
		return RefreshJob
	case personalToken != "":
		return RefreshPAT
	default:
		return RefreshNone
	}
}
