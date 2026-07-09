package ner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAnalyzeBatchConvertsUnicodeOffsetsAndMapsLabels(t *testing.T) {
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"model":"test/model","model_revision":"abc123","results":[[{"start":1,"end":7,"label":"person name","score":0.9}]],"latency_ms":1}`
		return response(http.StatusOK, body), nil
	}))
	results, err := client.AnalyzeBatch(context.Background(), []string{"😀Élodie Martin"})
	if err != nil {
		t.Fatalf("AnalyzeBatch: %v", err)
	}
	match := results[0][0]
	if got, want := "😀Élodie Martin"[match.Start:match.End], "Élodie"; got != want {
		t.Fatalf("matched %q, want %q", got, want)
	}
	if match.Type != anonymizer.EntityName || match.Priority != DefaultPriority {
		t.Fatalf("match = %#v", match)
	}
}

func TestAnalyzeBatchUsesFullLabelProfile(t *testing.T) {
	var gotLabels []string
	client := testClientWithConfig(t, Config{
		Mode:           ModeFull,
		URL:            "http://127.0.0.1:8091",
		Model:          "test/model",
		ModelRevision:  "abc123",
		Timeout:        time.Second,
		MaxConcurrency: 1,
		MaxQueue:       2,
		MaxBatchChars:  100,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var payload analyzeRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			gotLabels = append([]string(nil), payload.Labels...)
			return response(http.StatusOK, `{"model":"test/model","model_revision":"abc123","results":[[]],"latency_ms":1}`), nil
		})},
	})
	if _, err := client.AnalyzeBatch(context.Background(), []string{"Paris"}); err != nil {
		t.Fatalf("AnalyzeBatch: %v", err)
	}
	want := []string{"person name", "organization", "location", "employer", "school or educational institution", "medical provider or healthcare institution", "street address"}
	if strings.Join(gotLabels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %v, want %v", gotLabels, want)
	}
	if status := client.Status(); status.Mode != ModeFull {
		t.Fatalf("status mode = %q, want %q", status.Mode, ModeFull)
	}
}

func TestAnalyzeBatchRejectsMalformedAndMismatchedResponses(t *testing.T) {
	tests := []string{
		`{"model":"other","model_revision":"abc123","results":[[]]}`,
		`{"model":"test/model","model_revision":"abc123","results":[]}`,
		`{"model":"test/model","model_revision":"abc123","results":[[{"start":0,"end":99,"label":"location","score":1}]]}`,
		`{"model":"test/model","model_revision":"abc123","results":[[{"start":0,"end":1,"label":"email","score":1}]]}`,
	}
	for _, body := range tests {
		client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		}))
		if _, err := client.AnalyzeBatch(context.Background(), []string{"Paris"}); err == nil {
			t.Fatalf("AnalyzeBatch accepted %s", body)
		}
	}
}

func TestAnalyzeBatchBoundsSizeAndQueue(t *testing.T) {
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	client.maxBatchChars = 3
	if _, err := client.AnalyzeBatch(context.Background(), []string{"Paris"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}

	client.maxBatchChars = 100
	client.timeout = time.Second
	client.slots = make(chan struct{}, 1)
	client.queue = make(chan struct{}, 1)
	client.slots <- struct{}{}
	var wait sync.WaitGroup
	wait.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer wait.Done()
		_, _ = client.AnalyzeBatch(ctx, []string{"Paris"})
	}()
	deadline := time.Now().Add(time.Second)
	for len(client.queue) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := client.AnalyzeBatch(context.Background(), []string{"Lyon"}); !errors.Is(err, ErrSaturated) {
		t.Fatalf("error = %v, want ErrSaturated", err)
	}
	cancel()
	wait.Wait()
	<-client.slots
}

func TestAnalyzeJSONStringsDeduplicatesOneBatch(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	matches, err := AnalyzeJSONStrings(
		context.Background(),
		analyzer,
		[]byte(`{"input":"Paris","metadata":"Paris","nested":["Lyon"]}`),
	)
	if err != nil {
		t.Fatalf("AnalyzeJSONStrings: %v", err)
	}
	if analyzer.calls != 1 || len(analyzer.texts) != 2 {
		t.Fatalf("calls=%d texts=%v", analyzer.calls, analyzer.texts)
	}
	if len(matches) != 2 {
		t.Fatalf("matches=%v", matches)
	}
}

type fakeAnalyzer struct {
	calls int
	texts []string
}

func (f *fakeAnalyzer) AnalyzeBatch(_ context.Context, texts []string) ([][]anonymizer.Match, error) {
	f.calls++
	f.texts = append([]string(nil), texts...)
	return make([][]anonymizer.Match, len(texts)), nil
}

func (f *fakeAnalyzer) Status() Status { return Status{Enabled: true, State: "ready"} }

func testClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := NewClient(Config{
		Mode:           ModeFull,
		URL:            "http://127.0.0.1:8091",
		Model:          "test/model",
		ModelRevision:  "abc123",
		Timeout:        time.Second,
		MaxConcurrency: 1,
		MaxQueue:       2,
		MaxBatchChars:  100,
		HTTPClient:     &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testClientWithConfig(t *testing.T, config Config) *Client {
	t.Helper()
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
