package policyloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyServiceLoaderCachesWithETag(t *testing.T) {
	t.Parallel()

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/policies/example.rego" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == "v1" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Etag", "v1")
		_, _ = w.Write([]byte("package example\nallow := true"))
	}))
	t.Cleanup(server.Close)

	cfg := PolicyServiceConfig{
		ServiceURL:     server.URL,
		ResourcePrefix: "policies",
		PollMin:        time.Hour,
		PollMax:        time.Hour,
		HTTPTimeout:    time.Second,
		Persist:        false,
	}

	loader, err := NewPolicyServiceLoader(cfg)
	if err != nil {
		t.Fatalf("failed to create loader: %v", err)
	}

	ctx := context.Background()
	if _, err := loader.LoadPolicy(ctx, "example"); err != nil {
		t.Fatalf("expected policy, got %v", err)
	}

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected one HTTP call, got %d", got)
	}

	entry := loader.getEntry("example")
	entry.mu.Lock()
	entry.nextSync = time.Now().Add(-time.Minute)
	entry.mu.Unlock()

	if _, err := loader.LoadPolicy(ctx, "example"); err != nil {
		t.Fatalf("expected cached policy, got %v", err)
	}

	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected cache revalidation call, got %d", got)
	}
}

func TestPolicyServiceLoaderUsesPersistedPolicy(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("package example\nallow := true"))
	}))
	t.Cleanup(server.Close)

	cfg := PolicyServiceConfig{
		ServiceURL:     server.URL,
		ResourcePrefix: "policies",
		PollMin:        time.Hour,
		PollMax:        time.Hour,
		HTTPTimeout:    time.Second,
		Persist:        true,
		CacheDir:       cacheDir,
	}

	loader, err := NewPolicyServiceLoader(cfg)
	if err != nil {
		t.Fatalf("failed to create loader: %v", err)
	}

	if _, err := loader.LoadPolicy(context.Background(), "example"); err != nil {
		t.Fatalf("expected policy, got %v", err)
	}

	persisted := filepath.Join(cacheDir, "example.rego")
	if _, err := os.Stat(persisted); err != nil {
		t.Fatalf("expected persisted file, got %v", err)
	}

	// New loader pointing to nowhere should still serve persisted file.
	cfg2 := cfg
	cfg2.ServiceURL = "http://127.0.0.1:0"
	loader2, err := NewPolicyServiceLoader(cfg2)
	if err != nil {
		t.Fatalf("failed to create loader2: %v", err)
	}

	if _, err := loader2.LoadPolicy(context.Background(), "example"); err != nil {
		t.Fatalf("expected persisted policy, got %v", err)
	}
}
