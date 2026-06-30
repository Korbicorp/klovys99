package anonymizer

import (
	"sync"
	"testing"
)

type staticDetector struct {
	matches []Match
}

func (d staticDetector) FindAll(string) []Match {
	return append([]Match(nil), d.matches...)
}

func TestAnonymizeKeepsStableTokensForRepeatedValues(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 16, Type: EntityEmail, Priority: 10, Normalized: "alice@example.io"},
				{Start: 21, End: 37, Type: EntityEmail, Priority: 10, Normalized: "alice@example.io"},
			},
		},
	})

	output, result := engine.Anonymize("alice@example.io and alice@example.io")

	if got, want := output, "[EMAIL_1] and [EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[EntityEmail].Count, 2; got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
	if got, want := len(result.Findings), 2; got != want {
		t.Fatalf("findings count = %d, want %d", got, want)
	}
	if got, want := result.Findings[0].Value, "alice@example.io"; got != want {
		t.Fatalf("finding value = %q, want %q", got, want)
	}
	if got, want := result.Findings[0].Token, "[EMAIL_1]"; got != want {
		t.Fatalf("finding token = %q, want %q", got, want)
	}
}

func TestAnonymizeAllocatesDistinctTokensForDistinctValues(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 12, Type: EntityEmail, Priority: 10, Normalized: "a@example.io"},
				{Start: 13, End: 25, Type: EntityEmail, Priority: 10, Normalized: "b@example.io"},
			},
		},
	})

	output, _ := engine.Anonymize("a@example.io b@example.io")

	if got, want := output, "[EMAIL_1] [EMAIL_2]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAnonymizeDoesNotShareTokensAcrossCalls(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 12, Type: EntityEmail, Priority: 10},
			},
		},
	})

	first, _ := engine.Anonymize("a@example.io")
	second, _ := engine.Anonymize("b@example.io")

	if got, want := first, "[EMAIL_1]"; got != want {
		t.Fatalf("first output = %q, want %q", got, want)
	}
	if got, want := second, "[EMAIL_1]"; got != want {
		t.Fatalf("second output = %q, want %q", got, want)
	}
}

func TestRunKeepsStableTokensAcrossCalls(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 12, Type: EntityEmail, Priority: 10},
			},
		},
	})
	run := engine.NewRun()

	first, _ := run.Anonymize("a@example.io")
	second, _ := run.Anonymize("b@example.io")
	third, _ := run.Anonymize("a@example.io")

	if got, want := first, "[EMAIL_1]"; got != want {
		t.Fatalf("first output = %q, want %q", got, want)
	}
	if got, want := second, "[EMAIL_2]"; got != want {
		t.Fatalf("second output = %q, want %q", got, want)
	}
	if got, want := third, "[EMAIL_1]"; got != want {
		t.Fatalf("third output = %q, want %q", got, want)
	}
}

func TestServiceAnonymizeRunsConcurrently(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 12, Type: EntityEmail, Priority: 10},
			},
		},
	})

	var wg sync.WaitGroup
	errs := make(chan string, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			output, _ := engine.Anonymize("a@example.io")
			if output != "[EMAIL_1]" {
				errs <- output
			}
		}()
	}
	wg.Wait()
	close(errs)

	for output := range errs {
		t.Fatalf("output = %q, want [EMAIL_1]", output)
	}
}

func TestAnonymizeResolvesOverlapsByPriorityThenLength(t *testing.T) {
	engine := NewService([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 5, End: 16, Type: EntityLastName, Priority: 10, Normalized: "example.com"},
			},
		},
		staticDetector{
			matches: []Match{
				{Start: 0, End: 16, Type: EntityEmail, Priority: 20, Normalized: "name@example.com"},
			},
		},
	})

	output, result := engine.Anonymize("name@example.com")

	if got, want := output, "[EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, ok := result.Stats[EntityLastName]; ok {
		t.Fatal("lower priority overlapping match should not be counted")
	}
}
