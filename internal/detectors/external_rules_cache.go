package detectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
)

const DefaultExternalRulesCacheTTL = 24 * time.Hour

// ExternalRuleLoadResult returns both the compiled detectors and the loading
// metrics collected while fetching, parsing, and compiling external rule files.
type ExternalRuleLoadResult struct {
	Detectors []anonymizer.Detector
	Metrics   ExternalLoadMetrics
}

// ExternalLoadMetrics groups operational timings and counters for external rule
// loading. These values are used for startup logs and to understand whether the
// proxy used disk cache, downloaded remote files, or fell back to stale cache.
type ExternalLoadMetrics struct {
	CacheHits      int
	CacheMisses    int
	CacheFallbacks int
	Downloads      int
	Files          int
	Bytes          int
	Rules          int
	Recognizers    int
	Patterns       int
	Detectors      int
	CacheRead      time.Duration
	CacheWrite     time.Duration
	Download       time.Duration
	Parse          time.Duration
	Compile        time.Duration
	Total          time.Duration
}

// cachedBodyMetrics is the per-file fetch accounting returned by the cache
// helper before it is merged into the broader ExternalLoadMetrics aggregate.
type cachedBodyMetrics struct {
	cacheHit      bool
	cacheFallback bool
	cacheRead     time.Duration
	cacheWrite    time.Duration
	download      time.Duration
	bytes         int
}

// defaultExternalRulesCacheDir resolves the user-scoped cache directory used to
// persist downloaded external rule files between proxy starts.
func defaultExternalRulesCacheDir() string {
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		return ""
	}
	return filepath.Join(cacheRoot, "klovis", "external-rules")
}

// loadCachedRemoteBody loads a remote rule file with a disk-cache layer.
//
// The cache stores the raw response body only. Parsing and regexp compilation
// still happen on every load so the callers can keep their own rule-specific
// validation logic. When the cached file is fresh, no network request is made.
// When it is stale or missing, the function tries to download a new copy. If
// that download fails but an older cached file exists, the stale file is returned
// as a resilience fallback.
func loadCachedRemoteBody(ctx context.Context, client *http.Client, cacheDir, namespace, sourceURL string, ttl time.Duration) ([]byte, cachedBodyMetrics, error) {
	if client == nil {
		return nil, cachedBodyMetrics{}, fmt.Errorf("http client is nil")
	}
	if ttl <= 0 {
		ttl = DefaultExternalRulesCacheTTL
	}

	cachePath := externalCachePath(cacheDir, namespace, sourceURL)
	if cachePath != "" {
		readStart := time.Now()
		payload, fresh, ok := readCachedBody(cachePath, ttl)
		cacheRead := time.Since(readStart)
		if ok && fresh {
			// Fresh cache hit: avoid the remote request entirely.
			return payload, cachedBodyMetrics{
				cacheHit:  true,
				cacheRead: cacheRead,
				bytes:     len(payload),
			}, nil
		}

		downloaded, stats, err := downloadRemoteBody(ctx, client, sourceURL)
		if err == nil {
			writeStart := time.Now()
			// Cache write failures should not prevent using the freshly
			// downloaded rules; they only remove the benefit for later starts.
			if writeErr := writeCachedBody(cachePath, downloaded); writeErr == nil {
				stats.cacheWrite = time.Since(writeStart)
			}
			return downloaded, stats, nil
		}

		if ok {
			// Stale cache fallback: prefer an older rules file over failing
			// startup when the remote source is temporarily unavailable.
			return payload, cachedBodyMetrics{
				cacheHit:      true,
				cacheFallback: true,
				cacheRead:     cacheRead,
				bytes:         len(payload),
			}, nil
		}
		return nil, cachedBodyMetrics{}, err
	}

	return downloadRemoteBody(ctx, client, sourceURL)
}

// externalCachePath creates a stable cache filename for a source URL inside the
// given namespace. The URL is hashed to avoid unsafe path characters and to keep
// source-specific cache entries isolated.
func externalCachePath(cacheDir, namespace, sourceURL string) string {
	if cacheDir == "" || namespace == "" || sourceURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceURL))
	return filepath.Join(cacheDir, namespace, hex.EncodeToString(sum[:])+".cache")
}

// readCachedBody returns the cached payload, whether it is still fresh for the
// requested TTL, and whether a readable cache entry existed at all.
func readCachedBody(cachePath string, ttl time.Duration) ([]byte, bool, bool) {
	info, err := os.Stat(cachePath)
	if err != nil || info.IsDir() {
		return nil, false, false
	}

	payload, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, false, false
	}

	return payload, time.Since(info.ModTime()) <= ttl, true
}

// writeCachedBody persists a downloaded rule file for future starts.
func writeCachedBody(cachePath string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, payload, 0o644)
}

// downloadRemoteBody fetches a rule file from its source URL and measures the
// network transfer. HTTP non-2xx responses are treated as load failures.
func downloadRemoteBody(ctx context.Context, client *http.Client, sourceURL string) ([]byte, cachedBodyMetrics, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, cachedBodyMetrics{}, err
	}

	downloadStart := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return nil, cachedBodyMetrics{}, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, cachedBodyMetrics{}, fmt.Errorf("HTTP %d", response.StatusCode)
	}

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, cachedBodyMetrics{}, err
	}

	return payload, cachedBodyMetrics{
		download:  time.Since(downloadStart),
		bytes:     len(payload),
		cacheHit:  false,
		cacheRead: 0,
	}, nil
}

// mergeCachedBodyMetrics folds one file fetch into the aggregate metrics
// returned by the external rule loaders.
func mergeCachedBodyMetrics(target *ExternalLoadMetrics, stats cachedBodyMetrics) {
	if stats.cacheHit {
		target.CacheHits++
	} else {
		target.CacheMisses++
		target.Downloads++
	}
	if stats.cacheFallback {
		target.CacheFallbacks++
	}
	target.Bytes += stats.bytes
	target.CacheRead += stats.cacheRead
	target.CacheWrite += stats.cacheWrite
	target.Download += stats.download
	target.Files++
}
