package stats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultPath is the local JSONL file used by the dashboard stats backend.
	DefaultPath = "anonymization_stats.jsonl"
	// DefaultMaxBytes is the maximum stats file size before it is truncated.
	DefaultMaxBytes = 10 * 1024 * 1024
	// DefaultBucketDuration controls the timeline aggregation granularity.
	DefaultBucketDuration = time.Hour

	// EventRequestProcessed records one intercepted request after local processing.
	EventRequestProcessed = "request_processed"
	// EventLLMError records a failure from the optional local LLM extractor.
	EventLLMError = "llm_error"
	// EventProxyError records a failure while forwarding to the upstream provider.
	EventProxyError = "proxy_error"
	// EventRequestBodyError records a failure while reading the incoming request body.
	EventRequestBodyError = "request_body_error"
)

// Config controls the JSONL stats file path, rotation size, and optional clock.
type Config struct {
	Path     string
	MaxBytes int64
	Now      func() time.Time
}

// Event is the raw append-only record written as one JSON object per JSONL line.
type Event struct {
	Timestamp         time.Time      `json:"timestamp"`
	Event             string         `json:"event"`
	Anonymized        bool           `json:"anonymized"`
	TotalReplacements int            `json:"total_replacements"`
	Counts            map[string]int `json:"counts"`
}

// Recorder owns the stats file and serializes reads, writes, reset, and rotation.
type Recorder struct {
	path     string
	maxBytes int64
	now      func() time.Time
	mu       sync.Mutex
}

// Summary is the dashboard-ready aggregate returned by GET /api/stats.
type Summary struct {
	TotalRequests      int              `json:"total_requests"`
	AnonymizedRequests int              `json:"anonymized_requests"`
	LLMErrors          int              `json:"llm_errors"`
	ProxyErrors        int              `json:"proxy_errors"`
	RequestBodyErrors  int              `json:"request_body_errors"`
	TotalReplacements  int              `json:"total_replacements"`
	CountsByType       []TypeCount      `json:"counts_by_type"`
	Timeline           []TimelineBucket `json:"timeline"`
}

// TypeCount is one sorted replacement total for a single entity type.
type TypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// TimelineBucket is the dashboard aggregate for one chronological time window.
type TimelineBucket struct {
	Bucket             time.Time      `json:"bucket"`
	Requests           int            `json:"requests"`
	AnonymizedRequests int            `json:"anonymized_requests"`
	LLMErrors          int            `json:"llm_errors"`
	ProxyErrors        int            `json:"proxy_errors"`
	RequestBodyErrors  int            `json:"request_body_errors"`
	TotalReplacements  int            `json:"total_replacements"`
	Counts             map[string]int `json:"counts"`
}

func NewRecorder(config Config) (*Recorder, error) {
	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = DefaultPath
	}
	maxBytes := config.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	recorder := &Recorder{
		path:     path,
		maxBytes: maxBytes,
		now:      now,
	}
	if err := recorder.ensureFile(); err != nil {
		return nil, err
	}
	return recorder, nil
}

