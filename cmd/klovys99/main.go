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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	appconfig "github.com/Korbicorp/klovys99/internal/appconfig"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/Korbicorp/klovys99/internal/proxy"
	statlog "github.com/Korbicorp/klovys99/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	proxyAddrEnv       = "KLOVIS_ADDR"
	targetEnv          = "KLOVIS_TARGET_URL"
	anthropicTargetEnv = "KLOVIS_ANTHROPIC_TARGET_URL"
	openaiTargetEnv    = "KLOVIS_OPENAI_TARGET_URL"
	proxyDebugEnv      = "KLOVIS_PROXY_DEBUG"
	logToFileEnv       = "KLOVIS_LOG_TO_FILE"
)

const (
	defaultProxyAdr      = "127.0.0.1:8080"
	defaultDebug         = false
	defaultLogToFile     = false
	defaultStatsPath     = statlog.DefaultPath
	defaultStatsMaxBytes = statlog.DefaultMaxBytes
	defaultConfigPath    = appconfig.DefaultPath
)

//go:embed dashboard/index.html dashboard/test-tool.html dashboard/assets/*
var dashboardFiles embed.FS

var (
	dashboardIndexHTML = mustDashboardFile("dashboard/index.html")
	testToolIndexHTML  = mustDashboardFile("dashboard/test-tool.html")
	dashboardAssetsFS  = mustDashboardSubFS("dashboard/assets")
)

func main() {
	config, err := runtimeConfigFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to parse runtime configuration")
	}
	if err := run(context.Background(), config); err != nil {
		log.Fatal().Err(err).Msg("fail to start proxy")
	}
}

type runtimeConfig struct {
	Addr            string
	Target          *url.URL
	AnthropicTarget *url.URL
	OpenAITarget    *url.URL
	Logger          *zerolog.Logger
	DebugTrafficLog bool
	LogToFile       bool
	Detectors       detectors.Config
	StatsPath       string
	StatsMaxBytes   int64
	ConfigPath      string
}

type application struct {
	addr          string
	handler       http.Handler
	logger        *zerolog.Logger
	logFile       *os.File
	statsRecorder *statlog.Recorder
	configStore   *appconfig.Store
}

type anonymizationPreviewer interface {
	Preview(input string) anonymizer.PreviewResult
}

