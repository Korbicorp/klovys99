package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
	"github.com/Korbicorp/klovis/internal/llm"
	"github.com/Korbicorp/klovis/internal/proxy"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	proxyDebugEnv   = "KLOVIS_PROXY_DEBUG"
	logToFileEnv    = "KLOVIS_LOG_TO_FILE"
	llmEnabledEnv   = "KLOVIS_LLM_ENABLED"
	llmURLEnv       = "KLOVIS_LLM_URL"
	llmModelEnv     = "KLOVIS_LLM_MODEL"
	llmTimeoutEnv   = "KLOVIS_LLM_TIMEOUT"
	llmMaxCharsEnv  = "KLOVIS_LLM_MAX_CHARS"
	llmAutoStartEnv = "KLOVIS_LLM_AUTOSTART"
)

const (
	defaultProxyAdr         = ":8080"
	defaultLLMEnable        = false
	defaultLLMAutoStart     = false
	defaultDebug            = false
	defaultLogToFile        = false
	defaultLLMTimeout       = llm.DefaultTimeout
	defaultLLMMaxChunkBytes = llm.DefaultMaxChunkBytes
	defaultLLMBaseUrl       = llm.DefaultBaseURL
	defaultLLMModel         = llm.DefaultModel
)

func main() {
	config, err := runtimeConfigFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to parse runtime configuration")
	}
	if err := run(context.Background(), config); err != nil {
		log.Fatal().Err(err).Msg("fail to start Anthropic proxy")
	}
}

type runtimeConfig struct {
	Addr             string
	Target           *url.URL
	Logger           *zerolog.Logger
	DebugTrafficLog  bool
	LogToFile        bool
	Detectors        detectors.Config
	LLMEnabled       bool
	LLMBaseURL       string
	LLMModel         string
	LLMTimeout       time.Duration
	LLMMaxChunkBytes int
	LLMAutoStart     bool
}

type application struct {
	addr       string
	handler    http.Handler
	logger     *zerolog.Logger
	logFile    *os.File
	llmService *llm.Service
}

func run(ctx context.Context, config runtimeConfig) error {
	app, err := buildApplication(ctx, config)
	if err != nil {
		return err
	}
	defer app.Close()

	app.logger.Info().Msg("proxy starting")
	return http.ListenAndServe(app.addr, app.handler)
}

func buildApplication(ctx context.Context, config runtimeConfig) (*application, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(config.Addr) == "" {
		config.Addr = defaultProxyAdr
	}
	if config.Target == nil {
		target, err := url.Parse(proxy.DefaultAnthropicURL)
		if err != nil {
			return nil, fmt.Errorf("parse anthropic URL: %w", err)
		}
		config.Target = target
	}
	var logFile *os.File
	if config.Logger == nil {
		logger, openedLogFile, err := runtimeLogger(config.DebugTrafficLog, config.LogToFile)
		if err != nil {
			return nil, err
		}
		config.Logger = &logger
		log.Logger = logger
		logFile = openedLogFile
	}

	detectorService := detectors.NewService(config.Detectors)
	detectorResult, err := detectorService.Load(ctx)
	if err != nil {
		closeLogFile(logFile)
		return nil, err
	}
	logExternalLoadStats(config.Logger, "betterleaks", detectorResult.Gitleaks)
	logExternalLoadStats(config.Logger, "presidio", detectorResult.Presidio)
	config.Logger.Info().Int("detectors", len(detectorResult.Detectors)).Msg("proxy detectors loaded")

	var matchFinder proxy.MatchFinder
	var llmService *llm.Service
	if config.LLMEnabled {
		llmConfig := llmConfigFromRuntime(config)
		service, err := llm.NewService(ctx, llmConfig)
		if err != nil {
			closeLogFile(logFile)
			return nil, err
		}
		llmService = service
		matchFinder = service
		config.Logger.Info().
			Str("url", llmConfig.BaseURL).
			Str("model", llmConfig.Model).
			Dur("timeout", llmConfig.Timeout).
			Int("max_chunk_bytes", llmConfig.MaxChunkBytes).
			Bool("autostart", llmConfig.AutoStart).
			Msg("llm enabled")
	}

	proxyHandler, err := proxy.NewProxyHandler(proxy.Config{
		Target:      config.Target,
		Logger:      config.Logger,
		Anonymizer:  anonymizer.NewService(detectorResult.Detectors),
		MatchFinder: matchFinder,
	})
	if err != nil {
		if llmService != nil {
			llmService.Close()
		}
		closeLogFile(logFile)
		return nil, err
	}

	handler := newHTTPHandler(proxyHandler)
	return &application{
		addr:       config.Addr,
		handler:    handler,
		logger:     config.Logger,
		logFile:    logFile,
		llmService: llmService,
	}, nil
}

func newHTTPHandler(proxyHandler gin.HandlerFunc) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Any("/*proxyPath", proxyHandler)
	return router
}

