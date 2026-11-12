package helps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// OpenCodeBaselineVersion is the built-in baseline version.
	OpenCodeBaselineVersion = "2.0.32"
	// OpenCodeBaselineUserAgent is the built-in baseline User-Agent.
	OpenCodeBaselineUserAgent = "opencode/latest/2.0.32/cli"
)

// openCodeRuntimeState holds the OpenCode runtime version and User-Agent.
type openCodeRuntimeState struct {
	mu        sync.RWMutex
	version   string
	userAgent string
	lastSync  time.Time
}

var (
	openCodeState = openCodeRuntimeState{
		version:   OpenCodeBaselineVersion,
		userAgent: OpenCodeBaselineUserAgent,
	}
	openCodeUpdaterOnce sync.Once
)

// CurrentOpenCodeUserAgent returns the effective User-Agent.
func CurrentOpenCodeUserAgent() string {
	openCodeState.mu.RLock()
	defer openCodeState.mu.RUnlock()
	return openCodeState.userAgent
}

// CurrentOpenCodeVersion returns the effective OpenCode version.
func CurrentOpenCodeVersion() string {
	openCodeState.mu.RLock()
	defer openCodeState.mu.RUnlock()
	return openCodeState.version
}

// SetOpenCodeVersion updates the current version and User-Agent atomically.
func SetOpenCodeVersion(v string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return
	}
	openCodeState.mu.Lock()
	defer openCodeState.mu.Unlock()
	openCodeState.version = v
	openCodeState.userAgent = fmt.Sprintf("opencode/latest/%s/cli", v)
	openCodeState.lastSync = time.Now()
}

// ResetOpenCodeStateForTest resets the runtime state and sync.Once. Test use only.
func ResetOpenCodeStateForTest() {
	openCodeState.mu.Lock()
	defer openCodeState.mu.Unlock()
	openCodeState.version = OpenCodeBaselineVersion
	openCodeState.userAgent = OpenCodeBaselineUserAgent
	openCodeState.lastSync = time.Time{}
	openCodeUpdaterOnce = sync.Once{}
}

type semver struct {
	major int
	minor int
	patch int
}

func parseSemver(v string) (semver, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return semver{}, fmt.Errorf("invalid semver: %s", v)
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return semver{}, err
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return semver{}, err
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return semver{}, err
	}
	return semver{major: maj, minor: min, patch: patch}, nil
}

// CompareSemver compares two semantic versions: 1 when v1 > v2, -1 when v1 < v2, 0 when equal.
func CompareSemver(v1, v2 string) int {
	s1, err1 := parseSemver(v1)
	s2, err2 := parseSemver(v2)
	if err1 != nil && err2 != nil {
		return strings.Compare(v1, v2)
	}
	if err1 != nil {
		return -1
	}
	if err2 != nil {
		return 1
	}
	if s1.major != s2.major {
		if s1.major > s2.major {
			return 1
		}
		return -1
	}
	if s1.minor != s2.minor {
		if s1.minor > s2.minor {
			return 1
		}
		return -1
	}
	if s1.patch != s2.patch {
		if s1.patch > s2.patch {
			return 1
		}
		return -1
	}
	return 0
}

// buildUpdaterHTTPClient builds an HTTP client from the proxy configuration and environment.
func buildUpdaterHTTPClient(proxyURL, githubToken string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
	}
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed != "" {
		transport, mode, err := proxyutil.BuildHTTPTransport(trimmed)
		if err == nil && transport != nil {
			client.Transport = transport
			return client
		}
		if mode == proxyutil.ModeDirect {
			client.Transport = proxyutil.NewDirectTransport()
			return client
		}
		log.Debugf("opencode updater: failed to build transport for proxy %s: %v, falling back to environment proxy", proxyutil.Redact(trimmed), err)
	}
	client.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	return client
}

// fetchVersionFromOfficial fetches the latest version from the OpenCode update CDN.
func fetchVersionFromOfficial(ctx context.Context, client *http.Client) (string, error) {
	const officialURL = "https://opencode.ai/update/api/latest/cli/npm"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, officialURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("opencode updater: close official body: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("official update endpoint status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("unmarshal official update response: %w", err)
	}
	version := strings.TrimSpace(data.Version)
	if _, err := parseSemver(version); err != nil {
		return "", fmt.Errorf("invalid semver from official update: %q: %w", version, err)
	}
	return strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V"), nil
}

