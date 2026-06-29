package anonymizer

import (
	"fmt"
	"sort"
	"strings"
)

type Service struct {
	// detectors is the ordered set of regex-based detectors used for each call.
	detectors []Detector
}

type Run struct {
	service *Service
	// tokens stores stable pseudonyms by entity type and normalized value.
	tokens map[EntityType]map[string]string
	// nextID tracks the next numeric suffix to allocate for each entity type.
	nextID map[EntityType]int
}

func NewService(detectors []Detector) *Service {
	return &Service{
		detectors: detectors,
	}
}

func (a *Service) NewRun() *Run {
	return &Run{
		service: a,
		tokens:  make(map[EntityType]map[string]string),
		nextID:  make(map[EntityType]int),
	}
}

func (a *Service) Anonymize(input string) (string, Result) {
	return a.AnonymizeWithMatches(input, nil)
}

func (a *Service) AnonymizeWithMatches(input string, extraMatches []Match) (string, Result) {
	return a.NewRun().AnonymizeWithMatches(input, extraMatches)
}

func (r *Run) Anonymize(input string) (string, Result) {
	return r.AnonymizeWithMatches(input, nil)
}

func (r *Run) AnonymizeWithMatches(input string, extraMatches []Match) (string, Result) {
	matches := append(r.service.collectMatches(input, r.service.detectors), extraMatches...)
	matches = deduplicateMatches(validMatches(input, matches))
	matches = r.service.resolveOverlaps(matches)
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Start < matches[j].Start
	})

	result := Result{Stats: make(map[EntityType]EntityStats)}

	var output strings.Builder
	output.Grow(len(input))

	last := 0
	for _, match := range matches {
		output.WriteString(input[last:match.Start])

		value := input[match.Start:match.End]
		key := normalizedKey(match, value)
		output.WriteString(r.tokenFor(match.Type, key))
		updateStats(result.Stats, match.Type)

		last = match.End
	}

	output.WriteString(input[last:])
	return output.String(), result
}

func validMatches(input string, matches []Match) []Match {
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

func (a *Service) collectMatches(input string, detectors []Detector) []Match {
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

func (a *Service) resolveOverlaps(matches []Match) []Match {
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

func (r *Run) tokenFor(entityType EntityType, key string) string {
	if r.tokens[entityType] == nil {
		r.tokens[entityType] = make(map[string]string)
	}
	if token, ok := r.tokens[entityType][key]; ok {
		return token
	}

	// Tokens are stable for the lifetime of the run.
	r.nextID[entityType]++
	token := fmt.Sprintf("[%s_%d]", entityType, r.nextID[entityType])
	r.tokens[entityType][key] = token
	return token
}

func normalizedKey(match Match, value string) string {
	if match.Normalized != "" {
		return match.Normalized
	}

	return strings.ToLower(strings.TrimSpace(value))
}
