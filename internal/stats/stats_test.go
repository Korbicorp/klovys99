package stats

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderRecordsAndAggregatesStats(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 7, 2, 10, 15, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 20, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 25, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 10, 35, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 11, 5, 0, 0, time.UTC),
	}
	index := 0
	recorder, err := NewRecorder(Config{
		Path: filepath.Join(t.TempDir(), "stats.jsonl"),
		Now: func() time.Time {
			if index >= len(times) {
				t.Fatalf("unexpected timestamp request")
			}
			timestamp := times[index]
			index++
			return timestamp
		},
	})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}

	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 2, "SECRET": 1},
	})
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{},
	})
	mustRecord(t, recorder, Event{Event: EventLLMError})
	mustRecord(t, recorder, Event{Event: EventProxyError})
	mustRecord(t, recorder, Event{Event: EventRequestBodyError})
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 1, "IBAN": 3},
	})

	summary, err := recorder.Summary()
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}

	if summary.TotalRequests != 4 {
		t.Fatalf("TotalRequests = %d, want 4", summary.TotalRequests)
	}
	if summary.AnonymizedRequests != 2 {
		t.Fatalf("AnonymizedRequests = %d, want 2", summary.AnonymizedRequests)
	}
	if summary.LLMErrors != 1 || summary.ProxyErrors != 1 || summary.RequestBodyErrors != 1 {
		t.Fatalf("errors = llm:%d proxy:%d body:%d, want 1 each", summary.LLMErrors, summary.ProxyErrors, summary.RequestBodyErrors)
	}
	if summary.TotalReplacements != 7 {
		t.Fatalf("TotalReplacements = %d, want 7", summary.TotalReplacements)
	}
	assertTypeCounts(t, summary.CountsByType, []TypeCount{
		{Type: "EMAIL", Count: 3},
		{Type: "IBAN", Count: 3},
		{Type: "SECRET", Count: 1},
	})
	if len(summary.Timeline) != 2 {
		t.Fatalf("timeline length = %d, want 2", len(summary.Timeline))
	}
	if summary.Timeline[0].Bucket != times[0].Truncate(time.Hour) {
		t.Fatalf("first bucket = %s, want %s", summary.Timeline[0].Bucket, times[0].Truncate(time.Hour))
	}
	if summary.Timeline[0].Requests != 3 || summary.Timeline[0].AnonymizedRequests != 1 {
		t.Fatalf("first bucket requests = %d/%d, want 3/1", summary.Timeline[0].Requests, summary.Timeline[0].AnonymizedRequests)
	}
	if summary.Timeline[0].LLMErrors != 1 || summary.Timeline[0].ProxyErrors != 1 || summary.Timeline[0].RequestBodyErrors != 1 {
		t.Fatalf("first bucket errors = %#v, want one of each", summary.Timeline[0])
	}
	if summary.Timeline[1].Requests != 1 || summary.Timeline[1].TotalReplacements != 4 {
		t.Fatalf("second bucket = %#v, want one request and four replacements", summary.Timeline[1])
	}
}

func TestRecorderResetClearsStats(t *testing.T) {
	recorder, err := NewRecorder(Config{Path: filepath.Join(t.TempDir(), "stats.jsonl")})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 1},
	})

	if err := recorder.Reset(); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}
	summary, err := recorder.Summary()
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if summary.TotalRequests != 0 || len(summary.CountsByType) != 0 || len(summary.Timeline) != 0 {
		t.Fatalf("summary after reset = %#v, want empty", summary)
	}
}

// TestRecorderRotatesToArchiveWhenFileWouldExceedLimit verifies that a new event is not appended
// past the size limit: the existing active file is moved to .1 and the new event stays readable
// from the new active file. The summary must include both active and rotated events.
func TestRecorderRotatesToArchiveWhenFileWouldExceedLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.jsonl")
	recorder, err := NewRecorder(Config{Path: path})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 1},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat stats file: %v", err)
	}

	recorder, err = NewRecorder(Config{
		Path:     path,
		MaxBytes: info.Size() + 60,
	})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event: EventRequestProcessed,
		Counts: map[string]int{
			"VERY_LONG_SYNTHETIC_SECRET_TYPE": 2,
		},
	})

	summary, err := recorder.Summary()
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if summary.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want active and rotated requests", summary.TotalRequests)
	}
	assertTypeCounts(t, summary.CountsByType, []TypeCount{
		{Type: "VERY_LONG_SYNTHETIC_SECRET_TYPE", Count: 2},
		{Type: "EMAIL", Count: 1},
	})
	if _, err := os.Stat(rotatedPath(path, 1)); err != nil {
		t.Fatalf("stat rotated stats file: %v", err)
	}
}

