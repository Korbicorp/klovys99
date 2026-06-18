package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
	"github.com/Korbicorp/klovis/internal/llm"
)

func main() {
	if err := run(os.Stdin, os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "klovis: %v\n", err)
		os.Exit(1)
	}
}

func run(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	return runWithDependencies(
		stdin,
		stdout,
		stderr,
		args,
		newOllamaExtractor,
		llm.EnsureOllamaServer,
		detectors.LoadGitleaksRulesWithStats,
		detectors.LoadPresidioRulesWithStats,
	)
}

type llmExtractorFactory func(baseURL, model string, timeout time.Duration) (llm.Extractor, error)
type llmServerEnsurer func(ctx context.Context, baseURL string, timeout time.Duration) (func(), error)
type externalRulesLoader func(ctx context.Context, sourceURL string, timeout time.Duration) (detectors.ExternalRuleLoadResult, error)

func runWithLLMFactory(stdin io.Reader, stdout, stderr io.Writer, args []string, factory llmExtractorFactory) error {
	return runWithDependencies(
		stdin,
		stdout,
		stderr,
		args,
		factory,
		func(context.Context, string, time.Duration) (func(), error) { return func() {}, nil },
		func(context.Context, string, time.Duration) (detectors.ExternalRuleLoadResult, error) {
			return detectors.ExternalRuleLoadResult{}, nil
		},
		func(context.Context, string, time.Duration) (detectors.ExternalRuleLoadResult, error) {
			return detectors.ExternalRuleLoadResult{}, nil
		},
	)
}

func runWithLLMDependencies(stdin io.Reader, stdout, stderr io.Writer, args []string, factory llmExtractorFactory, ensureServer llmServerEnsurer) error {
	return runWithDependencies(
		stdin,
		stdout,
		stderr,
		args,
		factory,
		ensureServer,
		func(context.Context, string, time.Duration) (detectors.ExternalRuleLoadResult, error) {
			return detectors.ExternalRuleLoadResult{}, nil
		},
		func(context.Context, string, time.Duration) (detectors.ExternalRuleLoadResult, error) {
			return detectors.ExternalRuleLoadResult{}, nil
		},
	)
}

