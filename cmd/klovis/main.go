package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	appconfig "github.com/Korbicorp/klovis/internal/appconfig"
	"github.com/Korbicorp/klovis/internal/detectors"
	"github.com/Korbicorp/klovis/internal/llm"
	"github.com/Korbicorp/klovis/internal/proxy"
	statlog "github.com/Korbicorp/klovis/internal/stats"
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
	defaultStatsPath        = statlog.DefaultPath
	defaultStatsMaxBytes    = statlog.DefaultMaxBytes
	defaultConfigPath       = appconfig.DefaultPath
)

//go:embed dashboard/index.html dashboard/assets/*
var dashboardFiles embed.FS

var (
	dashboardIndexHTML = mustDashboardFile("dashboard/index.html")
	dashboardAssetsFS  = mustDashboardSubFS("dashboard/assets")
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
	StatsPath        string
	StatsMaxBytes    int64
	ConfigPath       string
}

type application struct {
	addr          string
	handler       http.Handler
	logger        *zerolog.Logger
	logFile       *os.File
	llmService    *llm.Service
	statsRecorder *statlog.Recorder
	configStore   *appconfig.Store
}

func run(ctx context.Context, config runtimeConfig) error {
	app, err := buildApplication(ctx, config)
	if err != nil {
		return err
	}
	defer app.Close()

	app.logger.Info().
		Str("addr", app.addr).
		Str("dashboard_url", dashboardURLFromAddr(app.addr)).
		Msg("proxy starting")
	return http.ListenAndServe(app.addr, app.handler)
}

// dashboardURLFromAddr converts the listen address into a local dashboard URL.
func dashboardURLFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultProxyAdr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			host = "localhost"
			port = strings.TrimPrefix(addr, ":")
		} else if !strings.Contains(addr, ":") {
			host = "localhost"
			port = addr
		} else {
			return "http://" + addr + "/dashboard"
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s/dashboard", host, port)
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

	statsPath := strings.TrimSpace(config.StatsPath)
	if statsPath == "" {
		statsPath = defaultStatsPath
	}
	statsMaxBytes := config.StatsMaxBytes
	if statsMaxBytes <= 0 {
		statsMaxBytes = defaultStatsMaxBytes
	}
	statsRecorder, err := statlog.NewRecorder(statlog.Config{
		Path:     statsPath,
		MaxBytes: statsMaxBytes,
	})
	if err != nil {
		closeLogFile(logFile)
		return nil, err
	}

	configPath := strings.TrimSpace(config.ConfigPath)
	if configPath == "" {
		configPath = defaultConfigPath
	}
	configStore, err := appconfig.NewStore(configPath, anonymizer.KnownEntityTypes())
	if err != nil {
		closeLogFile(logFile)
		return nil, err
	}

	detectorService := detectors.NewService(config.Detectors)
	detectorResult, err := detectorService.Load(ctx)
	if err != nil {
		closeLogFile(logFile)
		return nil, err
	}
	logExternalLoadStats(config.Logger, "gitleaks", detectorResult.Gitleaks)
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
		Target:        config.Target,
		Logger:        config.Logger,
		Anonymizer:    anonymizer.NewServiceWithProtectionPolicy(detectorResult.Detectors, configStore),
		MatchFinder:   matchFinder,
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		if llmService != nil {
			llmService.Close()
		}
		closeLogFile(logFile)
		return nil, err
	}

	handler := newHTTPHandler(proxyHandler, statsRecorder, configStore)
	return &application{
		addr:          config.Addr,
		handler:       handler,
		logger:        config.Logger,
		logFile:       logFile,
		llmService:    llmService,
		statsRecorder: statsRecorder,
		configStore:   configStore,
	}, nil
}

type statsStore interface {
	Summary() (statlog.Summary, error)
	Reset() error
}

type appConfigStore interface {
	ProtectionOptions() []appconfig.ProtectionOption
	UpdateProtectionOptions(options []appconfig.ProtectionOption) ([]appconfig.ProtectionOption, error)
}

type configAPIResponse struct {
	ProtectionOptions []appconfig.ProtectionOption `json:"protection_options"`
}

func newHTTPHandler(proxyHandler gin.HandlerFunc, statsStore statsStore, configStore appConfigStore) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	registerDashboardRoutes(router)
	if statsStore != nil {
		router.GET("/api/stats", func(ctx *gin.Context) {
			summary, err := statsStore.Summary()
			if err != nil {
				ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			ctx.JSON(http.StatusOK, summary)
		})
		router.POST("/api/stats/reset", func(ctx *gin.Context) {
			if err := statsStore.Reset(); err != nil {
				ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			ctx.JSON(http.StatusOK, gin.H{"reset": true})
		})
	}
	if configStore != nil {
		router.GET("/api/config", func(ctx *gin.Context) {
			ctx.JSON(http.StatusOK, configResponse(configStore))
		})
		router.PUT("/api/config", func(ctx *gin.Context) {
			options, err := decodeConfigRequest(ctx.Request)
			if err != nil {
				ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			updated, err := configStore.UpdateProtectionOptions(options)
			if err != nil {
				ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			ctx.JSON(http.StatusOK, configAPIResponse{ProtectionOptions: updated})
		})
	}
	router.NoRoute(func(ctx *gin.Context) {
		path := ctx.Request.URL.Path
		if path == "/api" || strings.HasPrefix(path, "/api/") {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if path == "/dashboard" || strings.HasPrefix(path, "/dashboard/") {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		proxyHandler(ctx)
	})
	return router
}

func registerDashboardRoutes(router *gin.Engine) {
	router.GET("/dashboard", serveDashboardIndex)
	router.GET("/dashboard/", serveDashboardIndex)
	router.StaticFS("/dashboard/assets", dashboardAssetsFS)
}

// configResponse builds the dashboard/API view of the current app config.
func configResponse(configStore appConfigStore) configAPIResponse {
	return configAPIResponse{ProtectionOptions: configStore.ProtectionOptions()}
}

// decodeConfigRequest parses a dashboard config update request.
func decodeConfigRequest(request *http.Request) ([]appconfig.ProtectionOption, error) {
	defer request.Body.Close()
	var payload configAPIResponse
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode config request: %w", err)
	}
	if payload.ProtectionOptions == nil {
		return nil, fmt.Errorf("protection_options is required")
	}
	return payload.ProtectionOptions, nil
}

func serveDashboardIndex(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	ctx.Data(http.StatusOK, "text/html; charset=utf-8", dashboardIndexHTML)
}

func mustDashboardFile(path string) []byte {
	content, err := dashboardFiles.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("load embedded dashboard file %q: %v", path, err))
	}
	return content
}

func mustDashboardSubFS(dir string) http.FileSystem {
	subFS, err := fs.Sub(dashboardFiles, dir)
	if err != nil {
		panic(fmt.Sprintf("load embedded dashboard filesystem %q: %v", dir, err))
	}
	return http.FS(subFS)
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
		StatsPath:        defaultStatsPath,
		StatsMaxBytes:    defaultStatsMaxBytes,
		ConfigPath:       defaultConfigPath,
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