func (r *Recorder) Record(event Event) error {
	if r == nil {
		return nil
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = r.now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	event.Counts = cleanCounts(event.Counts)
	event.TotalReplacements = totalCounts(event.Counts)
	if event.Event != EventRequestProcessed {
		event.TotalReplacements = 0
		event.Counts = map[string]int{}
	}
	if event.Event == EventRequestProcessed {
		event.Anonymized = event.TotalReplacements > 0
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal stats event: %w", err)
	}
	payload = append(payload, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileLocked(); err != nil {
		return err
	}
	if err := r.rotateIfNeededLocked(int64(len(payload))); err != nil {
		return err
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open stats file: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write stats event: %w", err)
	}
	return nil
}

func (r *Recorder) Summary() (Summary, error) {
	return r.SummaryWithBucket(DefaultBucketDuration)
}

func (r *Recorder) SummaryWithBucket(bucketDuration time.Duration) (Summary, error) {
	if r == nil {
		return Summary{}, nil
	}
	if bucketDuration <= 0 {
		bucketDuration = DefaultBucketDuration
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	events, err := r.readEventsLocked()
	if err != nil {
		return Summary{}, err
	}
	return aggregate(events, bucketDuration), nil
}

func (r *Recorder) Events() ([]Event, error) {
	if r == nil {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readEventsLocked()
}

func (r *Recorder) Reset() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureParentDirLocked(); err != nil {
		return err
	}
	if err := os.WriteFile(r.path, nil, 0o600); err != nil {
		return fmt.Errorf("reset stats file: %w", err)
	}
	return nil
}

func (r *Recorder) ensureFile() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureFileLocked()
}

func (r *Recorder) ensureFileLocked() error {
	if err := r.ensureParentDirLocked(); err != nil {
		return err
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open stats file: %w", err)
	}
	return file.Close()
}

func (r *Recorder) ensureParentDirLocked() error {
	dir := filepath.Dir(r.path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create stats directory: %w", err)
	}
	return nil
}

func (r *Recorder) rotateIfNeededLocked(nextBytes int64) error {
	if r.maxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat stats file: %w", err)
	}
	if info.Size()+nextBytes <= r.maxBytes {
		return nil
	}
	if err := os.WriteFile(r.path, nil, 0o600); err != nil {
		return fmt.Errorf("rotate stats file: %w", err)
	}
	return nil
}

func (r *Recorder) readEventsLocked() ([]Event, error) {
	file, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open stats file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var events []Event
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("parse stats event line %d: %w", lineNumber, err)
		}
		event.Counts = cleanCounts(event.Counts)
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stats file: %w", err)
	}
	return events, nil
}

func aggregate(events []Event, bucketDuration time.Duration) Summary {
	summary := Summary{}
	countsByType := make(map[string]int)
	buckets := make(map[time.Time]*TimelineBucket)

	for _, event := range events {
		bucket := event.Timestamp.UTC().Truncate(bucketDuration)
		timelineBucket := buckets[bucket]
		if timelineBucket == nil {
			timelineBucket = &TimelineBucket{
				Bucket: bucket,
				Counts: make(map[string]int),
			}
			buckets[bucket] = timelineBucket
		}

		switch event.Event {
		case EventRequestProcessed:
			summary.TotalRequests++
			timelineBucket.Requests++
			if event.Anonymized {
				summary.AnonymizedRequests++
				timelineBucket.AnonymizedRequests++
			}
			summary.TotalReplacements += event.TotalReplacements
			timelineBucket.TotalReplacements += event.TotalReplacements
			addCounts(countsByType, event.Counts)
			addCounts(timelineBucket.Counts, event.Counts)
		case EventLLMError:
			summary.LLMErrors++
			timelineBucket.LLMErrors++
		case EventProxyError:
			summary.ProxyErrors++
			timelineBucket.ProxyErrors++
		case EventRequestBodyError:
			summary.TotalRequests++
			summary.RequestBodyErrors++
			timelineBucket.Requests++
			timelineBucket.RequestBodyErrors++
		}
	}

	summary.CountsByType = sortedTypeCounts(countsByType)
	summary.Timeline = sortedTimelineBuckets(buckets)
	return summary
}

func addCounts(target map[string]int, source map[string]int) {
	for entityType, count := range source {
		if count <= 0 {
			continue
		}
		target[entityType] += count
	}
}

func cleanCounts(counts map[string]int) map[string]int {
	cleaned := make(map[string]int)
	for entityType, count := range counts {
		entityType = strings.TrimSpace(entityType)
		if entityType == "" || count <= 0 {
			continue
		}
		cleaned[entityType] += count
	}
	return cleaned
}

func totalCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		if count > 0 {
			total += count
		}
	}
	return total
}

func sortedTypeCounts(counts map[string]int) []TypeCount {
	result := make([]TypeCount, 0, len(counts))
	for entityType, count := range counts {
		result = append(result, TypeCount{Type: entityType, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Type < result[j].Type
	})
	return result
}

func sortedTimelineBuckets(buckets map[time.Time]*TimelineBucket) []TimelineBucket {
	result := make([]TimelineBucket, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, *bucket)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Bucket.Before(result[j].Bucket)
	})
	return result
}
