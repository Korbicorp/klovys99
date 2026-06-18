package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
	"github.com/Korbicorp/klovis/internal/llm"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestRunReadsStdinAndWritesStdout(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithDependencies(
		strings.NewReader("Email: alice@example.com\n"),
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(nil, nil),
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got, want := stdout.String(), "Email: [EMAIL_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunStatsWriteToStderrOnly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithDependencies(
		strings.NewReader("Tel: 06 12 34 56 78\n"),
		&stdout,
		&stderr,
		[]string{"--stats"},
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(nil, nil),
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got, want := stdout.String(), "Tel: [PHONE_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "PHONE count=1") {
		t.Fatalf("stderr = %q, want phone stats", stderr.String())
	}
	if !strings.Contains(stderr.String(), "time.anonymization=") ||
		!strings.Contains(stderr.String(), "time.total=") ||
		!strings.Contains(stderr.String(), "time.gitleaks_total=") ||
		!strings.Contains(stderr.String(), "time.presidio_total=") ||
		!strings.Contains(stderr.String(), "detectors.builtin=") ||
		!strings.Contains(stderr.String(), "stdin.bytes=") ||
		!strings.Contains(stderr.String(), "stdin.empty=") ||
		!strings.Contains(stderr.String(), "stdout.bytes=") {
		t.Fatalf("stderr = %q, want timing stats", stderr.String())
	}
}

func TestRunNoExtraDisablesExtraDetectors(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	input := "https://example.com aa:bb:cc:dd:ee:ff"

	err := runWithDependencies(
		strings.NewReader(input),
		&stdout,
		&stderr,
		[]string{"--no-extra"},
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(nil, nil),
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if got := stdout.String(); got != input {
		t.Fatalf("stdout = %q, want original %q", got, input)
	}
}

func TestRunReturnsCleanReadError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithDependencies(
		failingReader{},
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(nil, nil),
	)
	if err == nil {
		t.Fatal("run returned nil error")
	}
	if !strings.Contains(err.Error(), "read stdin") {
		t.Fatalf("error = %q, want read stdin context", err.Error())
	}
}

func TestRunDoesNotCallLLMWithoutFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	called := false

	err := runWithLLMFactory(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		nil,
		func(string, string, time.Duration) (llm.Extractor, error) {
			called = true
			return nil, fmt.Errorf("should not be called")
		},
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if called {
		t.Fatal("llm factory was called without --llm")
	}
	if got, want := stdout.String(), "Bonjour Jean Dupont\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunLoadsExternalDetectorsByDefault(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	external := []anonymizer.Detector{
		staticDetector{matches: []anonymizer.Match{{Start: 6, End: 12, Type: anonymizer.EntitySecret, Priority: 600}}},
	}
	err := runWithDependencies(
		strings.NewReader("token=abc123\n"),
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(external, nil),
		newTestExternalRulesLoader(nil, nil),
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "token=[SECRET_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunReturnsExternalDetectorLoadError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithDependencies(
		strings.NewReader("token=abc123\n"),
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, fmt.Errorf("boom")),
		newTestExternalRulesLoader(nil, nil),
	)
	if err == nil || !strings.Contains(err.Error(), "load gitleaks detectors") {
		t.Fatalf("error = %v, want gitleaks load error", err)
	}
}

func TestRunLoadsPresidioDetectorsByDefault(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	presidio := []anonymizer.Detector{
		staticDetector{matches: []anonymizer.Match{{Start: 5, End: 15, Type: anonymizer.EntityDate, Priority: 600}}},
	}
	err := runWithDependencies(
		strings.NewReader("date=2024-01-12\n"),
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(presidio, nil),
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "date=[DATE_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunReturnsPresidioLoadError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithDependencies(
		strings.NewReader("date=2024-01-12\n"),
		&stdout,
		&stderr,
		nil,
		newTestLLMFactory(),
		newTestServerEnsurer(),
		newTestExternalRulesLoader(nil, nil),
		newTestExternalRulesLoader(nil, fmt.Errorf("boom")),
	)
	if err == nil || !strings.Contains(err.Error(), "load presidio detectors") {
		t.Fatalf("error = %v, want presidio load error", err)
	}
}

