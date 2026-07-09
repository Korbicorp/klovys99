package anonymizer

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

type Service struct {
	// detectors is the ordered set of regex-based detectors used for each call.
	detectors        []Detector
	protectionPolicy ProtectionPolicy
	tokenStore       TokenStoreFactory
}

type Run struct {
	service  *Service
	store    RunTokenStore
	fallback *memoryRunTokenStore
	// logMatches controls whether raw detector matches are emitted in debug logs.
	logMatches bool
}

type allowAllProtectionPolicy struct{}

// ProtectionPolicy decides whether a detected type can be anonymized.
type ProtectionPolicy interface {
	IsTypeEnabled(entityType EntityType) bool
}

var tokenPattern = regexp.MustCompile(`\[[A-Z_]+_[0-9]+\]`)

// IsTypeEnabled keeps every type enabled when no external policy is configured.
func (allowAllProtectionPolicy) IsTypeEnabled(EntityType) bool {
	return true
}

// NewService creates an anonymizer with every detected type enabled.
func NewService(detectors []Detector) *Service {
	return NewServiceWithProtectionPolicy(detectors, nil)
}

// NewServiceWithProtectionPolicy creates an anonymizer that filters matches through a runtime policy.
func NewServiceWithProtectionPolicy(detectors []Detector, protectionPolicy ProtectionPolicy) *Service {
	return NewServiceWithProtectionPolicyAndTokenStore(detectors, protectionPolicy, nil)
}

// NewServiceWithTokenStore creates an anonymizer that persists token mappings in the provided store.
func NewServiceWithTokenStore(detectors []Detector, tokenStore TokenStoreFactory) *Service {
	return NewServiceWithProtectionPolicyAndTokenStore(detectors, nil, tokenStore)
}

// NewServiceWithProtectionPolicyAndTokenStore creates an anonymizer with a runtime policy and token store.
func NewServiceWithProtectionPolicyAndTokenStore(detectors []Detector, protectionPolicy ProtectionPolicy, tokenStore TokenStoreFactory) *Service {
	if protectionPolicy == nil {
		protectionPolicy = allowAllProtectionPolicy{}
	}
	return &Service{
		detectors:        detectors,
		protectionPolicy: protectionPolicy,
		tokenStore:       tokenStore,
	}
}

// NewRun creates an isolated anonymization run with stable tokens across calls.
func (a *Service) NewRun() *Run {
	return a.newRun(true)
}

func (a *Service) newRun(logMatches bool) *Run {
	store, err := newRunTokenStore(a.tokenStore)
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize token store, falling back to in-memory mappings")
		store = newMemoryRunTokenStore()
	}

	return &Run{
		service:    a,
		store:      store,
		fallback:   newMemoryRunTokenStore(),
		logMatches: logMatches,
	}
}

// Anonymize replaces detected sensitive values in one input.
func (a *Service) Anonymize(input string) (string, Result) {
	return a.NewRun().Anonymize(input)
}

// Preview returns an anonymization preview for the currently enabled protections.
func (a *Service) Preview(input string) PreviewResult {
	return a.PreviewWithMatches(input, nil)
}

// PreviewWithMatches returns an anonymization preview that includes caller-provided matches.
func (a *Service) PreviewWithMatches(input string, extraMatches []Match) PreviewResult {
	run := a.newRun(false)
	return run.PreviewWithMatches(input, extraMatches)
}

// Anonymize replaces detected sensitive values while reusing run-local tokens.
func (r *Run) Anonymize(input string) (string, Result) {
	return r.AnonymizeWithMatches(input, nil)
}

// AnonymizeWithMatches replaces detected sensitive values plus caller-provided matches for one run.
func (r *Run) AnonymizeWithMatches(input string, extraMatches []Match) (string, Result) {
	matches := r.resolvedEnabledMatches(input, extraMatches)
	return r.anonymizeResolvedMatches(input, matches)
}

