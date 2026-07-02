package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const defaultCorpusDir = "test/anonymization/corpus"

type expectedFile struct {
	Entities []ExpectedEntity `json:"entities"`
}

type ExpectedEntity struct {
	Type  anonymizer.EntityType `json:"type"`
	Value string                `json:"value"`
}

type CorpusCase struct {
	Name         string
	PromptPath   string
	ExpectedPath string
	Prompt       string
	Expected     []ExpectedEntity
}

type entityKey struct {
	Type  anonymizer.EntityType
	Value string
}

type EntityDelta struct {
	Type  anonymizer.EntityType
	Value string
	Count int
}

type Stats struct {
	Expected   int
	Found      int
	Matched    int
	Missing    int
	Unexpected int
}

type FileReport struct {
	Name         string
	Stats        Stats
	RelaxedStats Stats
	ByType       map[anonymizer.EntityType]Stats
	Missing      []EntityDelta
	Unexpected   []EntityDelta
}

type Report struct {
	Files         []FileReport
	Totals        Stats
	RelaxedTotals Stats
	ByType        map[anonymizer.EntityType]Stats
}

type textAnonymizer interface {
	Anonymize(string) (string, anonymizer.Result)
}

func main() {
	corpusDir := flag.String("corpus", defaultCorpusDir, "corpus directory containing prompts and expected folders")
	strict := flag.Bool("strict", true, "exit with status 1 when expected and found entities differ")
	flag.Parse()

	log.Logger = zerolog.Nop()

	loadResult, engine, err := loadProxyAnonymizer(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load anonymizer: %v\n", err)
		os.Exit(1)
	}

	cases, err := loadCorpus(*corpusDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load corpus: %v\n", err)
		os.Exit(1)
	}

	report := runCases(cases, engine)
	printReport(os.Stdout, *corpusDir, report, &loadResult)
	if err := exitStatus(report, *strict); err != nil {
		os.Exit(1)
	}
}

func loadProxyAnonymizer(ctx context.Context) (detectors.LoadResult, *anonymizer.Service, error) {
	service := detectors.NewService(detectors.DefaultConfig())
	loadResult, err := service.Load(ctx)
	if err != nil {
		return detectors.LoadResult{}, nil, err
	}
	return loadResult, anonymizer.NewService(loadResult.Detectors), nil
}

func loadCorpus(corpusDir string) ([]CorpusCase, error) {
	promptPattern := filepath.Join(corpusDir, "prompts", "*.txt")
	promptPaths, err := filepath.Glob(promptPattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(promptPaths)
	if len(promptPaths) == 0 {
		return nil, fmt.Errorf("no prompt files found in %s", filepath.Dir(promptPattern))
	}

	cases := make([]CorpusCase, 0, len(promptPaths))
	for _, promptPath := range promptPaths {
		name := strings.TrimSuffix(filepath.Base(promptPath), filepath.Ext(promptPath))
		expectedPath := filepath.Join(corpusDir, "expected", name+".json")

		prompt, err := os.ReadFile(promptPath)
		if err != nil {
			return nil, fmt.Errorf("read prompt %s: %w", promptPath, err)
		}
		expected, err := readExpected(expectedPath)
		if err != nil {
			return nil, fmt.Errorf("read expected %s: %w", expectedPath, err)
		}

		cases = append(cases, CorpusCase{
			Name:         name,
			PromptPath:   promptPath,
			ExpectedPath: expectedPath,
			Prompt:       string(prompt),
			Expected:     expected.Entities,
		})
	}

	return cases, nil
}

func readExpected(path string) (expectedFile, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return expectedFile{}, err
	}

	var expected expectedFile
	if err := json.Unmarshal(payload, &expected); err != nil {
		return expectedFile{}, err
	}
	for index, entity := range expected.Entities {
		if entity.Type == "" {
			return expectedFile{}, fmt.Errorf("entity %d has empty type", index)
		}
		if entity.Value == "" {
			return expectedFile{}, fmt.Errorf("entity %d has empty value", index)
		}
	}

	return expected, nil
}

func runCases(cases []CorpusCase, engine textAnonymizer) Report {
	report := Report{
		Files:  make([]FileReport, 0, len(cases)),
		ByType: make(map[anonymizer.EntityType]Stats),
	}

	for _, corpusCase := range cases {
		_, result := engine.Anonymize(corpusCase.Prompt)
		fileReport := compareCase(corpusCase, result.Findings)
		report.Files = append(report.Files, fileReport)
		report.addFile(fileReport)
	}

	return report
}