func runWithDependencies(stdin io.Reader, stdout, stderr io.Writer, args []string, factory llmExtractorFactory, ensureServer llmServerEnsurer, loadGitleaks externalRulesLoader, loadPresidio externalRulesLoader) error {
	totalStart := time.Now()
	timings := runTimings{}

	// 0. CLI setup: parse runtime options before touching stdin or starting detectors.
	flags := flag.NewFlagSet("klovis", flag.ContinueOnError)
	flags.SetOutput(stderr)

	stats := flags.Bool("stats", false, "write anonymization statistics to stderr")
	noExtra := flags.Bool("no-extra", false, "disable extra detectors such as URL, IBAN, credit cards and MAC addresses")
	noGitleaks := flags.Bool("no-gitleaks", false, "disable downloading and loading external Gitleaks secret detectors")
	noPresidio := flags.Bool("no-presidio", false, "disable downloading and loading external Presidio regex detectors")
	useLLM := flags.Bool("llm", false, "enable local LLM extraction through Ollama")
	gitleaksURL := flags.String("gitleaks-url", detectors.DefaultGitleaksURL, "Gitleaks TOML config URL used to load external secret detectors")
	gitleaksTimeout := flags.Duration("gitleaks-timeout", detectors.DefaultGitleaksTimeout, "timeout for downloading and parsing Gitleaks rules")
	presidioURL := flags.String("presidio-url", detectors.DefaultPresidioURL, "Presidio YAML config URL used to load external regex detectors")
	presidioTimeout := flags.Duration("presidio-timeout", detectors.DefaultPresidioTimeout, "timeout for downloading and parsing Presidio rules")
	llmURL := flags.String("llm-url", "http://localhost:11434", "Ollama base URL")
	llmModel := flags.String("llm-model", "mistral", "Ollama model name")
	llmTimeout := flags.Duration("llm-timeout", 30*time.Second, "Ollama request timeout")
	llmMaxChars := flags.Int("llm-max-chars", llm.DefaultMaxChunkBytes, "maximum input bytes sent to the LLM per chunk")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	var err error
	externalDetectors := []anonymizer.Detector(nil)
	if !*noGitleaks && loadGitleaks != nil {
		ctx, cancel := context.WithTimeout(context.Background(), *gitleaksTimeout)
		loadResult, loadErr := loadGitleaks(ctx, *gitleaksURL, *gitleaksTimeout)
		cancel()
		if loadErr != nil {
			return fmt.Errorf("load gitleaks detectors: %w", loadErr)
		}
		externalDetectors = append(externalDetectors, loadResult.Detectors...)
		timings.Gitleaks = loadResult.Metrics
	}
	if !*noPresidio && loadPresidio != nil {
		ctx, cancel := context.WithTimeout(context.Background(), *presidioTimeout)
		loadResult, loadErr := loadPresidio(ctx, *presidioURL, *presidioTimeout)
		cancel()
		if loadErr != nil {
			return fmt.Errorf("load presidio detectors: %w", loadErr)
		}
		externalDetectors = append(externalDetectors, loadResult.Detectors...)
		timings.Presidio = loadResult.Metrics
	}
	timings.ExternalDetectors = len(externalDetectors)

	readStart := time.Now()
	input, err := io.ReadAll(stdin)
	timings.StdinRead = time.Since(readStart)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	timings.StdinBytes = len(input)

	// 1. Local detection pipeline: regex and built-in detectors produce the baseline match set.
	engineStart := time.Now()
	builtinDetectors := detectors.Default(!*noExtra)
	timings.BuiltinDetectors = len(builtinDetectors)
	engine := anonymizer.New(append(builtinDetectors, externalDetectors...))
	timings.EngineInit = time.Since(engineStart)
	var llmMatches []anonymizer.Match
	if *useLLM {
		// 2. Optional LLM extraction: start Ollama, query the model, then merge those matches later.
		startupCtx, startupCancel := context.WithTimeout(context.Background(), *llmTimeout)
		startupStart := time.Now()
		cleanup, err := ensureServer(startupCtx, *llmURL, *llmTimeout)
		timings.LLMStartup = time.Since(startupStart)
		startupCancel()
		if err != nil {
			return fmt.Errorf("start ollama: %w", err)
		}

		extractor, err := factory(*llmURL, *llmModel, *llmTimeout)
		if err != nil {
			shutdownStart := time.Now()
			cleanup()
			timings.LLMShutdown = time.Since(shutdownStart)
			return fmt.Errorf("initialize llm extractor: %w", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), *llmTimeout)
		llmStart := time.Now()
		llmMatches, timings.LLMChunks, err = llm.FindMatches(ctx, extractor, input, *llmMaxChars)
		timings.LLMExtraction = time.Since(llmStart)
		cancel()
		shutdownStart := time.Now()
		cleanup()
		timings.LLMShutdown = time.Since(shutdownStart)
		if err != nil {
			return fmt.Errorf("extract llm pii: %w", err)
		}
	}

	// 3.Final rewrite: apply all detected matches to the original input, then emit output and stats.
	anonymizeStart := time.Now()
	output, result := engine.AnonymizeWithMatches(input, llmMatches)
	timings.Anonymization = time.Since(anonymizeStart)

	writeStart := time.Now()
	if _, err := stdout.Write(output); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	timings.StdoutWrite = time.Since(writeStart)
	timings.StdoutBytes = len(output)
	timings.Total = time.Since(totalStart)

	if *stats {
		writeStats(stderr, result, timings)
	}

	return nil
}

func newOllamaExtractor(baseURL, model string, timeout time.Duration) (llm.Extractor, error) {
	return llm.NewOllamaExtractor(baseURL, model, timeout)
}

type runTimings struct {
	StdinBytes        int
	StdinRead         time.Duration
	Gitleaks          detectors.ExternalLoadMetrics
	Presidio          detectors.ExternalLoadMetrics
	BuiltinDetectors  int
	ExternalDetectors int
	EngineInit        time.Duration
	LLMStartup        time.Duration
	LLMExtraction     time.Duration
	LLMChunks         int
	StdoutBytes       int
	Anonymization     time.Duration
	StdoutWrite       time.Duration
	LLMShutdown       time.Duration
	Total             time.Duration
}

