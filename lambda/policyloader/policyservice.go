package policyloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// PolicyServiceConfig describes how to download individual policies via HTTP.
type PolicyServiceConfig struct {
	ServiceURL     string
	ResourcePrefix string
	BearerToken    string
	Persist        bool
	CacheDir       string
	PollMin        time.Duration
	PollMax        time.Duration
	HTTPTimeout    time.Duration
}

// PolicyServiceLoader fetches .rego files from an HTTP policy service API.
type PolicyServiceLoader struct {
	cfg            PolicyServiceConfig
	client         *http.Client
	baseURL        string
	resourcePrefix string
	cacheDir       string

	mu    sync.RWMutex
	cache map[string]*policyCacheEntry
}

type policyCacheEntry struct {
	mu       sync.Mutex
	module   string
	etag     string
	nextSync time.Time
	loaded   bool
}

// NewPolicyServiceLoader creates a loader backed by an HTTP policy service.
func NewPolicyServiceLoader(cfg PolicyServiceConfig) (*PolicyServiceLoader, error) {
	if cfg.ServiceURL == "" {
		return nil, errors.New("policy service URL is required")
	}

	cfg.ServiceURL = strings.TrimRight(cfg.ServiceURL, "/")

	if cfg.PollMin <= 0 {
		cfg.PollMin = 10 * time.Second
	}
	if cfg.PollMax < cfg.PollMin {
		cfg.PollMax = cfg.PollMin
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 15 * time.Second
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), ".opa", "policies")
	}

	loader := &PolicyServiceLoader{
		cfg:            cfg,
		client:         &http.Client{Timeout: cfg.HTTPTimeout},
		baseURL:        cfg.ServiceURL,
		resourcePrefix: strings.Trim(cfg.ResourcePrefix, "/"),
		cacheDir:       cacheDir,
		cache:          make(map[string]*policyCacheEntry),
	}

	return loader, nil
}

// LoadPolicy retrieves the policy module text for the given package name.
func (l *PolicyServiceLoader) LoadPolicy(ctx context.Context, policyName string) (string, error) {
	entry := l.getEntry(policyName)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.loaded && time.Now().Before(entry.nextSync) {
		return entry.module, nil
	}

	if err := l.refreshPolicy(ctx, policyName, entry); err != nil {
		if entry.loaded {
			log.WithError(err).Warnf("serving cached copy of %s after refresh failure", policyName)
			return entry.module, nil
		}

		if l.cfg.Persist {
			if cached, readErr := l.readPersistedPolicy(policyName); readErr == nil {
				entry.module = cached
				entry.loaded = true
				entry.etag = ""
				entry.nextSync = l.nextInterval()
				return entry.module, nil
			}
		}

		return "", err
	}

	return entry.module, nil
}

func (l *PolicyServiceLoader) getEntry(policyName string) *policyCacheEntry {
	l.mu.RLock()
	entry := l.cache[policyName]
	l.mu.RUnlock()
	if entry != nil {
		return entry
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	entry = l.cache[policyName]
	if entry == nil {
		entry = &policyCacheEntry{}
		l.cache[policyName] = entry
	}
	return entry
}

func (l *PolicyServiceLoader) refreshPolicy(ctx context.Context, policyName string, entry *policyCacheEntry) error {
	filename, err := KeyToFilename(policyName)
	if err != nil {
		return err
	}

	path := filename
	if l.resourcePrefix != "" {
		path = l.resourcePrefix + "/" + filename
	}
	url := fmt.Sprintf("%s/%s", l.baseURL, strings.TrimLeft(path, "/"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	if entry.etag != "" {
		req.Header.Set("If-None-Match", entry.etag)
	}
	if l.cfg.BearerToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", l.cfg.BearerToken))
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download policy %s: %w", policyName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if !entry.loaded {
			return errors.New("policy not downloaded yet; received 304 Not Modified")
		}
		entry.nextSync = l.nextInterval()
		return nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("policy %s not found (404)", policyName)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("policy download failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}

	contentBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read policy body: %w", err)
	}

	entry.module = string(contentBytes)
	entry.etag = resp.Header.Get("Etag")
	entry.loaded = true
	entry.nextSync = l.nextInterval()

	if l.cfg.Persist {
		if err := l.persistPolicy(filename, entry.module); err != nil {
			log.WithError(err).Warnf("failed to persist policy %s", policyName)
		}
	}

	return nil
}

func (l *PolicyServiceLoader) persistPolicy(filename, contents string) error {
	fullPath := filepath.Join(l.cacheDir, filename)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return err
	}
	tmp := fullPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(contents), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fullPath)
}

func (l *PolicyServiceLoader) readPersistedPolicy(policyName string) (string, error) {
	filename, err := KeyToFilename(policyName)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(l.cacheDir, filename)
	bytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func (l *PolicyServiceLoader) nextInterval() time.Time {
	interval := l.cfg.PollMin
	if l.cfg.PollMax > l.cfg.PollMin {
		delta := l.cfg.PollMax - l.cfg.PollMin
		interval += time.Duration(rand.Int63n(int64(delta)))
	}
	return time.Now().Add(interval)
}

func newPolicyServiceConfigFromEnv() (*PolicyServiceConfig, error) {
	svc := strings.TrimSpace(os.Getenv("POLICY_SERVICE_URL"))
	if svc == "" {
		return nil, nil
	}

	cfg := &PolicyServiceConfig{
		ServiceURL:     svc,
		ResourcePrefix: strings.TrimSpace(os.Getenv("POLICY_RESOURCE_PREFIX")),
		BearerToken:    strings.TrimSpace(os.Getenv("POLICY_BEARER_TOKEN")),
		CacheDir:       strings.TrimSpace(os.Getenv("POLICY_CACHE_DIR")),
		Persist:        true,
	}

	if raw := strings.TrimSpace(os.Getenv("POLICY_PERSIST")); raw != "" {
		val, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid POLICY_PERSIST: %w", err)
		}
		cfg.Persist = val
	}

	var err error
	if cfg.PollMin, err = durationFromEnv("POLICY_POLL_MIN_SECONDS", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.PollMax, err = durationFromEnv("POLICY_POLL_MAX_SECONDS", 30*time.Second); err != nil {
		return nil, err
	}
	if cfg.HTTPTimeout, err = durationFromEnv("POLICY_HTTP_TIMEOUT_SECONDS", 15*time.Second); err != nil {
		return nil, err
	}

	return cfg, nil
}

func durationFromEnv(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return time.Duration(val) * time.Second, nil
}