func compareCase(corpusCase CorpusCase, findings []anonymizer.Finding) FileReport {
	expected := expectedCounts(corpusCase.Expected)
	found := findingCounts(findings)
	keys := mergedKeys(expected, found)

	fileReport := FileReport{
		Name:   corpusCase.Name,
		ByType: make(map[anonymizer.EntityType]Stats),
	}
	fileReport.RelaxedStats = relaxedStats(expected, found)
	for _, key := range keys {
		expectedCount := expected[key]
		foundCount := found[key]
		matched := min(expectedCount, foundCount)

		fileReport.Stats.Expected += expectedCount
		fileReport.Stats.Found += foundCount
		fileReport.Stats.Matched += matched
		if missing := expectedCount - matched; missing > 0 {
			fileReport.Stats.Missing += missing
			fileReport.Missing = append(fileReport.Missing, EntityDelta{
				Type:  key.Type,
				Value: key.Value,
				Count: missing,
			})
		}
		if unexpected := foundCount - matched; unexpected > 0 {
			fileReport.Stats.Unexpected += unexpected
			fileReport.Unexpected = append(fileReport.Unexpected, EntityDelta{
				Type:  key.Type,
				Value: key.Value,
				Count: unexpected,
			})
		}

		typeStats := fileReport.ByType[key.Type]
		typeStats.Expected += expectedCount
		typeStats.Found += foundCount
		typeStats.Matched += matched
		typeStats.Missing += expectedCount - matched
		typeStats.Unexpected += foundCount - matched
		fileReport.ByType[key.Type] = typeStats
	}

	return fileReport
}

func relaxedStats(expected, found map[entityKey]int) Stats {
	stats := Stats{
		Expected: countEntities(expected),
		Found:    countEntities(found),
	}
	remainingExpected := cloneCounts(expected)
	remainingFound := cloneCounts(found)

	for _, key := range mergedKeys(expected, found) {
		matched := min(remainingExpected[key], remainingFound[key])
		if matched == 0 {
			continue
		}
		stats.Matched += matched
		remainingExpected[key] -= matched
		remainingFound[key] -= matched
	}

	foundKeys := sortedEntityKeys(remainingFound)
	for _, expectedKey := range sortedEntityKeys(remainingExpected) {
		for remainingExpected[expectedKey] > 0 {
			foundKey, ok := bestRelaxedMatch(expectedKey, foundKeys, remainingFound)
			if !ok {
				break
			}
			stats.Matched++
			remainingExpected[expectedKey]--
			remainingFound[foundKey]--
		}
	}

	stats.Missing = stats.Expected - stats.Matched
	stats.Unexpected = stats.Found - stats.Matched
	return stats
}

func countEntities(counts map[entityKey]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func cloneCounts(counts map[entityKey]int) map[entityKey]int {
	cloned := make(map[entityKey]int, len(counts))
	for key, count := range counts {
		cloned[key] = count
	}
	return cloned
}

func sortedEntityKeys(counts map[entityKey]int) []entityKey {
	keys := make([]entityKey, 0, len(counts))
	for key, count := range counts {
		if count > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Type != keys[j].Type {
			return keys[i].Type < keys[j].Type
		}
		return keys[i].Value < keys[j].Value
	})
	return keys
}

func bestRelaxedMatch(expected entityKey, foundKeys []entityKey, found map[entityKey]int) (entityKey, bool) {
	var best entityKey
	bestQuality := 0
	bestLength := 0
	bestFound := false
	for _, candidate := range foundKeys {
		if found[candidate] == 0 || !isRelaxedMatch(expected, candidate) {
			continue
		}
		quality := relaxedMatchQuality(expected, candidate)
		length := len(candidate.Value)
		if !bestFound || quality < bestQuality || (quality == bestQuality && length < bestLength) {
			best = candidate
			bestQuality = quality
			bestLength = length
			bestFound = true
		}
	}
	return best, bestFound
}

func isRelaxedMatch(expected, found entityKey) bool {
	return expected.Value != "" && strings.Contains(found.Value, expected.Value)
}

func relaxedMatchQuality(expected, found entityKey) int {
	if found.Value == expected.Value {
		return 0
	}
	if found.Type == expected.Type {
		return 1
	}
	return 2
}

func expectedCounts(entities []ExpectedEntity) map[entityKey]int {
	counts := make(map[entityKey]int, len(entities))
	for _, entity := range entities {
		counts[entityKey{Type: entity.Type, Value: entity.Value}]++
	}
	return counts
}

func findingCounts(findings []anonymizer.Finding) map[entityKey]int {
	counts := make(map[entityKey]int, len(findings))
	for _, finding := range findings {
		counts[entityKey{Type: finding.Type, Value: finding.Value}]++
	}
	return counts
}

func mergedKeys(left, right map[entityKey]int) []entityKey {
	seen := make(map[entityKey]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}

	keys := make([]entityKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Type != keys[j].Type {
			return keys[i].Type < keys[j].Type
		}
		return keys[i].Value < keys[j].Value
	})
	return keys
}

func (r *Report) addFile(fileReport FileReport) {
	r.Totals.add(fileReport.Stats)
	r.RelaxedTotals.add(fileReport.RelaxedStats)
	for entityType, stats := range fileReport.ByType {
		current := r.ByType[entityType]
		current.add(stats)
		r.ByType[entityType] = current
	}
}

