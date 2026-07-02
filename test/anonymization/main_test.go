package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
)

type fakeAnonymizer struct {
	result anonymizer.Result
}

func (f fakeAnonymizer) Anonymize(input string) (string, anonymizer.Result) {
	return input, f.result
}

func TestLoadCorpusReadsPromptsAndExpectedSidecars(t *testing.T) {
	corpusDir := t.TempDir()
	mustMkdir(t, filepath.Join(corpusDir, "prompts"))
	mustMkdir(t, filepath.Join(corpusDir, "expected"))
	mustWrite(t, filepath.Join(corpusDir, "prompts", "one.txt"), "Email alice@example.com")
	mustWrite(t, filepath.Join(corpusDir, "expected", "one.json"), `{"entities":[{"type":"EMAIL","value":"alice@example.com"}]}`)

	cases, err := loadCorpus(corpusDir)
	if err != nil {
		t.Fatalf("loadCorpus returned error: %v", err)
	}

	if got, want := len(cases), 1; got != want {
		t.Fatalf("case count = %d, want %d", got, want)
	}
	if got, want := cases[0].Name, "one"; got != want {
		t.Fatalf("case name = %q, want %q", got, want)
	}
	if got, want := cases[0].Prompt, "Email alice@example.com"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if got, want := cases[0].Expected[0].Type, anonymizer.EntityEmail; got != want {
		t.Fatalf("expected type = %q, want %q", got, want)
	}
}