// PreviewWithMatches returns a visual preview without emitting raw sensitive logs.
func (r *Run) PreviewWithMatches(input string, extraMatches []Match) PreviewResult {
	enabledMatches := r.resolvedEnabledMatches(input, extraMatches)
	anonymized, result := r.anonymizeResolvedMatches(input, enabledMatches)
	return PreviewResult{
		Anonymized: anonymized,
		Result:     result,
	}
}

// Deanonymize replaces known anonymization tokens with their original values.
func (r *Run) Deanonymize(input string) (string, bool) {
	indices := tokenPattern.FindAllStringIndex(input, -1)
	if len(indices) == 0 {
		return input, false
	}

	var output strings.Builder
	output.Grow(len(input))

	last := 0
	replaced := false
	for _, index := range indices {
		output.WriteString(input[last:index[0]])

		token := input[index[0]:index[1]]
		if value, ok := r.ValueForToken(token); ok {
			output.WriteString(value)
			replaced = true
		} else {
			output.WriteString(token)
		}

		last = index[1]
	}

	output.WriteString(input[last:])
	return output.String(), replaced
}

// ValueForToken resolves one anonymization token to its original value when known.
func (r *Run) ValueForToken(token string) (string, bool) {
	if r == nil || r.store == nil {
		return "", false
	}

	value, ok, err := r.store.ValueForToken(token)
	if err != nil {
		log.Error().Err(err).Str("token", token).Msg("Failed to resolve anonymization token")
		if r.fallback == nil {
			return "", false
		}
		value, ok, _ = r.fallback.ValueForToken(token)
		return value, ok
	}

	return value, ok
}

// Close releases resources associated with the run store.
func (r *Run) Close() error {
	if r == nil || r.store == nil {
		return nil
	}

	return r.store.Close()
}

func (r *Run) anonymizeResolvedMatches(input string, matches []Match) (string, Result) {
	if r.logMatches && len(matches) > 0 {
		log.Debug().Interface("pii", matches).Msg("Secret and PII found")
	}

	result := Result{Stats: make(map[EntityType]EntityStats)}
	var output strings.Builder
	output.Grow(len(input))

	last := 0
	for _, match := range matches {
		output.WriteString(input[last:match.Start])

		value := input[match.Start:match.End]
		key := normalizedKey(match, value)
		token := r.tokenFor(match.Type, key, value)
		output.WriteString(token)
		updateStats(result.Stats, match.Type)
		result.Findings = append(result.Findings, Finding{
			Type:  match.Type,
			Value: value,
			Start: match.Start,
			End:   match.End,
			Token: token,
		})
		last = match.End
	}

	output.WriteString(input[last:])
	return output.String(), result
}

// filterEnabledMatches removes matches for types disabled by the current protection policy.
func (a *Service) filterEnabledMatches(matches []Match) []Match {
	if a == nil || a.protectionPolicy == nil {
		return matches
	}
	filtered := matches[:0]
	for _, match := range matches {
		if !a.protectionPolicy.IsTypeEnabled(match.Type) {
			continue
		}
		filtered = append(filtered, match)
	}
	return filtered
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

func (a *Service) normalizedMatches(input string, extraMatches []Match) []Match {
	matches := append(a.collectMatches(input, a.detectors), extraMatches...)
	return deduplicateMatches(validMatches(input, matches))
}

func (r *Run) resolvedEnabledMatches(input string, extraMatches []Match) []Match {
	matches := r.service.filterEnabledMatches(r.service.normalizedMatches(input, extraMatches))
	matches = r.service.resolveOverlaps(matches)
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Start < matches[j].Start
	})
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

func (r *Run) tokenFor(entityType EntityType, key string, value string) string {
	token, err := r.store.TokenFor(entityType, key, value)
	if err != nil {
		log.Error().Err(err).Str("entity_type", string(entityType)).Msg("Failed to persist anonymization token, using in-memory fallback")
		token, _ = r.fallback.TokenFor(entityType, key, value)
	}

	return token
}

func normalizedKey(match Match, value string) string {
	if match.Normalized != "" {
		return match.Normalized
	}

	return strings.ToLower(strings.TrimSpace(value))
}
