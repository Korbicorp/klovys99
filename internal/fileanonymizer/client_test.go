package fileanonymizer

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/rs/zerolog"
)

type emailDetector struct{}

func (emailDetector) FindAll(text string) []anonymizer.Match {
	index := strings.Index(text, "alice@example.com")
	if index < 0 {
		return nil
	}
	return []anonymizer.Match{{Start: index, End: index + len("alice@example.com"), Type: anonymizer.EntityEmail}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnonymizeUsesExtractedTextAndRendersReplacement(t *testing.T) {
	var rendered string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/extract":
			return response(`{"segments":[{"ID":"p:0","Text":"alice@example.com"}]}`, "application/json"), nil
		case "/v1/render":
			request.ParseMultipartForm(1 << 20)
			rendered = request.FormValue("replacements")
			return response("sanitized", "application/octet-stream"), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})}
	client, err := New(Config{Mode: ModeFull, URL: "http://127.0.0.1:8092", FailurePolicy: PolicyRemove, HTTPClient: httpClient, Anonymizer: anonymizer.NewService([]anonymizer.Detector{emailDetector{}})})
	if err != nil {
		t.Fatal(err)
	}
	fullOutput, err := client.AnonymizeWithText(context.Background(), "openai", "application/pdf", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	if string(fullOutput.Data) != "sanitized" || fullOutput.Text != "[EMAIL_1]" || !strings.Contains(rendered, `"id":"p:0"`) || !strings.Contains(rendered, `"text":"[EMAIL_1]"`) || fullOutput.Result.Stats[anonymizer.EntityEmail].Count != 1 {
		t.Fatalf("output=%q text=%q replacements=%q result=%+v", fullOutput.Data, fullOutput.Text, rendered, fullOutput.Result)
	}
}

func TestOffModeDoesNotCallSidecar(t *testing.T) {
	client, err := New(Config{Mode: ModeOff, URL: "http://127.0.0.1:8092", FailurePolicy: PolicyRemove})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("original")
	output, _, err := client.Anonymize(context.Background(), "openai", "application/pdf", input)
	if err != nil || !bytes.Equal(output, input) {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestAnonymizeLogsPresidioErrorDetailAndStage(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := response(`{"error":"cannot redact PDF page"}`, "application/json")
		response.StatusCode = http.StatusUnprocessableEntity
		return response, nil
	})}
	client, err := New(Config{Mode: ModeFull, URL: "http://127.0.0.1:8092", FailurePolicy: PolicyRemove, HTTPClient: httpClient, Anonymizer: anonymizer.NewService(nil), Logger: &logger})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Anonymize(context.Background(), "anthropic", "application/pdf", []byte("pdf"))
	if err == nil || !strings.Contains(err.Error(), "cannot redact PDF page") {
		t.Fatalf("error = %v", err)
	}
	if got := logs.String(); !strings.Contains(got, `"stage":"extract"`) || !strings.Contains(got, "cannot redact PDF page") {
		t.Fatalf("logs = %s", got)
	}
}

func response(body, contentType string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}