// fetchVersionFromGitHub fetches the highest v2.x tag from the GitHub git matching-refs API.
func fetchVersionFromGitHub(ctx context.Context, client *http.Client, token string) (string, error) {
	const githubURL = "https://api.github.com/repos/anomalyco/opencode/git/matching-refs/tags/v2."
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CLIProxyAPI")
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken != "" {
		req.Header.Set("Authorization", "Bearer "+trimmedToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("opencode updater: close github body: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github matching-refs endpoint status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}

	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &refs); err != nil {
		return "", fmt.Errorf("unmarshal github matching-refs response: %w", err)
	}

	var highestVer string
	for _, item := range refs {
		ref := strings.TrimSpace(item.Ref)
		const tagPrefix = "refs/tags/"
		if !strings.HasPrefix(ref, tagPrefix) {
			continue
		}
		rawTag := strings.TrimPrefix(ref, tagPrefix)
		cleanVer := strings.TrimPrefix(strings.TrimPrefix(rawTag, "v"), "V")
		if _, err := parseSemver(cleanVer); err != nil {
			continue
		}
		if highestVer == "" || CompareSemver(cleanVer, highestVer) > 0 {
			highestVer = cleanVer
		}
	}

	if highestVer == "" {
		return "", errors.New("no valid v2 tags found in github matching-refs response")
	}
	return highestVer, nil
}

// fetchVersionFromNPM fetches the latest version from the npm registry.
func fetchVersionFromNPM(ctx context.Context, client *http.Client) (string, error) {
	const npmURL = "https://registry.npmjs.org/@opencode/cli/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, npmURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("opencode updater: close npm body: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry endpoint status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("unmarshal npm response: %w", err)
	}
	version := strings.TrimSpace(data.Version)
	if _, err := parseSemver(version); err != nil {
		return "", fmt.Errorf("invalid semver from npm: %q: %w", version, err)
	}
	return strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V"), nil
}

// FetchLatestOpenCodeVersion discovers the latest version through a three-tier fallback chain.
func FetchLatestOpenCodeVersion(ctx context.Context, client *http.Client, githubToken string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	// First priority: the official update endpoint.
	if v, err := fetchVersionFromOfficial(ctx, client); err == nil && v != "" {
		return v, nil
	} else if err != nil {
		log.Debugf("opencode updater: official endpoint failed: %v", err)
	}

	// Second priority: the GitHub matching-refs API.
	if v, err := fetchVersionFromGitHub(ctx, client, githubToken); err == nil && v != "" {
		return v, nil
	} else if err != nil {
		log.Debugf("opencode updater: github tags endpoint failed: %v", err)
	}

	// Third priority: the npm registry mirror.
	if v, err := fetchVersionFromNPM(ctx, client); err == nil && v != "" {
		return v, nil
	} else if err != nil {
		log.Debugf("opencode updater: npm registry endpoint failed: %v", err)
	}

	return "", errors.New("all OpenCode version discovery sources failed")
}

func resolveOpenCodeProxyURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, compat := range cfg.OpenAICompatibility {
		if strings.EqualFold(compat.Name, "opencode") || strings.Contains(strings.ToLower(compat.BaseURL), "opencode.ai") {
			for _, entry := range compat.APIKeyEntries {
				if p := strings.TrimSpace(entry.ProxyURL); p != "" {
					return p
				}
			}
		}
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

func resolveOpenCodeGitHubToken(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.GitHubToken) != "" {
		return strings.TrimSpace(cfg.GitHubToken)
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

// SyncOpenCodeVersion fetches the latest version and atomically stores it when newer.
func SyncOpenCodeVersion(ctx context.Context, cfg *config.Config) error {
	proxyURL := resolveOpenCodeProxyURL(cfg)
	ghToken := resolveOpenCodeGitHubToken(cfg)
	client := buildUpdaterHTTPClient(proxyURL, ghToken, 10*time.Second)

	v, err := FetchLatestOpenCodeVersion(ctx, client, ghToken)
	if err != nil {
		return err
	}

	current := CurrentOpenCodeVersion()
	if CompareSemver(v, current) > 0 {
		SetOpenCodeVersion(v)
		log.Infof("opencode updater: synced upstream version to %s", v)
	} else {
		log.Debugf("opencode updater: current version %s is up-to-date (upstream: %s)", current, v)
	}
	return nil
}

// StartOpenCodeUpdater starts the OpenCode version updater: an async initial fetch followed by 6-hour polling.
func StartOpenCodeUpdater(ctx context.Context, cfg *config.Config) {
	openCodeUpdaterOnce.Do(func() {
		go func() {
			syncCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := SyncOpenCodeVersion(syncCtx, cfg); err != nil {
				log.Warnf("opencode updater: initial version sync failed, using baseline %s: %v", CurrentOpenCodeVersion(), err)
			}
		}()

		go func() {
			ticker := time.NewTicker(6 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					syncCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					if err := SyncOpenCodeVersion(syncCtx, cfg); err != nil {
						log.Warnf("opencode updater: periodic version refresh failed: %v", err)
					}
					cancel()
				}
			}
		}()
	})
}
