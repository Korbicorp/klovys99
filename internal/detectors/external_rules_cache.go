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

type ExternalRuleLoadResult struct {
	Detectors []anonymizer.Detector
	Metrics   ExternalLoadMetrics
}

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

type cachedBodyMetrics struct {
	cacheHit      bool
	cacheFallback bool
	cacheRead     time.Duration
	cacheWrite    time.Duration
	download      time.Duration
	bytes         int
}

func defaultExternalRulesCacheDir() string {
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		return ""
	}
	return filepath.Join(cacheRoot, "klovis", "external-rules")
}

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
			return payload, cachedBodyMetrics{
				cacheHit:  true,
				cacheRead: cacheRead,
				bytes:     len(payload),
			}, nil
		}

		downloaded, stats, err := downloadRemoteBody(ctx, client, sourceURL)
		if err == nil {
			writeStart := time.Now()
			if writeErr := writeCachedBody(cachePath, downloaded); writeErr == nil {
				stats.cacheWrite = time.Since(writeStart)
			}
			return downloaded, stats, nil
		}

		if ok {
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

func externalCachePath(cacheDir, namespace, sourceURL string) string {
	if cacheDir == "" || namespace == "" || sourceURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceURL))
	return filepath.Join(cacheDir, namespace, hex.EncodeToString(sum[:])+".cache")
}

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

func writeCachedBody(cachePath string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, payload, 0o644)
}

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