func run(ctx context.Context, config runtimeConfig) error {
	app, err := buildApplication(ctx, config)
	if err != nil {
		return err
	}
	defer app.Close()

	defaultTarget := proxy.DefaultAnthropicURL
	if config.Target != nil {
		defaultTarget = config.Target.String()
	}
	app.logger.Info().
		Str("addr", app.addr).
		Str("dashboard_url", dashboardURLFromAddr(app.addr)).
		Str("default_target", defaultTarget).
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
	if config.AnthropicTarget == nil {
		target, err := url.Parse(proxy.DefaultAnthropicURL)
		if err != nil {
			return nil, fmt.Errorf("parse anthropic URL: %w", err)
		}
		config.AnthropicTarget = target
	}
	if config.OpenAITarget == nil {
		target, err := url.Parse(proxy.DefaultOpenAIURL)
		if err != nil {
			return nil, fmt.Errorf("parse openai URL: %w", err)
		}
		config.OpenAITarget = target
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

	anonymizerService := anonymizer.NewServiceWithProtectionPolicy(detectorResult.Detectors, configStore)
	proxyHandler, err := proxy.NewProxyHandler(proxy.Config{
		Target: config.Target,
		RouteTargets: map[string]*url.URL{
			proxy.AnthropicRoutePrefix: config.AnthropicTarget,
			proxy.OpenAIRoutePrefix:    config.OpenAITarget,
		},
		Logger:        config.Logger,
		Anonymizer:    anonymizerService,
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		closeLogFile(logFile)
		return nil, err
	}

	handler := newHTTPHandler(proxyHandler, statsRecorder, configStore, anonymizerService)
	return &application{
		addr:          config.Addr,
		handler:       handler,
		logger:        config.Logger,
		logFile:       logFile,
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

type anonymizationTestRequest struct {
	Text string `json:"text"`
}

type anonymizationTypeCount struct {
	Type  anonymizer.EntityType `json:"type"`
	Count int                   `json:"count"`
}

type anonymizationTestResponse struct {
	OriginalText   string                   `json:"original_text"`
	AnonymizedText string                   `json:"anonymized_text"`
	Findings       []anonymizer.Finding     `json:"findings"`
	CountsByType   []anonymizationTypeCount `json:"counts_by_type"`
}

func newHTTPHandler(proxyHandler gin.HandlerFunc, statsStore statsStore, configStore appConfigStore, previewer anonymizationPreviewer) http.Handler {
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
	if previewer != nil {
		router.POST("/api/anonymization/test", func(ctx *gin.Context) {
			request, err := decodeAnonymizationTestRequest(ctx.Request)
			if err != nil {
				ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			preview := previewer.Preview(request.Text)
			ctx.JSON(http.StatusOK, anonymizationTestResponse{
				OriginalText:   request.Text,
				AnonymizedText: preview.Anonymized,
				Findings:       preview.Findings,
				CountsByType:   previewCounts(preview.Stats),
			})
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
	router.GET("/dashboard/test-tool", serveTestToolIndex)
	router.GET("/dashboard/test-tool/", serveTestToolIndex)
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

func decodeAnonymizationTestRequest(request *http.Request) (anonymizationTestRequest, error) {
	defer request.Body.Close()
	var payload anonymizationTestRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return anonymizationTestRequest{}, fmt.Errorf("decode anonymization test request: %w", err)
	}
	return payload, nil
}

func previewCounts(stats map[anonymizer.EntityType]anonymizer.EntityStats) []anonymizationTypeCount {
	if len(stats) == 0 {
		return nil
	}
	entityTypes := make([]anonymizer.EntityType, 0, len(stats))
	for entityType, entityStats := range stats {
		if entityStats.Count <= 0 {
			continue
		}
		entityTypes = append(entityTypes, entityType)
	}
	sort.Slice(entityTypes, func(i, j int) bool {
		return entityTypes[i] < entityTypes[j]
	})

	counts := make([]anonymizationTypeCount, 0, len(entityTypes))
	for _, entityType := range entityTypes {
		counts = append(counts, anonymizationTypeCount{
			Type:  entityType,
			Count: stats[entityType].Count,
		})
	}
	return counts
}

func serveDashboardIndex(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	ctx.Data(http.StatusOK, "text/html; charset=utf-8", dashboardIndexHTML)
}

func serveTestToolIndex(ctx *gin.Context) {
	ctx.Header("Cache-Control", "no-store")
	ctx.Data(http.StatusOK, "text/html; charset=utf-8", testToolIndexHTML)
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
	if a.logFile != nil {
		closeLogFile(a.logFile)
	}
}

func closeLogFile(logFile *os.File) {
	if logFile != nil {
		_ = logFile.Close()
	}
}

func runtimeConfigFromEnv() (runtimeConfig, error) {
	target, err := envURLWithDefault(targetEnv, proxy.DefaultAnthropicURL)
	if err != nil {
		return runtimeConfig{}, err
	}
	anthropicTarget, err := envURLWithDefault(anthropicTargetEnv, proxy.DefaultAnthropicURL)
	if err != nil {
		return runtimeConfig{}, err
	}
	openaiTarget, err := envURLWithDefault(openaiTargetEnv, proxy.DefaultOpenAIURL)
	if err != nil {
		return runtimeConfig{}, err
	}
	debugTrafficLog, err := envBoolWithDefault(proxyDebugEnv, defaultDebug)
	if err != nil {
		return runtimeConfig{}, err
	}
	logToFile, err := envBoolWithDefault(logToFileEnv, defaultLogToFile)
	if err != nil {
		return runtimeConfig{}, err
	}
	return runtimeConfig{
		Addr:            envStringWithDefault(proxyAddrEnv, defaultProxyAdr),
		Target:          target,
		AnthropicTarget: anthropicTarget,
		OpenAITarget:    openaiTarget,
		DebugTrafficLog: debugTrafficLog,
		LogToFile:       logToFile,
		Detectors:       detectors.DefaultConfig(),
		StatsPath:       defaultStatsPath,
		StatsMaxBytes:   defaultStatsMaxBytes,
		ConfigPath:      defaultConfigPath,
	}, nil
}

func envURLWithDefault(name, def string) (*url.URL, error) {
	value := envStringWithDefault(name, def)
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("parse %s: value must include scheme and host", name)
	}
	return parsed, nil
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
