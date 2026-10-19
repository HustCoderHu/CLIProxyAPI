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
	// OpenCodeBaselineVersion 是内置固化的基线版本号
	OpenCodeBaselineVersion = "2.0.32"
	// OpenCodeBaselineUserAgent 是内置固化的基线 User-Agent
	OpenCodeBaselineUserAgent = "opencode/latest/2.0.32/cli"
)

// openCodeRuntimeState 保存 OpenCode 运行时的版本与 User-Agent 状态
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

// CurrentOpenCodeUserAgent 获取当前有效的 User-Agent
func CurrentOpenCodeUserAgent() string {
	openCodeState.mu.RLock()
	defer openCodeState.mu.RUnlock()
	return openCodeState.userAgent
}

// CurrentOpenCodeVersion 获取当前有效的 OpenCode 版本号
func CurrentOpenCodeVersion() string {
	openCodeState.mu.RLock()
	defer openCodeState.mu.RUnlock()
	return openCodeState.version
}

// SetOpenCodeVersion 线程安全地更新当前版本与 User-Agent
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

// ResetOpenCodeStateForTest 重置运行时状态与 sync.Once，供单元测试使用
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

// CompareSemver 比较两个语义化版本号：v1 > v2 返回 1，v1 < v2 返回 -1，相等返回 0
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

// buildUpdaterHTTPClient 根据代理配置和系统环境变量构建 HTTP 客户端
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

// fetchVersionFromOfficial 从 OpenCode 官方更新 CDN 端点拉取最新版本
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

// fetchVersionFromGitHub 从 GitHub git matching-refs API 拉取最新 v2.x 标签
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

// fetchVersionFromNPM 从 NPM 官方注册表镜像拉取最新版本
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

// FetchLatestOpenCodeVersion 尝试通过三级高可用链条发现最新版本
func FetchLatestOpenCodeVersion(ctx context.Context, client *http.Client, githubToken string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	// 第一优先级：官方更新端点
	if v, err := fetchVersionFromOfficial(ctx, client); err == nil && v != "" {
		return v, nil
	} else if err != nil {
		log.Debugf("opencode updater: official endpoint failed: %v", err)
	}

	// 第二优先级：GitHub matching-refs API
	if v, err := fetchVersionFromGitHub(ctx, client, githubToken); err == nil && v != "" {
		return v, nil
	} else if err != nil {
		log.Debugf("opencode updater: github tags endpoint failed: %v", err)
	}

	// 第三优先级：NPM 官方注册表镜像
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

// SyncOpenCodeVersion 拉取最新版本并在有更新时原子写入运行时状态
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

// StartOpenCodeUpdater 启动 OpenCode 版本更新器（异步初次拉取 + 6 小时定时轮询）
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