func writeStats(writer io.Writer, result anonymizer.Result, timings runTimings) {
	types := make([]string, 0, len(result.Stats))
	for entityType := range result.Stats {
		types = append(types, string(entityType))
	}
	sort.Strings(types)

	for _, entityType := range types {
		typedEntity := anonymizer.EntityType(entityType)
		stats := result.Stats[typedEntity]
		fmt.Fprintf(writer, "%s count=%d\n", statName(typedEntity), stats.Count)
	}

	writeExternalLoadStats(writer, "gitleaks", timings.Gitleaks)
	writeExternalLoadStats(writer, "presidio", timings.Presidio)
	fmt.Fprintf(writer, "detectors.builtin=%d\n", timings.BuiltinDetectors)
	fmt.Fprintf(writer, "detectors.external=%d\n", timings.ExternalDetectors)
	fmt.Fprintf(writer, "stdin.bytes=%d\n", timings.StdinBytes)
	fmt.Fprintf(writer, "stdin.empty=%t\n", timings.StdinBytes == 0)
	fmt.Fprintf(writer, "time.external_rules_total=%s\n", timings.Gitleaks.Total+timings.Presidio.Total)
	fmt.Fprintf(writer, "time.stdin_read=%s\n", timings.StdinRead)
	fmt.Fprintf(writer, "time.engine_init=%s\n", timings.EngineInit)
	fmt.Fprintf(writer, "time.llm_startup=%s\n", timings.LLMStartup)
	fmt.Fprintf(writer, "time.llm_extraction=%s\n", timings.LLMExtraction)
	fmt.Fprintf(writer, "llm.chunks=%d\n", timings.LLMChunks)
	fmt.Fprintf(writer, "stdout.bytes=%d\n", timings.StdoutBytes)
	fmt.Fprintf(writer, "time.anonymization=%s\n", timings.Anonymization)
	fmt.Fprintf(writer, "time.stdout_write=%s\n", timings.StdoutWrite)
	fmt.Fprintf(writer, "time.llm_shutdown=%s\n", timings.LLMShutdown)
	fmt.Fprintf(writer, "time.total=%s\n", timings.Total)
}

func writeExternalLoadStats(writer io.Writer, prefix string, metrics detectors.ExternalLoadMetrics) {
	fmt.Fprintf(writer, "%s.cache_hits=%d\n", prefix, metrics.CacheHits)
	fmt.Fprintf(writer, "%s.cache_misses=%d\n", prefix, metrics.CacheMisses)
	fmt.Fprintf(writer, "%s.cache_fallbacks=%d\n", prefix, metrics.CacheFallbacks)
	fmt.Fprintf(writer, "%s.downloads=%d\n", prefix, metrics.Downloads)
	fmt.Fprintf(writer, "%s.files=%d\n", prefix, metrics.Files)
	fmt.Fprintf(writer, "%s.bytes=%d\n", prefix, metrics.Bytes)
	fmt.Fprintf(writer, "%s.rules=%d\n", prefix, metrics.Rules)
	fmt.Fprintf(writer, "%s.recognizers=%d\n", prefix, metrics.Recognizers)
	fmt.Fprintf(writer, "%s.patterns=%d\n", prefix, metrics.Patterns)
	fmt.Fprintf(writer, "%s.detectors=%d\n", prefix, metrics.Detectors)
	fmt.Fprintf(writer, "time.%s_cache_read=%s\n", prefix, metrics.CacheRead)
	fmt.Fprintf(writer, "time.%s_cache_write=%s\n", prefix, metrics.CacheWrite)
	fmt.Fprintf(writer, "time.%s_download=%s\n", prefix, metrics.Download)
	fmt.Fprintf(writer, "time.%s_parse=%s\n", prefix, metrics.Parse)
	fmt.Fprintf(writer, "time.%s_compile=%s\n", prefix, metrics.Compile)
	fmt.Fprintf(writer, "time.%s_total=%s\n", prefix, metrics.Total)
}

func statName(entityType anonymizer.EntityType) string {
	if isLLMEntityType(entityType) {
		return "llm." + string(entityType)
	}

	return string(entityType)
}

func isLLMEntityType(entityType anonymizer.EntityType) bool {
	switch entityType {
	case anonymizer.EntityPersonName,
		anonymizer.EntityLocation,
		anonymizer.EntityOrganization,
		anonymizer.EntityContextIdentifier,
		anonymizer.EntityOtherPII,
		anonymizer.EntityDate,
		anonymizer.EntityDocumentID,
		anonymizer.EntityVehiclePlate,
		anonymizer.EntityMedicalProvider,
		anonymizer.EntitySchool,
		anonymizer.EntityEmployer,
		anonymizer.EntityPetIdentifier:
		return true
	default:
		return false
	}
}
