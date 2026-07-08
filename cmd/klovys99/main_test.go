package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	appconfig "github.com/Korbicorp/klovys99/internal/appconfig"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/Korbicorp/klovys99/internal/proxy"
	statlog "github.com/Korbicorp/klovys99/internal/stats"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestEnvBoolWithDefault(t *testing.T) {
	tests := []struct {
		name      string
		envValue  *string
		def       bool
		want      bool
		wantError string
	}{
		{name: "unset uses false default", def: false, want: false},
		{name: "unset uses true default", def: true, want: true},
		{name: "empty uses default", envValue: stringPtr(""), def: true, want: true},
		{name: "spaces use default", envValue: stringPtr("   "), def: false, want: false},
		{name: "true enables", envValue: stringPtr("true"), def: false, want: true},
		{name: "false disables", envValue: stringPtr("false"), def: true, want: false},
		{name: "trimmed true enables", envValue: stringPtr(" true "), def: false, want: true},
		{name: "trimmed false disables", envValue: stringPtr(" false "), def: true, want: false},
		{name: "one rejected", envValue: stringPtr("1"), def: false, wantError: "value must be true or false"},
		{name: "zero rejected", envValue: stringPtr("0"), def: false, wantError: "value must be true or false"},
		{name: "yes rejected", envValue: stringPtr("yes"), def: false, wantError: "value must be true or false"},
		{name: "on rejected", envValue: stringPtr("on"), def: false, wantError: "value must be true or false"},
		{name: "uppercase true rejected", envValue: stringPtr("TRUE"), def: false, wantError: "value must be true or false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envName = "KLOVIS_TEST_BOOL"
			setEnv(t, envName, tt.envValue)

			got, err := envBoolWithDefault(envName, tt.def)
			assertErrorContains(t, err, tt.wantError)
			if tt.wantError == "" && got != tt.want {
				t.Fatalf("envBoolWithDefault() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestEnvStringWithDefault(t *testing.T) {
	tests := []struct {
		name     string
		envValue *string
		def      string
		want     string
	}{
		{name: "unset uses default", def: "fallback", want: "fallback"},
		{name: "empty uses default", envValue: stringPtr(""), def: "fallback", want: "fallback"},
		{name: "spaces use default", envValue: stringPtr("   "), def: "fallback", want: "fallback"},
		{name: "value is returned", envValue: stringPtr("mistral"), def: "fallback", want: "mistral"},
		{name: "value is trimmed", envValue: stringPtr("  mistral  "), def: "fallback", want: "mistral"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envName = "KLOVIS_TEST_STRING"
			setEnv(t, envName, tt.envValue)

			got := envStringWithDefault(envName, tt.def)
			if got != tt.want {
				t.Fatalf("envStringWithDefault() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvDurationWithDefault(t *testing.T) {
	tests := []struct {
		name      string
		envValue  *string
		def       time.Duration
		want      time.Duration
		wantError string
	}{
		{name: "unset uses default", def: 30 * time.Second, want: 30 * time.Second},
		{name: "empty uses default", envValue: stringPtr(""), def: time.Minute, want: time.Minute},
		{name: "spaces use default", envValue: stringPtr("   "), def: time.Minute, want: time.Minute},
		{name: "seconds parsed", envValue: stringPtr("5s"), def: time.Minute, want: 5 * time.Second},
		{name: "trimmed duration parsed", envValue: stringPtr(" 250ms "), def: time.Minute, want: 250 * time.Millisecond},
		{name: "compound duration parsed", envValue: stringPtr("1m30s"), def: time.Second, want: 90 * time.Second},
		{name: "invalid duration rejected", envValue: stringPtr("soon"), def: time.Second, wantError: "parse KLOVIS_TEST_DURATION"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envName = "KLOVIS_TEST_DURATION"
			setEnv(t, envName, tt.envValue)

			got, err := envDurationWithDefault(envName, tt.def)
			assertErrorContains(t, err, tt.wantError)
			if tt.wantError == "" && got != tt.want {
				t.Fatalf("envDurationWithDefault() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEnvIntWithDefault(t *testing.T) {
	tests := []struct {
		name      string
		envValue  *string
		def       int
		want      int
		wantError string
	}{
		{name: "unset uses default", def: 1000, want: 1000},
		{name: "empty uses default", envValue: stringPtr(""), def: 1000, want: 1000},
		{name: "spaces use default", envValue: stringPtr("   "), def: 1000, want: 1000},
		{name: "integer parsed", envValue: stringPtr("64"), def: 1000, want: 64},
		{name: "trimmed integer parsed", envValue: stringPtr(" 64 "), def: 1000, want: 64},
		{name: "zero rejected", envValue: stringPtr("0"), def: 1000, wantError: "value must be greater than zero"},
		{name: "negative rejected", envValue: stringPtr("-1"), def: 1000, wantError: "value must be greater than zero"},
		{name: "invalid integer rejected", envValue: stringPtr("large"), def: 1000, wantError: "parse KLOVIS_TEST_INT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envName = "KLOVIS_TEST_INT"
			setEnv(t, envName, tt.envValue)

			got, err := envIntWithDefault(envName, tt.def)
			assertErrorContains(t, err, tt.wantError)
			if tt.wantError == "" && got != tt.want {
				t.Fatalf("envIntWithDefault() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEnvURLWithDefault(t *testing.T) {
	tests := []struct {
		name      string
		envValue  *string
		def       string
		want      string
		wantError string
	}{
		{name: "unset uses default", def: "https://api.anthropic.com", want: "https://api.anthropic.com"},
		{name: "value is returned", envValue: stringPtr("https://api.openai.com"), def: "https://api.anthropic.com", want: "https://api.openai.com"},
		{name: "trimmed value is returned", envValue: stringPtr(" https://example.com/base "), def: "https://api.anthropic.com", want: "https://example.com/base"},
		{name: "missing host rejected", envValue: stringPtr("https:///missing-host"), def: "https://api.anthropic.com", wantError: "value must include scheme and host"},
		{name: "missing scheme rejected", envValue: stringPtr("localhost:8080"), def: "https://api.anthropic.com", wantError: "value must include scheme and host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const envName = "KLOVIS_TEST_URL"
			setEnv(t, envName, tt.envValue)

			got, err := envURLWithDefault(envName, tt.def)
			assertErrorContains(t, err, tt.wantError)
			if tt.wantError == "" && got.String() != tt.want {
				t.Fatalf("envURLWithDefault() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestRuntimeConfigFromEnv(t *testing.T) {
	t.Setenv(proxyAddrEnv, "127.0.0.1:8181")
	t.Setenv(targetEnv, "https://gateway.example.com")
	t.Setenv(anthropicTargetEnv, "https://api.anthropic.com")
	t.Setenv(openaiTargetEnv, "https://api.openai.com")
	t.Setenv(proxyDebugEnv, "true")
	t.Setenv(logToFileEnv, "true")

	config, err := runtimeConfigFromEnv()
	if err != nil {
		t.Fatalf("runtimeConfigFromEnv returned error: %v", err)
	}

	if !config.DebugTrafficLog {
		t.Fatal("DebugTrafficLog = false, want true")
	}
	if !config.LogToFile {
		t.Fatal("LogToFile = false, want true")
	}
	if config.Addr != "127.0.0.1:8181" {
		t.Fatalf("Addr = %q, want custom addr", config.Addr)
	}
	if config.Target.String() != "https://gateway.example.com" {
		t.Fatalf("Target = %q, want custom target", config.Target.String())
	}
	if config.AnthropicTarget.String() != "https://api.anthropic.com" {
		t.Fatalf("AnthropicTarget = %q, want anthropic target", config.AnthropicTarget.String())
	}
	if config.OpenAITarget.String() != "https://api.openai.com" {
		t.Fatalf("OpenAITarget = %q, want openai target", config.OpenAITarget.String())
	}
	if config.StatsPath != statlog.DefaultPath {
		t.Fatalf("StatsPath = %q, want default stats path", config.StatsPath)
	}
	if config.StatsMaxBytes != statlog.DefaultMaxBytes {
		t.Fatalf("StatsMaxBytes = %d, want default stats max bytes", config.StatsMaxBytes)
	}
	if config.ConfigPath != appconfig.DefaultPath {
		t.Fatalf("ConfigPath = %q, want default config path", config.ConfigPath)
	}
}

func TestDashboardURLFromAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "default empty address", addr: "", want: "http://127.0.0.1:8080/dashboard"},
		{name: "port only listen address", addr: ":8080", want: "http://localhost:8080/dashboard"},
		{name: "all interfaces", addr: "0.0.0.0:9090", want: "http://localhost:9090/dashboard"},
		{name: "loopback address", addr: "127.0.0.1:9090", want: "http://127.0.0.1:9090/dashboard"},
		{name: "localhost address", addr: "localhost:9090", want: "http://localhost:9090/dashboard"},
		{name: "ipv6 loopback", addr: "[::1]:9090", want: "http://[::1]:9090/dashboard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dashboardURLFromAddr(tt.addr); got != tt.want {
				t.Fatalf("dashboardURLFromAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestRuntimeLoggerDefaultsToStdoutWithoutProxyLog(t *testing.T) {
	t.Chdir(t.TempDir())

	logger, closeLog, err := runtimeLogger(false, false)
	if err != nil {
		t.Fatalf("runtime logger: %v", err)
	}
	if closeLog != nil {
		t.Fatal("closeLog is not nil, want stdout logger")
	}
	logger.Info().Str("event", "startup").Msg("runtime ready")

	if _, err := os.Stat(proxy.DefaultLogPath); !os.IsNotExist(err) {
		t.Fatalf("proxy log stat error = %v, want not exist", err)
	}
}

func TestRuntimeLoggerWritesInfoLogsToProxyLog(t *testing.T) {
	t.Chdir(t.TempDir())

	logger, closeLog, err := runtimeLogger(false, true)
	if err != nil {
		t.Fatalf("runtime logger: %v", err)
	}
	if closeLog == nil {
		t.Fatal("closeLog is nil, want file logger")
	}
	logger.Info().Str("event", "startup").Msg("runtime ready")
	logger.Debug().Str("body", "{}").Msg("traffic body")
	if err := closeLog.Close(); err != nil {
		t.Fatalf("close runtime logger: %v", err)
	}

	logContent, err := os.ReadFile(proxy.DefaultLogPath)
	if err != nil {
		t.Fatalf("read proxy log: %v", err)
	}
	if got := string(logContent); !strings.Contains(got, `"level":"info"`) || !strings.Contains(got, `"event":"startup"`) {
		t.Fatalf("proxy log = %q, want info event", got)
	}
	if got := string(logContent); strings.Contains(got, `"level":"debug"`) || strings.Contains(got, `"body":"{}"`) {
		t.Fatalf("proxy log = %q, want no debug traffic event without debug mode", got)
	}
}

func TestRuntimeLoggerDebugWritesProxyLog(t *testing.T) {
	t.Chdir(t.TempDir())

	logger, closeLog, err := runtimeLogger(true, true)
	if err != nil {
		t.Fatalf("runtime logger: %v", err)
	}
	logger.Debug().Str("body", "{}").Msg("traffic body")
	if closeLog == nil {
		t.Fatal("closeLog is nil, want file logger")
	}
	if err := closeLog.Close(); err != nil {
		t.Fatalf("close runtime logger: %v", err)
	}

	logContent, err := os.ReadFile(proxy.DefaultLogPath)
	if err != nil {
		t.Fatalf("read proxy log: %v", err)
	}
	if got := string(logContent); !strings.Contains(got, `"level":"debug"`) || !strings.Contains(got, `"body":"{}"`) {
		t.Fatalf("proxy log = %q, want debug traffic event", got)
	}
}

func TestBuildApplicationSetsGlobalLoggerToRuntimeFile(t *testing.T) {
	t.Chdir(t.TempDir())

	previousLogger := log.Logger
	t.Cleanup(func() {
		log.Logger = previousLogger
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	app, err := buildApplication(context.Background(), runtimeConfig{
		Target:     mustParseURL(t, upstream.URL),
		LogToFile:  true,
		Detectors:  noExternalDetectorsConfig(),
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}

	log.Info().Str("source", "global").Msg("global log routed")
	app.Close()

	logContent, err := os.ReadFile(proxy.DefaultLogPath)
	if err != nil {
		t.Fatalf("read proxy log: %v", err)
	}
	if got := string(logContent); !strings.Contains(got, `"source":"global"`) || !strings.Contains(got, `"message":"global log routed"`) {
		t.Fatalf("proxy log = %q, want global log event", got)
	}
}

func TestBuildApplicationComposesServices(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs strings.Builder
	logger := zerolog.New(&logs)

	app, err := buildApplication(context.Background(), runtimeConfig{
		Addr:       ":9090",
		Target:     mustParseURL(t, upstream.URL),
		Logger:     &logger,
		Detectors:  noExternalDetectorsConfig(),
		StatsPath:  t.TempDir() + "/stats.jsonl",
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}
	defer app.Close()

	if app.addr != ":9090" {
		t.Fatalf("addr = %q, want :9090", app.addr)
	}
	if app.handler == nil {
		t.Fatalf("application dependencies not built: %#v", app)
	}

	proxyServer := httptest.NewServer(app.handler)
	defer proxyServer.Close()

	response, err := proxyServer.Client().Post(proxyServer.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want 200", response.StatusCode)
	}
	if strings.Contains(upstreamBody, "alice@example.com") || !strings.Contains(upstreamBody, "Email [EMAIL_1]") {
		t.Fatalf("upstream body = %q, want anonymized body", upstreamBody)
	}
}

// TestBuildApplicationServesStatsAPIBeforeProxy verifies that dashboard API routes are handled locally before proxy fallback.
func TestBuildApplicationServesStatsAPIBeforeProxy(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamHits++
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	app, err := buildApplication(context.Background(), runtimeConfig{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     ptrLogger(zerolog.Nop()),
		Detectors:  noExternalDetectorsConfig(),
		StatsPath:  t.TempDir() + "/stats.jsonl",
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}
	defer app.Close()

	if err := app.statsRecorder.Record(statlog.Event{
		Event:  statlog.EventRequestProcessed,
		Counts: map[string]int{"EMAIL": 2},
	}); err != nil {
		t.Fatalf("record stats event: %v", err)
	}

	server := httptest.NewServer(app.handler)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatalf("call stats API: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", response.StatusCode)
	}
	var summary statlog.Summary
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		t.Fatalf("decode stats response: %v", err)
	}
	if summary.TotalRequests != 1 || summary.AnonymizedRequests != 1 || len(summary.CountsByType) != 1 || summary.CountsByType[0].Count != 2 {
		t.Fatalf("summary = %#v, want one anonymized EMAIL request", summary)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want stats API not proxied", upstreamHits)
	}

	missingAPIResponse, err := server.Client().Get(server.URL + "/api/unknown")
	if err != nil {
		t.Fatalf("call missing API route: %v", err)
	}
	defer missingAPIResponse.Body.Close()
	if missingAPIResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("missing API status = %d, want 404", missingAPIResponse.StatusCode)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want missing API route not proxied", upstreamHits)
	}

	resetResponse, err := server.Client().Post(server.URL+"/api/stats/reset", "application/json", nil)
	if err != nil {
		t.Fatalf("call stats reset API: %v", err)
	}
	defer resetResponse.Body.Close()
	if resetResponse.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", resetResponse.StatusCode)
	}
	summaryAfterReset, err := app.statsRecorder.Summary()
	if err != nil {
		t.Fatalf("summary after reset: %v", err)
	}
	if summaryAfterReset.TotalRequests != 0 {
		t.Fatalf("summary after reset = %#v, want empty", summaryAfterReset)
	}

	proxyResponse, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("call proxy route: %v", err)
	}
	defer proxyResponse.Body.Close()
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits = %d, want proxy route forwarded once", upstreamHits)
	}
}

// TestBuildApplicationConfigAPIUpdatesAnonymization verifies that saved dashboard toggles affect real anonymization.
func TestBuildApplicationConfigAPIUpdatesAnonymization(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	app, err := buildApplication(context.Background(), runtimeConfig{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     ptrLogger(zerolog.Nop()),
		Detectors:  noExternalDetectorsConfig(),
		StatsPath:  t.TempDir() + "/stats.jsonl",
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}
	defer app.Close()

	server := httptest.NewServer(app.handler)
	defer server.Close()

	configResponse, err := server.Client().Get(server.URL + "/api/config")
	if err != nil {
		t.Fatalf("call config API: %v", err)
	}
	defer configResponse.Body.Close()
	if configResponse.StatusCode != http.StatusOK {
		t.Fatalf("config status = %d, want 200", configResponse.StatusCode)
	}
	var initialConfig configAPIResponse
	if err := json.NewDecoder(configResponse.Body).Decode(&initialConfig); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if !optionEnabled(initialConfig.ProtectionOptions, anonymizer.EntityEmail) {
		t.Fatalf("initial config = %#v, want EMAIL enabled", initialConfig)
	}

	updateBody := strings.NewReader(`{"protection_options":[{"type":"EMAIL","enabled":false}]}`)
	updateResponse, err := server.Client().Post(server.URL+"/api/config", "application/json", updateBody)
	if err != nil {
		t.Fatalf("call config update with POST: %v", err)
	}
	defer updateResponse.Body.Close()
	if updateResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("config POST status = %d, want 404", updateResponse.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/config", strings.NewReader(`{"protection_options":[{"type":"EMAIL","enabled":false}]}`))
	if err != nil {
		t.Fatalf("build config request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	putResponse, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call config update: %v", err)
	}
	defer putResponse.Body.Close()
	if putResponse.StatusCode != http.StatusOK {
		t.Fatalf("config update status = %d, want 200", putResponse.StatusCode)
	}
	var updatedConfig configAPIResponse
	if err := json.NewDecoder(putResponse.Body).Decode(&updatedConfig); err != nil {
		t.Fatalf("decode updated config: %v", err)
	}
	if optionEnabled(updatedConfig.ProtectionOptions, anonymizer.EntityEmail) {
		t.Fatalf("updated config = %#v, want EMAIL disabled", updatedConfig)
	}

	proxyResponse, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer proxyResponse.Body.Close()
	if strings.Contains(upstreamBody, "[EMAIL_") || !strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want EMAIL left untouched", upstreamBody)
	}

	summary, err := app.statsRecorder.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalRequests != 1 || summary.AnonymizedRequests != 0 || len(summary.CountsByType) != 0 {
		t.Fatalf("summary = %#v, want one non-anonymized request", summary)
	}
}

func TestBuildApplicationAnonymizationTestAPIUsesConfigWithoutStatsOrLogs(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.DebugLevel)

	app, err := buildApplication(context.Background(), runtimeConfig{
		Target:     mustParseURL(t, "https://api.anthropic.com"),
		Logger:     &logger,
		Detectors:  noExternalDetectorsConfig(),
		StatsPath:  t.TempDir() + "/stats.jsonl",
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}
	defer app.Close()
	logs.Reset()

	server := httptest.NewServer(app.handler)
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/anonymization/test", "application/json", strings.NewReader(`{"text":"hello alice@example.com and FR76 3000 6000 0112 3456 7890 189"}`))
	if err != nil {
		t.Fatalf("call anonymization test API: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("anonymization test status = %d, want 200", response.StatusCode)
	}

	var preview anonymizationTestResponse
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatalf("decode anonymization test response: %v", err)
	}
	if got, want := preview.AnonymizedText, "hello [EMAIL_1] and [IBAN_1]"; got != want {
		t.Fatalf("anonymized text = %q, want %q", got, want)
	}
	if len(preview.Findings) != 2 {
		t.Fatalf("findings = %#v, want two anonymized findings", preview.Findings)
	}
	if len(preview.CountsByType) != 2 {
		t.Fatalf("counts = %#v, want two enabled counts", preview.CountsByType)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("logs = %q, want anonymization test endpoint to stay silent", got)
	}

	summary, err := app.statsRecorder.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalRequests != 0 {
		t.Fatalf("summary = %#v, want anonymization test to avoid stats", summary)
	}

	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/config", strings.NewReader(`{"protection_options":[{"type":"EMAIL","enabled":false},{"type":"IBAN","enabled":true}]}`))
	if err != nil {
		t.Fatalf("build config request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	updateResponse, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call config update: %v", err)
	}
	updateResponse.Body.Close()

	secondResponse, err := server.Client().Post(server.URL+"/api/anonymization/test", "application/json", strings.NewReader(`{"text":"hello alice@example.com and FR76 3000 6000 0112 3456 7890 189"}`))
	if err != nil {
		t.Fatalf("call anonymization test API after config update: %v", err)
	}
	defer secondResponse.Body.Close()

	var updatedPreview anonymizationTestResponse
	if err := json.NewDecoder(secondResponse.Body).Decode(&updatedPreview); err != nil {
		t.Fatalf("decode updated anonymization test response: %v", err)
	}
	if got, want := updatedPreview.AnonymizedText, "hello alice@example.com and [IBAN_1]"; got != want {
		t.Fatalf("anonymized text after config update = %q, want %q", got, want)
	}
	if len(updatedPreview.Findings) != 1 || updatedPreview.Findings[0].Type != anonymizer.EntityIBAN {
		t.Fatalf("updated findings = %#v, want only one anonymized iban finding", updatedPreview.Findings)
	}
	if got := logs.String(); got != "" {
		t.Fatalf("logs after config update = %q, want anonymization test endpoint to stay silent", got)
	}
}

// TestBuildApplicationServesDashboardBeforeProxy verifies that the local dashboard is served by the proxy itself
// and that missing dashboard routes are not accidentally forwarded upstream.
func TestBuildApplicationServesDashboardBeforeProxy(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamHits++
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	app, err := buildApplication(context.Background(), runtimeConfig{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     ptrLogger(zerolog.Nop()),
		Detectors:  noExternalDetectorsConfig(),
		StatsPath:  t.TempDir() + "/stats.jsonl",
		ConfigPath: testConfigPath(t),
	})
	if err != nil {
		t.Fatalf("buildApplication returned error: %v", err)
	}
	defer app.Close()

	server := httptest.NewServer(app.handler)
	defer server.Close()

	dashboardResponse, err := server.Client().Get(server.URL + "/dashboard")
	if err != nil {
		t.Fatalf("call dashboard: %v", err)
	}
	defer dashboardResponse.Body.Close()
	if dashboardResponse.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", dashboardResponse.StatusCode)
	}
	dashboardBody, err := io.ReadAll(dashboardResponse.Body)
	if err != nil {
		t.Fatalf("read dashboard body: %v", err)
	}
	if got := string(dashboardBody); !strings.Contains(got, "klovys99 Anonymization dashboard") ||
		!strings.Contains(got, "Anonymization dashboard") ||
		!strings.Contains(got, "Test tool") ||
		!strings.Contains(got, "/dashboard/test-tool") ||
		!strings.Contains(got, "klovys99-logo.png") ||
		!strings.Contains(got, "icon.svg") ||
		!strings.Contains(got, "Protection coverage") ||
		!strings.Contains(got, "Protection options") ||
		!strings.Contains(got, "Enable all") ||
		!strings.Contains(got, "Disable all") ||
		!strings.Contains(got, "Save changes") ||
		!strings.Contains(got, "/dashboard/assets/dashboard.js") {
		t.Fatalf("dashboard body = %q, want embedded dashboard HTML", got)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want dashboard not proxied", upstreamHits)
	}

	testToolResponse, err := server.Client().Get(server.URL + "/dashboard/test-tool")
	if err != nil {
		t.Fatalf("call test tool page: %v", err)
	}
	defer testToolResponse.Body.Close()
	if testToolResponse.StatusCode != http.StatusOK {
		t.Fatalf("test tool status = %d, want 200", testToolResponse.StatusCode)
	}
	testToolBody, err := io.ReadAll(testToolResponse.Body)
	if err != nil {
		t.Fatalf("read test tool body: %v", err)
	}
	if got := string(testToolBody); !strings.Contains(got, "klovys99 Test tool") ||
		!strings.Contains(got, "Preview how klovys99 anonymizes a prompt") ||
		!strings.Contains(got, "Test anonymization") ||
		!strings.Contains(got, "/dashboard/assets/dashboard.js") {
		t.Fatalf("test tool body = %q, want embedded test tool HTML", got)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want test tool page not proxied", upstreamHits)
	}

	cssResponse, err := server.Client().Get(server.URL + "/dashboard/assets/dashboard.css")
	if err != nil {
		t.Fatalf("call dashboard CSS: %v", err)
	}
	defer cssResponse.Body.Close()
	if cssResponse.StatusCode != http.StatusOK {
		t.Fatalf("dashboard CSS status = %d, want 200", cssResponse.StatusCode)
	}
	cssBody, err := io.ReadAll(cssResponse.Body)
	if err != nil {
		t.Fatalf("read dashboard CSS: %v", err)
	}
	if got := string(cssBody); !strings.Contains(got, "--primary: #076cd8") {
		t.Fatalf("dashboard CSS = %q, want klovys99 primary color", got)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want dashboard assets not proxied", upstreamHits)
	}

	logoResponse, err := server.Client().Get(server.URL + "/dashboard/assets/klovys99-logo.png")
	if err != nil {
		t.Fatalf("call dashboard logo: %v", err)
	}
	defer logoResponse.Body.Close()
	if logoResponse.StatusCode != http.StatusOK {
		t.Fatalf("dashboard logo status = %d, want 200", logoResponse.StatusCode)
	}
	logoBody, err := io.ReadAll(logoResponse.Body)
	if err != nil {
		t.Fatalf("read dashboard logo: %v", err)
	}
	if len(logoBody) < 8 || string(logoBody[1:4]) != "PNG" {
		t.Fatalf("dashboard logo does not look like a PNG, length=%d", len(logoBody))
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want dashboard logo not proxied", upstreamHits)
	}

	iconResponse, err := server.Client().Get(server.URL + "/dashboard/assets/icon.svg")
	if err != nil {
		t.Fatalf("call dashboard icon: %v", err)
	}
	defer iconResponse.Body.Close()
	if iconResponse.StatusCode != http.StatusOK {
		t.Fatalf("dashboard icon status = %d, want 200", iconResponse.StatusCode)
	}
	iconBody, err := io.ReadAll(iconResponse.Body)
	if err != nil {
		t.Fatalf("read dashboard icon: %v", err)
	}
	if !strings.Contains(string(iconBody), "<svg") {
		t.Fatalf("dashboard icon does not look like SVG: %q", string(iconBody))
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want dashboard icon not proxied", upstreamHits)
	}

	missingDashboardResponse, err := server.Client().Get(server.URL + "/dashboard/unknown")
	if err != nil {
		t.Fatalf("call missing dashboard route: %v", err)
	}
	defer missingDashboardResponse.Body.Close()
	if missingDashboardResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("missing dashboard status = %d, want 404", missingDashboardResponse.StatusCode)
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits = %d, want missing dashboard route not proxied", upstreamHits)
	}

	proxyResponse, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("call proxy route: %v", err)
	}
	defer proxyResponse.Body.Close()
	if upstreamHits != 1 {
		t.Fatalf("upstreamHits = %d, want proxy route forwarded once", upstreamHits)
	}
}

func setEnv(t *testing.T, name string, value *string) {
	t.Helper()

	if value == nil {
		previous, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset env %s: %v", name, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, previous)
			} else {
				_ = os.Unsetenv(name)
			}
		})
		return
	}
	t.Setenv(name, *value)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func ptrLogger(logger zerolog.Logger) *zerolog.Logger {
	return &logger
}

// optionEnabled returns one type's enabled state from an API option list.
func optionEnabled(options []appconfig.ProtectionOption, entityType anonymizer.EntityType) bool {
	for _, option := range options {
		if option.Type == entityType {
			return option.Enabled
		}
	}
	return false
}

func noExternalDetectorsConfig() detectors.Config {
	config := detectors.DefaultConfig()
	config.EnableGitleaks = false
	config.EnablePresidio = false
	return config
}

// testConfigPath returns a per-test app config file path.
func testConfigPath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/klovys99_config.json"
}

func stringPtr(value string) *string {
	return &value
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("error = nil, want containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want containing %q", err.Error(), want)
	}
}
