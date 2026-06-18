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
	return runWithLLMDependencies(stdin, stdout, stderr, args, newOllamaExtractor, llm.EnsureOllamaServer)
}

type llmExtractorFactory func(baseURL, model string, timeout time.Duration) (llm.Extractor, error)
type llmServerEnsurer func(ctx context.Context, baseURL string, timeout time.Duration) (func(), error)

func runWithLLMFactory(stdin io.Reader, stdout, stderr io.Writer, args []string, factory llmExtractorFactory) error {
	return runWithLLMDependencies(stdin, stdout, stderr, args, factory, func(context.Context, string, time.Duration) (func(), error) {
		return func() {}, nil
	})
}

func runWithLLMDependencies(stdin io.Reader, stdout, stderr io.Writer, args []string, factory llmExtractorFactory, ensureServer llmServerEnsurer) error {
	totalStart := time.Now()
	timings := runTimings{}

	flags := flag.NewFlagSet("klovis", flag.ContinueOnError)
	flags.SetOutput(stderr)

	stats := flags.Bool("stats", false, "write anonymization statistics to stderr")
	noExtra := flags.Bool("no-extra", false, "disable extra detectors such as URL, IBAN, credit cards and MAC addresses")
	useLLM := flags.Bool("llm", false, "enable local LLM extraction through Ollama")
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

	readStart := time.Now()
	input, err := io.ReadAll(stdin)
	timings.StdinRead = time.Since(readStart)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	engine := anonymizer.New(detectors.Default(!*noExtra))
	var llmMatches []anonymizer.Match
	if *useLLM {
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

	anonymizeStart := time.Now()
	output, result := engine.AnonymizeWithMatches(input, llmMatches)
	timings.Anonymization = time.Since(anonymizeStart)

	writeStart := time.Now()
	if _, err := stdout.Write(output); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	timings.StdoutWrite = time.Since(writeStart)
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
	StdinRead     time.Duration
	LLMStartup    time.Duration
	LLMExtraction time.Duration
	LLMChunks     int
	Anonymization time.Duration
	StdoutWrite   time.Duration
	LLMShutdown   time.Duration
	Total         time.Duration
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

	fmt.Fprintf(writer, "time.stdin_read=%s\n", timings.StdinRead)
	fmt.Fprintf(writer, "time.llm_startup=%s\n", timings.LLMStartup)
	fmt.Fprintf(writer, "time.llm_extraction=%s\n", timings.LLMExtraction)
	fmt.Fprintf(writer, "llm.chunks=%d\n", timings.LLMChunks)
	fmt.Fprintf(writer, "time.anonymization=%s\n", timings.Anonymization)
	fmt.Fprintf(writer, "time.stdout_write=%s\n", timings.StdoutWrite)
	fmt.Fprintf(writer, "time.llm_shutdown=%s\n", timings.LLMShutdown)
	fmt.Fprintf(writer, "time.total=%s\n", timings.Total)
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
