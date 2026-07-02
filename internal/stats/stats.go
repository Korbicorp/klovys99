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
	// DefaultMaxBytes is the maximum stats file size before it is rotated.
	DefaultMaxBytes = 10 * 1024 * 1024
	// DefaultMaxArchiveFiles is the number of rotated stats files kept beside the active file.
	DefaultMaxArchiveFiles = 3
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
	Path            string
	MaxBytes        int64
	MaxArchiveFiles int
	Now             func() time.Time
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
	path            string
	maxBytes        int64
	maxArchiveFiles int
	now             func() time.Time
	mu              sync.Mutex
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

// NewRecorder creates a stats recorder with defaults filled in and ensures the active stats file exists.
func NewRecorder(config Config) (*Recorder, error) {
	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = DefaultPath
	}
	maxBytes := config.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	maxArchiveFiles := config.MaxArchiveFiles
	if maxArchiveFiles <= 0 {
		maxArchiveFiles = DefaultMaxArchiveFiles
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	recorder := &Recorder{
		path:            path,
		maxBytes:        maxBytes,
		maxArchiveFiles: maxArchiveFiles,
		now:             now,
	}
	if err := recorder.ensureFile(); err != nil {
		return nil, err
	}
	return recorder, nil
}

// Record normalizes one stats event, rotates the stats files if needed, and appends the event as JSONL.
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

// Summary returns dashboard-ready aggregates using the default timeline bucket duration.
func (r *Recorder) Summary() (Summary, error) {
	return r.SummaryWithBucket(DefaultBucketDuration)
}

// SummaryWithBucket reads all persisted events and aggregates them into the requested time windows.
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

// Events returns the raw persisted stats events from rotated files and the active file.
func (r *Recorder) Events() ([]Event, error) {
	if r == nil {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readEventsLocked()
}

// Reset clears the active stats file and removes all rotated stats files.
func (r *Recorder) Reset() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureParentDirLocked(); err != nil {
		return err
	}
	for _, path := range r.archivePathsLocked() {
		if err := removeIfExists(path); err != nil {
			return fmt.Errorf("reset rotated stats file: %w", err)
		}
	}
	if err := os.WriteFile(r.path, nil, 0o600); err != nil {
		return fmt.Errorf("reset stats file: %w", err)
	}
	return nil
}

// ensureFile serializes creation of the active stats file.
func (r *Recorder) ensureFile() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureFileLocked()
}

// ensureFileLocked creates the parent directory and active file if they do not already exist.
// The caller must hold r.mu.
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

// ensureParentDirLocked creates the directory that will contain the stats files.
// The caller must hold r.mu.
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

// rotateIfNeededLocked rotates files before the next append would make the active file exceed its size limit.
// The caller must hold r.mu.
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
	if err := r.rotateFilesLocked(); err != nil {
		return fmt.Errorf("rotate stats files: %w", err)
	}
	return nil
}

// readEventsLocked reads rotated files and the active file in chronological archive order.
// The caller must hold r.mu.
func (r *Recorder) readEventsLocked() ([]Event, error) {
	var events []Event
	for _, path := range r.readPathsLocked() {
		fileEvents, err := readEventsFile(path)
		if err != nil {
			return nil, err
		}
		events = append(events, fileEvents...)
	}
	return events, nil
}

// rotateFilesLocked shifts archives forward and moves the active file into the first archive slot.
// The caller must hold r.mu.
func (r *Recorder) rotateFilesLocked() error {
	if r.maxArchiveFiles <= 0 {
		if err := os.WriteFile(r.path, nil, 0o600); err != nil {
			return fmt.Errorf("truncate active stats file: %w", err)
		}
		return nil
	}

	if err := removeIfExists(r.archivePathLocked(r.maxArchiveFiles)); err != nil {
		return err
	}
	for index := r.maxArchiveFiles - 1; index >= 1; index-- {
		source := r.archivePathLocked(index)
		target := r.archivePathLocked(index + 1)
		if err := renameIfExists(source, target); err != nil {
			return err
		}
	}
	if err := renameIfExists(r.path, r.archivePathLocked(1)); err != nil {
		return err
	}
	return nil
}

// readPathsLocked returns the file read order: oldest archive first, then the active file.
// The caller must hold r.mu.
func (r *Recorder) readPathsLocked() []string {
	paths := make([]string, 0, r.maxArchiveFiles+1)
	for index := r.maxArchiveFiles; index >= 1; index-- {
		paths = append(paths, r.archivePathLocked(index))
	}
	paths = append(paths, r.path)
	return paths
}

// archivePathsLocked returns all archive paths in reset order, starting from the newest archive.
// The caller must hold r.mu.
func (r *Recorder) archivePathsLocked() []string {
	paths := make([]string, 0, r.maxArchiveFiles)
	for index := 1; index <= r.maxArchiveFiles; index++ {
		paths = append(paths, r.archivePathLocked(index))
	}
	return paths
}

// archivePathLocked builds the numbered archive path while preserving the active file extension.
// The caller must hold r.mu.
func (r *Recorder) archivePathLocked(index int) string {
	extension := filepath.Ext(r.path)
	base := strings.TrimSuffix(r.path, extension)
	return fmt.Sprintf("%s.%d%s", base, index, extension)
}

// readEventsFile parses one JSONL stats file into events and ignores missing files.
func readEventsFile(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open stats file %q: %w", path, err)
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
			return nil, fmt.Errorf("parse stats event %s line %d: %w", path, lineNumber, err)
		}
		event.Counts = cleanCounts(event.Counts)
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stats file %q: %w", path, err)
	}
	return events, nil
}

// removeIfExists removes a file and treats an already-missing file as success.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// renameIfExists renames a file only when the source exists.
func renameIfExists(source, target string) error {
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.Rename(source, target); err != nil {
		return err
	}
	return nil
}

// aggregate converts raw events into total counters, sorted type counts, and timeline buckets.
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

// addCounts merges positive entity counts into an aggregate map.
func addCounts(target map[string]int, source map[string]int) {
	for entityType, count := range source {
		if count <= 0 {
			continue
		}
		target[entityType] += count
	}
}

// cleanCounts trims entity names, removes invalid counts, and merges duplicate entity types.
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

// totalCounts sums the positive replacement counts in an entity-count map.
func totalCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		if count > 0 {
			total += count
		}
	}
	return total
}

// sortedTypeCounts returns entity counts sorted by descending count and then by type name.
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

// sortedTimelineBuckets returns timeline buckets ordered from oldest to newest.
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