func TestRunDoesNotEnsureOllamaWithoutLLMFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	called := false

	err := runWithLLMDependencies(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		nil,
		func(string, string, time.Duration) (llm.Extractor, error) {
			return nil, fmt.Errorf("should not be called")
		},
		func(context.Context, string, time.Duration) (func(), error) {
			called = true
			return func() {}, nil
		},
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if called {
		t.Fatal("ollama ensure was called without --llm")
	}
}

func TestRunWithLLMAnonymizesLLMEntities(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithLLMFactory(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		[]string{"--llm", "--stats"},
		func(string, string, time.Duration) (llm.Extractor, error) {
			return fakeCmdExtractor{
				entities: []llm.Entity{{Type: "PERSON_NAME", Text: "Jean Dupont"}},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "Bonjour [PERSON_NAME_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "llm.PERSON_NAME count=1") {
		t.Fatalf("stderr = %q, want person name stats", stderr.String())
	}
	if !strings.Contains(stderr.String(), "time.llm_startup=") ||
		!strings.Contains(stderr.String(), "time.llm_extraction=") ||
		!strings.Contains(stderr.String(), "time.llm_shutdown=") ||
		!strings.Contains(stderr.String(), "llm.chunks=1") {
		t.Fatalf("stderr = %q, want llm timing stats", stderr.String())
	}
}

func TestRunWithLLMCallsShutdownCleanup(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cleanupCalled := false

	err := runWithLLMDependencies(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		[]string{"--llm", "--stats"},
		func(string, string, time.Duration) (llm.Extractor, error) {
			return fakeCmdExtractor{}, nil
		},
		func(context.Context, string, time.Duration) (func(), error) {
			return func() { cleanupCalled = true }, nil
		},
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("ollama cleanup was not called")
	}
	if !strings.Contains(stderr.String(), "time.llm_shutdown=") {
		t.Fatalf("stderr = %q, want llm shutdown timing", stderr.String())
	}
}

func TestRunWithLLMReturnsOllamaStartupError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithLLMDependencies(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		[]string{"--llm"},
		func(string, string, time.Duration) (llm.Extractor, error) {
			return fakeCmdExtractor{}, nil
		},
		func(context.Context, string, time.Duration) (func(), error) {
			return nil, fmt.Errorf("ollama missing")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "start ollama") {
		t.Fatalf("error = %v, want ollama startup error", err)
	}
}

func TestRunWithLLMKeepsRegexPriorityOverLLM(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithLLMFactory(
		strings.NewReader("Email: alice@example.com\n"),
		&stdout,
		&stderr,
		[]string{"--llm", "--stats"},
		func(string, string, time.Duration) (llm.Extractor, error) {
			return fakeCmdExtractor{
				entities: []llm.Entity{{Type: "OTHER_PII", Text: "alice@example.com"}},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got, want := stdout.String(), "Email: [EMAIL_1]\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if strings.Contains(stderr.String(), "llm.OTHER_PII") {
		t.Fatalf("stderr = %q, LLM overlap should not be counted", stderr.String())
	}
}

func TestRunWithLLMReturnsExtractorError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runWithLLMFactory(
		strings.NewReader("Bonjour Jean Dupont\n"),
		&stdout,
		&stderr,
		[]string{"--llm"},
		func(string, string, time.Duration) (llm.Extractor, error) {
			return fakeCmdExtractor{err: fmt.Errorf("llm down")}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "extract llm pii") {
		t.Fatalf("error = %v, want llm extraction error", err)
	}
}

type fakeCmdExtractor struct {
	entities []llm.Entity
	err      error
}

func (f fakeCmdExtractor) Extract(context.Context, []byte) ([]llm.Entity, error) {
	return f.entities, f.err
}

type staticDetector struct {
	matches []anonymizer.Match
}

func (d staticDetector) FindAll([]byte) []anonymizer.Match {
	return d.matches
}

func newTestLLMFactory() llmExtractorFactory {
	return func(string, string, time.Duration) (llm.Extractor, error) {
		return fakeCmdExtractor{}, nil
	}
}

func newTestServerEnsurer() llmServerEnsurer {
	return func(context.Context, string, time.Duration) (func(), error) {
		return func() {}, nil
	}
}

func newTestExternalRulesLoader(loaded []anonymizer.Detector, err error) externalRulesLoader {
	return func(context.Context, string, time.Duration) (detectors.ExternalRuleLoadResult, error) {
		return detectors.ExternalRuleLoadResult{Detectors: loaded}, err
	}
}