func TestCorpusKeepsClaudeCodeFalsePositiveContextVisible(t *testing.T) {
	cases, err := loadCorpus("corpus")
	if err != nil {
		t.Fatalf("loadCorpus returned error: %v", err)
	}

	var corpusCase CorpusCase
	found := false
	for _, candidate := range cases {
		if candidate.Name == "en_claude_code_false_positive_context" {
			corpusCase = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("en_claude_code_false_positive_context corpus case not found")
	}

	engine := anonymizer.NewService(detectors.Default(true))
	_, result := engine.Anonymize(corpusCase.Prompt)
	report := compareCase(corpusCase, result.Findings)

	if report.Stats != (Stats{}) {
		t.Fatalf("stats = %#v, want no expected or found entities", report.Stats)
	}
}

func TestCompareCaseMatchesMissingAndUnexpectedByTypeAndValue(t *testing.T) {
	corpusCase := CorpusCase{
		Name: "one",
		Expected: []ExpectedEntity{
			{Type: anonymizer.EntityEmail, Value: "alice@example.com"},
			{Type: anonymizer.EntityPhone, Value: "06 12 34 56 78"},
		},
	}
	findings := []anonymizer.Finding{
		{Type: anonymizer.EntityEmail, Value: "alice@example.com"},
		{Type: anonymizer.EntityIP, Value: "192.168.1.42"},
	}

	report := compareCase(corpusCase, findings)

	if got, want := report.Stats, (Stats{Expected: 2, Found: 2, Matched: 1, Missing: 1, Unexpected: 1}); got != want {
		t.Fatalf("stats = %#v, want %#v", got, want)
	}
	if got, want := report.RelaxedStats, report.Stats; got != want {
		t.Fatalf("relaxed stats = %#v, want %#v", got, want)
	}
	if got, want := report.Missing[0].Type, anonymizer.EntityPhone; got != want {
		t.Fatalf("missing type = %q, want %q", got, want)
	}
	if got, want := report.Unexpected[0].Type, anonymizer.EntityIP; got != want {
		t.Fatalf("unexpected type = %q, want %q", got, want)
	}
}

func TestCompareCaseComputesRelaxedStatsForWrongTypeAndTooWideMatches(t *testing.T) {
	corpusCase := CorpusCase{
		Name: "one",
		Expected: []ExpectedEntity{
			{Type: anonymizer.EntityEmail, Value: "alice@example.com"},
			{Type: anonymizer.EntitySecret, Value: "sk_live_123"},
			{Type: anonymizer.EntityPhone, Value: "06 12 34 56 78"},
		},
	}
	findings := []anonymizer.Finding{
		{Type: anonymizer.EntityIP, Value: "alice@example.com"},
		{Type: anonymizer.EntitySecret, Value: "API_KEY=sk_live_123"},
		{Type: anonymizer.EntityDate, Value: "2026-07-02"},
	}

	report := compareCase(corpusCase, findings)

	if got, want := report.Stats, (Stats{Expected: 3, Found: 3, Missing: 3, Unexpected: 3}); got != want {
		t.Fatalf("strict stats = %#v, want %#v", got, want)
	}
	if got, want := report.RelaxedStats, (Stats{Expected: 3, Found: 3, Matched: 2, Missing: 1, Unexpected: 1}); got != want {
		t.Fatalf("relaxed stats = %#v, want %#v", got, want)
	}
}

func TestRunCasesComputesGlobalStats(t *testing.T) {
	cases := []CorpusCase{
		{
			Name: "one",
			Expected: []ExpectedEntity{
				{Type: anonymizer.EntityEmail, Value: "alice@example.com"},
			},
		},
	}
	engine := fakeAnonymizer{
		result: anonymizer.Result{
			Findings: []anonymizer.Finding{
				{Type: anonymizer.EntityEmail, Value: "alice@example.com"},
			},
		},
	}

	report := runCases(cases, engine)

	if got, want := report.Totals, (Stats{Expected: 1, Found: 1, Matched: 1}); got != want {
		t.Fatalf("totals = %#v, want %#v", got, want)
	}
	if got, want := report.RelaxedTotals, (Stats{Expected: 1, Found: 1, Matched: 1}); got != want {
		t.Fatalf("relaxed totals = %#v, want %#v", got, want)
	}
	if got, want := report.ByType[anonymizer.EntityEmail], (Stats{Expected: 1, Found: 1, Matched: 1}); got != want {
		t.Fatalf("email stats = %#v, want %#v", got, want)
	}
	if report.HasMismatches() {
		t.Fatal("report should not have mismatches")
	}
}

func TestExitStatusIsStrictOnlyWhenRequested(t *testing.T) {
	report := Report{Totals: Stats{Missing: 1}}

	if err := exitStatus(report, false); err != nil {
		t.Fatalf("non-strict exitStatus returned error: %v", err)
	}
	if err := exitStatus(report, true); !errors.Is(err, errStrictMismatch) {
		t.Fatalf("strict exitStatus error = %v, want errStrictMismatch", err)
	}
}

func TestPrintReportIncludesStatsAndMismatches(t *testing.T) {
	report := Report{
		Files: []FileReport{
			{
				Name:  "one",
				Stats: Stats{Expected: 1, Found: 1, Missing: 1, Unexpected: 1},
				Missing: []EntityDelta{
					{Type: anonymizer.EntityEmail, Value: "alice@example.com", Count: 1},
				},
				Unexpected: []EntityDelta{
					{Type: anonymizer.EntityIP, Value: "192.168.1.42", Count: 1},
				},
			},
		},
		Totals:        Stats{Expected: 1, Found: 1, Missing: 1, Unexpected: 1},
		RelaxedTotals: Stats{Expected: 1, Found: 1, Matched: 1},
		ByType: map[anonymizer.EntityType]Stats{
			anonymizer.EntityEmail: {Expected: 1, Missing: 1},
			anonymizer.EntityIP:    {Found: 1, Unexpected: 1},
		},
	}

	var output bytes.Buffer
	printReport(&output, "corpus", report, nil)

	got := output.String()
	for _, expected := range []string{
		"Files: 1",
		"Totals: expected=1 found=1 matched=0 missing=1 unexpected=1",
		"Relaxed totals: expected=1 found=1 matched=1 missing=0 unexpected=0",
		"EMAIL expected=1",
		"missing: EMAIL \"alice@example.com\" x1",
		"unexpected: IP \"192.168.1.42\" x1",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("report output = %q, want %q", got, expected)
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
