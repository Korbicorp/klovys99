package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovis/internal/llm"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestRunReadsStdinAndWritesStdout(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(strings.NewReader("Email: alice@example.com\n"), &stdout, &stderr, nil)
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

	err := run(strings.NewReader("Tel: 06 12 34 56 78\n"), &stdout, &stderr, []string{"--stats"})
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
		!strings.Contains(stderr.String(), "time.total=") {
		t.Fatalf("stderr = %q, want timing stats", stderr.String())
	}
}

func TestRunNoExtraDisablesExtraDetectors(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	input := "https://example.com aa:bb:cc:dd:ee:ff"

	err := run(strings.NewReader(input), &stdout, &stderr, []string{"--no-extra"})
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

	err := run(failingReader{}, &stdout, &stderr, nil)
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
