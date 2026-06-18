package anonymizer

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

type Anonymizer struct {
	// detectors is the ordered set of regex-based detectors used for each call.
	detectors []Detector
	// tokens stores stable pseudonyms by entity type and normalized value.
	tokens map[EntityType]map[string]string
	// nextID tracks the next numeric suffix to allocate for each entity type.
	nextID map[EntityType]int
}

func New(detectors []Detector) *Anonymizer {
	return &Anonymizer{
		detectors: detectors,
		tokens:    make(map[EntityType]map[string]string),
		nextID:    make(map[EntityType]int),
	}
}

func (a *Anonymizer) Anonymize(input []byte) ([]byte, Result) {
	return a.AnonymizeWithMatches(input, nil)
}

func (a *Anonymizer) AnonymizeWithMatches(input []byte, extraMatches []Match) ([]byte, Result) {
	matches := append(a.collectMatches(input, a.detectors), extraMatches...)
	matches = deduplicateMatches(validMatches(input, matches))
	matches = a.resolveOverlaps(matches)
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Start < matches[j].Start
	})

	result := Result{Stats: make(map[EntityType]EntityStats)}

	var output bytes.Buffer
	output.Grow(len(input))

	last := 0
	for _, match := range matches {
		output.Write(input[last:match.Start])

		value := input[match.Start:match.End]
		key := normalizedKey(match, value)
		output.WriteString(a.tokenFor(match.Type, key))
		updateStats(result.Stats, match.Type)

		last = match.End
	}

	output.Write(input[last:])
	return output.Bytes(), result
}

func validMatches(input []byte, matches []Match) []Match {
	valid := matches[:0]
	for _, match := range matches {
		if match.Start < 0 || match.End > len(input) || match.Start >= match.End || match.Type == "" {
			continue
		}
		valid = append(valid, match)
	}

	return valid
}

func deduplicateMatches(matches []Match) []Match {
	seen := make(map[matchKey]struct{}, len(matches))
	deduplicated := matches[:0]
	for _, match := range matches {
		key := matchKey{start: match.Start, end: match.End, entityType: match.Type}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduplicated = append(deduplicated, match)
	}

	return deduplicated
}

type matchKey struct {
	start      int
	end        int
	entityType EntityType
}

func (a *Anonymizer) collectMatches(input []byte, detectors []Detector) []Match {
	var matches []Match
	for _, detector := range detectors {
		matches = append(matches, detector.FindAll(input)...)
	}
	return matches
}

func updateStats(statsByType map[EntityType]EntityStats, entityType EntityType) {
	stats := statsByType[entityType]
	stats.Count++
	statsByType[entityType] = stats
}

func (a *Anonymizer) resolveOverlaps(matches []Match) []Match {
	// Resolve conflicts before rebuilding the output so replacements never cascade
	// into each other and higher-confidence detectors keep precedence.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Priority != matches[j].Priority {
			return matches[i].Priority > matches[j].Priority
		}
		if matches[i].Len() != matches[j].Len() {
			return matches[i].Len() > matches[j].Len()
		}
		if matches[i].Start != matches[j].Start {
			return matches[i].Start < matches[j].Start
		}
		return matches[i].End < matches[j].End
	})

	selected := make([]Match, 0, len(matches))
	for _, candidate := range matches {
		if overlapsAny(candidate, selected) {
			continue
		}
		selected = append(selected, candidate)
	}

	return selected
}

func overlapsAny(candidate Match, selected []Match) bool {
	for _, current := range selected {
		if candidate.Start < current.End && current.Start < candidate.End {
			return true
		}
	}

	return false
}

func (a *Anonymizer) tokenFor(entityType EntityType, key string) string {
	if a.tokens[entityType] == nil {
		a.tokens[entityType] = make(map[string]string)
	}
	if token, ok := a.tokens[entityType][key]; ok {
		return token
	}

	// Tokens are stable for the lifetime of the Anonymizer, not persisted across
	// executions. This keeps the CLI stateless and predictable in pipelines.
	a.nextID[entityType]++
	token := fmt.Sprintf("[%s_%d]", entityType, a.nextID[entityType])
	a.tokens[entityType][key] = token
	return token
}

func normalizedKey(match Match, value []byte) string {
	if match.Normalized != "" {
		return match.Normalized
	}

	return strings.ToLower(strings.TrimSpace(string(value)))
}