func (s *Stats) add(other Stats) {
	s.Expected += other.Expected
	s.Found += other.Found
	s.Matched += other.Matched
	s.Missing += other.Missing
	s.Unexpected += other.Unexpected
}

func (r Report) HasMismatches() bool {
	return r.Totals.Missing > 0 || r.Totals.Unexpected > 0
}

func printReport(writer io.Writer, corpusDir string, report Report, loadResult *detectors.LoadResult) {
	fmt.Fprintf(writer, "Anonymization corpus stats\n")
	fmt.Fprintf(writer, "Corpus: %s\n", corpusDir)
	fmt.Fprintf(writer, "Files: %d\n", len(report.Files))
	if loadResult != nil {
		fmt.Fprintf(writer, "Detectors: builtin=%d external=%d total=%d\n", loadResult.BuiltinDetectors, loadResult.ExternalDetectors, len(loadResult.Detectors))
		printExternalMetrics(writer, "Gitleaks", loadResult.Gitleaks)
		printExternalMetrics(writer, "Presidio", loadResult.Presidio)
	}
	fmt.Fprintf(writer, "\nTotals: expected=%d found=%d matched=%d missing=%d unexpected=%d precision=%.2f%% recall=%.2f%%\n",
		report.Totals.Expected,
		report.Totals.Found,
		report.Totals.Matched,
		report.Totals.Missing,
		report.Totals.Unexpected,
		percent(report.Totals.Matched, report.Totals.Found),
		percent(report.Totals.Matched, report.Totals.Expected),
	)
	fmt.Fprintf(writer, "Relaxed totals: expected=%d found=%d matched=%d missing=%d unexpected=%d precision=%.2f%% recall=%.2f%%\n",
		report.RelaxedTotals.Expected,
		report.RelaxedTotals.Found,
		report.RelaxedTotals.Matched,
		report.RelaxedTotals.Missing,
		report.RelaxedTotals.Unexpected,
		percent(report.RelaxedTotals.Matched, report.RelaxedTotals.Found),
		percent(report.RelaxedTotals.Matched, report.RelaxedTotals.Expected),
	)

	fmt.Fprintln(writer, "\nBy entity type:")
	for _, entityType := range sortedTypes(report.ByType) {
		stats := report.ByType[entityType]
		fmt.Fprintf(writer, "  %s expected=%d found=%d matched=%d missing=%d unexpected=%d precision=%.2f%% recall=%.2f%%\n",
			entityType,
			stats.Expected,
			stats.Found,
			stats.Matched,
			stats.Missing,
			stats.Unexpected,
			percent(stats.Matched, stats.Found),
			percent(stats.Matched, stats.Expected),
		)
	}

	fmt.Fprintln(writer, "\nMismatches:")
	if !report.HasMismatches() {
		fmt.Fprintln(writer, "  none")
		return
	}
	for _, fileReport := range report.Files {
		if fileReport.Stats.Missing == 0 && fileReport.Stats.Unexpected == 0 {
			continue
		}
		fmt.Fprintf(writer, "  %s\n", fileReport.Name)
		printDeltas(writer, "missing", fileReport.Missing)
		printDeltas(writer, "unexpected", fileReport.Unexpected)
	}
}

func printExternalMetrics(writer io.Writer, label string, metrics detectors.ExternalLoadMetrics) {
	fmt.Fprintf(writer, "%s: detectors=%d rules=%d recognizers=%d patterns=%d files=%d cache_hits=%d downloads=%d cache_fallbacks=%d total=%s\n",
		label,
		metrics.Detectors,
		metrics.Rules,
		metrics.Recognizers,
		metrics.Patterns,
		metrics.Files,
		metrics.CacheHits,
		metrics.Downloads,
		metrics.CacheFallbacks,
		metrics.Total,
	)
}

func sortedTypes(statsByType map[anonymizer.EntityType]Stats) []anonymizer.EntityType {
	entityTypes := make([]anonymizer.EntityType, 0, len(statsByType))
	for entityType := range statsByType {
		entityTypes = append(entityTypes, entityType)
	}
	sort.Slice(entityTypes, func(i, j int) bool {
		return entityTypes[i] < entityTypes[j]
	})
	return entityTypes
}

func printDeltas(writer io.Writer, label string, deltas []EntityDelta) {
	for _, delta := range deltas {
		fmt.Fprintf(writer, "    %s: %s %q x%d\n", label, delta.Type, delta.Value, delta.Count)
	}
}

func percent(numerator, denominator int) float64 {
	if denominator == 0 {
		if numerator == 0 {
			return 100
		}
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var errStrictMismatch = errors.New("anonymization corpus mismatches found")

func exitStatus(report Report, strict bool) error {
	if strict && report.HasMismatches() {
		return errStrictMismatch
	}
	return nil
}