func (a *application) Close() {
	if a == nil {
		return
	}
	if a.llmService != nil {
		a.llmService.Close()
	}
	if a.logFile != nil {
		closeLogFile(a.logFile)
	}
}

func closeLogFile(logFile *os.File) {
	if logFile != nil {
		_ = logFile.Close()
	}
}

func llmConfigFromRuntime(config runtimeConfig) llm.Config {
	llmConfig := llm.Config{
		BaseURL:       strings.TrimSpace(config.LLMBaseURL),
		Model:         strings.TrimSpace(config.LLMModel),
		Timeout:       config.LLMTimeout,
		MaxChunkBytes: config.LLMMaxChunkBytes,
		AutoStart:     config.LLMAutoStart,
	}
	if llmConfig.BaseURL == "" {
		llmConfig.BaseURL = defaultLLMBaseUrl
	}
	if llmConfig.Model == "" {
		llmConfig.Model = defaultLLMModel
	}
	if llmConfig.Timeout <= 0 {
		llmConfig.Timeout = defaultLLMTimeout
	}
	if llmConfig.MaxChunkBytes <= 0 {
		llmConfig.MaxChunkBytes = defaultLLMMaxChunkBytes
	}
	return llmConfig
}

func runtimeConfigFromEnv() (runtimeConfig, error) {
	target, err := url.Parse(proxy.DefaultAnthropicURL)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("parse DefaultAnthropicURL: %w", err)
	}
	debugTrafficLog, err := envBoolWithDefault(proxyDebugEnv, defaultDebug)
	if err != nil {
		return runtimeConfig{}, err
	}
	logToFile, err := envBoolWithDefault(logToFileEnv, defaultLogToFile)
	if err != nil {
		return runtimeConfig{}, err
	}
	llmEnabled, err := envBoolWithDefault(llmEnabledEnv, defaultLLMEnable)
	if err != nil {
		return runtimeConfig{}, err
	}
	llmTimeout, err := envDurationWithDefault(llmTimeoutEnv, defaultLLMTimeout)
	if err != nil {
		return runtimeConfig{}, err
	}
	llmMaxChunkBytes, err := envIntWithDefault(llmMaxCharsEnv, defaultLLMMaxChunkBytes)
	if err != nil {
		return runtimeConfig{}, err
	}
	llmAutoStart, err := envBoolWithDefault(llmAutoStartEnv, defaultLLMAutoStart)
	if err != nil {
		return runtimeConfig{}, err
	}

	return runtimeConfig{
		Addr:             defaultProxyAdr,
		Target:           target,
		DebugTrafficLog:  debugTrafficLog,
		LogToFile:        logToFile,
		Detectors:        detectors.DefaultConfig(),
		LLMEnabled:       llmEnabled,
		LLMBaseURL:       envStringWithDefault(llmURLEnv, defaultLLMBaseUrl),
		LLMModel:         envStringWithDefault(llmModelEnv, defaultLLMModel),
		LLMTimeout:       llmTimeout,
		LLMMaxChunkBytes: llmMaxChunkBytes,
		LLMAutoStart:     llmAutoStart,
	}, nil
}

func runtimeLogger(debugTraffic, logToFile bool) (zerolog.Logger, *os.File, error) {
	level := zerolog.InfoLevel
	if debugTraffic {
		level = zerolog.DebugLevel
	}

	if !logToFile {
		return zerolog.New(os.Stdout).Level(level).With().Timestamp().Logger(), nil, nil
	}

	logFile, err := os.OpenFile(proxy.DefaultLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return zerolog.Logger{}, nil, fmt.Errorf("open proxy log file: %w", err)
	}
	logger := zerolog.New(logFile).Level(level).With().Timestamp().Logger()
	return logger, logFile, nil
}

func logExternalLoadStats(logger *zerolog.Logger, prefix string, metrics detectors.ExternalLoadMetrics) {
	logger.Info().
		Str("source", prefix).
		Int("cache_hits", metrics.CacheHits).
		Int("cache_misses", metrics.CacheMisses).
		Int("cache_fallbacks", metrics.CacheFallbacks).
		Int("downloads", metrics.Downloads).
		Int("files", metrics.Files).
		Int("bytes", metrics.Bytes).
		Int("rules", metrics.Rules).
		Int("recognizers", metrics.Recognizers).
		Int("patterns", metrics.Patterns).
		Int("detectors", metrics.Detectors).
		Dur("cache_read", metrics.CacheRead).
		Dur("cache_write", metrics.CacheWrite).
		Dur("download", metrics.Download).
		Dur("parse", metrics.Parse).
		Dur("compile", metrics.Compile).
		Dur("total", metrics.Total).
		Msg("external detectors loaded")
}

func envBoolWithDefault(name string, def bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return def, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("parse %s: value must be true or false", name)
	}
}

func envStringWithDefault(name, def string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return def
	}
	return value
}

func envDurationWithDefault(name string, def time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return def, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func envIntWithDefault(name string, def int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("parse %s: value must be greater than zero", name)
	}
	return parsed, nil
}