// TestRecorderKeepsOnlyThreeRotatedFilesAndAggregatesAllReadableFiles forces enough rotations to
// exceed the archive limit. It verifies that only .1, .2, and .3 remain, while the summary still
// aggregates every event from the active file and the retained rotated files.
func TestRecorderKeepsOnlyThreeRotatedFilesAndAggregatesAllReadableFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.jsonl")
	recorder, err := NewRecorder(Config{Path: path})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"TYPE_1": 1},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat stats file: %v", err)
	}

	recorder, err = NewRecorder(Config{
		Path:     path,
		MaxBytes: info.Size() + 1,
	})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	for index := 2; index <= 5; index++ {
		mustRecord(t, recorder, Event{
			Event:  EventRequestProcessed,
			Counts: map[string]int{fmt.Sprintf("TYPE_%d", index): 1},
		})
	}

	summary, err := recorder.Summary()
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if summary.TotalRequests != 4 {
		t.Fatalf("TotalRequests = %d, want active plus three rotated files", summary.TotalRequests)
	}
	assertTypeCounts(t, summary.CountsByType, []TypeCount{
		{Type: "TYPE_2", Count: 1},
		{Type: "TYPE_3", Count: 1},
		{Type: "TYPE_4", Count: 1},
		{Type: "TYPE_5", Count: 1},
	})
	for index := 1; index <= 3; index++ {
		if _, err := os.Stat(rotatedPath(path, index)); err != nil {
			t.Fatalf("stat rotated stats file %d: %v", index, err)
		}
	}
	if _, err := os.Stat(rotatedPath(path, 4)); !os.IsNotExist(err) {
		t.Fatalf("rotated file 4 stat error = %v, want not exist", err)
	}
}

// TestRecorderResetClearsActiveAndRotatedStats verifies that reset removes every rotated file and
// leaves the active file empty, so the dashboard starts again from an empty summary.
func TestRecorderResetClearsActiveAndRotatedStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.jsonl")
	recorder, err := NewRecorder(Config{Path: path})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 1},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat stats file: %v", err)
	}
	recorder, err = NewRecorder(Config{
		Path:     path,
		MaxBytes: info.Size() + 1,
	})
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	mustRecord(t, recorder, Event{
		Event:  EventRequestProcessed,
		Counts: map[string]int{"SECRET": 1},
	})
	if _, err := os.Stat(rotatedPath(path, 1)); err != nil {
		t.Fatalf("stat rotated stats file: %v", err)
	}

	if err := recorder.Reset(); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}
	summary, err := recorder.Summary()
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if summary.TotalRequests != 0 || len(summary.CountsByType) != 0 || len(summary.Timeline) != 0 {
		t.Fatalf("summary after reset = %#v, want empty", summary)
	}
	for index := 1; index <= 3; index++ {
		if _, err := os.Stat(rotatedPath(path, index)); !os.IsNotExist(err) {
			t.Fatalf("rotated file %d stat error = %v, want not exist", index, err)
		}
	}
}

func mustRecord(t *testing.T, recorder *Recorder, event Event) {
	t.Helper()
	if err := recorder.Record(event); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}
}

func assertTypeCounts(t *testing.T, got, want []TypeCount) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("type counts length = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("type counts[%d] = %#v, want %#v; all counts: %#v", index, got[index], want[index], got)
		}
	}
}

func rotatedPath(path string, index int) string {
	extension := filepath.Ext(path)
	base := path[:len(path)-len(extension)]
	return fmt.Sprintf("%s.%d%s", base, index, extension)
}
