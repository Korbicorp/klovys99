package anonymizer

import "testing"

type staticDetector struct {
	matches []Match
}

func (d staticDetector) FindAll([]byte) []Match {
	return append([]Match(nil), d.matches...)
}

func TestAnonymizeKeepsStableTokensForRepeatedValues(t *testing.T) {
	engine := New([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 16, Type: EntityEmail, Priority: 10, Normalized: "alice@example.io"},
				{Start: 21, End: 37, Type: EntityEmail, Priority: 10, Normalized: "alice@example.io"},
			},
		},
	})

	output, result := engine.Anonymize([]byte("alice@example.io and alice@example.io"))

	if got, want := string(output), "[EMAIL_1] and [EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := result.Stats[EntityEmail].Count, 2; got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}

func TestAnonymizeAllocatesDistinctTokensForDistinctValues(t *testing.T) {
	engine := New([]Detector{
		staticDetector{
			matches: []Match{
				{Start: 0, End: 12, Type: EntityEmail, Priority: 10, Normalized: "a@example.io"},
				{Start: 13, End: 25, Type: EntityEmail, Priority: 10, Normalized: "b@example.io"},
			},
		},
	})

	output, _ := engine.Anonymize([]byte("a@example.io b@example.io"))

	if got, want := string(output), "[EMAIL_1] [EMAIL_2]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAnonymizeResolvesOverlapsByPriorityThenLength(t *testing.T) {
	engine := New([]Detector{
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

	output, result := engine.Anonymize([]byte("name@example.com"))

	if got, want := string(output), "[EMAIL_1]"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, ok := result.Stats[EntityLastName]; ok {
		t.Fatal("lower priority overlapping match should not be counted")
	}
}
