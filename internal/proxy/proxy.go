package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

const (
	DefaultLogPath      = "proxy.log"
	DebugTrafficLogEnv  = "KLOVIS_PROXY_DEBUG"
	defaultAnthropicURL = "https://api.anthropic.com"
	sessionOpenTag      = "<session>"
	sessionCloseTag     = "</session>"
)

type Config struct {
	Target     *url.URL
	Logger     *zerolog.Logger
	TrafficLog io.Writer
	Transport  http.RoundTripper
}

func RunAnthropic(addr string) error {
	target, err := url.Parse(defaultAnthropicURL)
	if err != nil {
		return fmt.Errorf("parse anthropic URL: %w", err)
	}
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	trafficLog, closeTrafficLog, err := trafficLogFromEnv()
	if err != nil {
		return err
	}
	defer func() {
		_ = closeTrafficLog()
	}()

	handler, err := NewHandler(Config{
		Target:     target,
		Logger:     &logger,
		TrafficLog: trafficLog,
	})
	if err != nil {
		return err
	}

	return http.ListenAndServe(addr, handler)
}

func trafficLogFromEnv() (io.Writer, func() error, error) {
	if !debugTrafficLogEnabled() {
		return io.Discard, func() error { return nil }, nil
	}

	logFile, err := os.OpenFile(DefaultLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open proxy log file: %w", err)
	}
	return logFile, logFile.Close, nil
}

func debugTrafficLogEnabled() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(DebugTrafficLogEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func NewHandler(config Config) (http.Handler, error) {
	if config.Target == nil {
		return nil, fmt.Errorf("proxy target is required")
	}
	if config.Target.Scheme == "" || config.Target.Host == "" {
		return nil, fmt.Errorf("proxy target must include scheme and host")
	}
	if config.Logger == nil {
		logger := zerolog.Nop()
		config.Logger = &logger
	}
	if config.TrafficLog == nil {
		config.TrafficLog = io.Discard
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Any("/*proxyPath", newProxyHandler(config))

	return router, nil
}

func newProxyHandler(config Config) gin.HandlerFunc {
	target := *config.Target
	logger := *config.Logger
	trafficLogger := newTrafficLogger(config.TrafficLog)
	promptAnonymizer := newSessionPromptAnonymizer()
	proxy := httputil.NewSingleHostReverseProxy(&target)
	proxy.Director = newDirector(&target, proxy.Director)
	if config.Transport != nil {
		proxy.Transport = config.Transport
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		body, err := readBody(response.Body)
		if err != nil {
			return fmt.Errorf("read upstream response body: %w", err)
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		trafficLogger.logResponse(body, response.Header.Get("Content-Encoding"))
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		logger.Error().Err(err).Msg("proxy error")
		http.Error(writer, err.Error(), http.StatusBadGateway)
	}

	return func(ctx *gin.Context) {
		requestBody, err := readBody(ctx.Request.Body)
		if err != nil {
			logger.Error().Err(err).Msg("read request body")
			ctx.String(http.StatusBadRequest, err.Error())
			return
		}

		requestBody = promptAnonymizer.anonymize(logger, requestBody)
		ctx.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
		ctx.Request.ContentLength = int64(len(requestBody))
		trafficLogger.logRequest(requestBody)
		proxy.ServeHTTP(ctx.Writer, ctx.Request)
	}
}

func newDirector(target *url.URL, defaultDirector func(*http.Request)) func(*http.Request) {
	return func(request *http.Request) {
		defaultDirector(request)
		request.Host = target.Host
		request.URL.Host = target.Host
		request.URL.Scheme = target.Scheme
	}
}

func readBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	content, err := io.ReadAll(body)
	if closeErr := body.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	return content, nil
}

type trafficLogger struct {
	mu     sync.Mutex
	writer io.Writer
}

func newTrafficLogger(writer io.Writer) *trafficLogger {
	return &trafficLogger{writer: writer}
}

func (l *trafficLogger) logRequest(body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.writer, "request.body=%s\n", string(body))
}

func (l *trafficLogger) logResponse(body []byte, contentEncoding string) {
	logBody, err := decodeGzipBodyForLog(body, contentEncoding)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		fmt.Fprintf(l.writer, "response.body.decode_error=%v\n", err)
		return
	}
	fmt.Fprintf(l.writer, "response.body=%s\n", string(logBody))
}

func decodeGzipBodyForLog(body []byte, contentEncoding string) ([]byte, error) {
	if strings.TrimSpace(strings.ToLower(contentEncoding)) != "gzip" {
		return body, nil
	}

	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

type sessionPromptAnonymizer struct {
	mu     sync.Mutex
	engine *anonymizer.Anonymizer
}

func newSessionPromptAnonymizer() *sessionPromptAnonymizer {
	return &sessionPromptAnonymizer{
		engine: anonymizer.New(detectors.Default(true)),
	}
}

func (a *sessionPromptAnonymizer) anonymize(logger zerolog.Logger, body []byte) []byte {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return body
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return body
	}

	stats := make(map[anonymizer.EntityType]int)
	a.mu.Lock()
	anonymized, changed := anonymizeSessionPrompts(payload, a.engine, stats)
	a.mu.Unlock()
	if !changed {
		return body
	}
	logAnonymizedStats(logger, stats)

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(anonymized); err != nil {
		return body
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}

func anonymizeSessionPrompts(value any, engine *anonymizer.Anonymizer, stats map[anonymizer.EntityType]int) (any, bool) {
	switch typed := value.(type) {
	case string:
		return anonymizeSessionPromptString(typed, engine, stats)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := anonymizeSessionPrompts(item, engine, stats)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			anonymized, itemChanged := anonymizeSessionPrompts(item, engine, stats)
			if key == "content" && typed["role"] == "user" {
				anonymized, itemChanged = anonymizeUserContent(item, engine, stats)
			}
			if itemChanged {
				typed[key] = anonymized
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func anonymizeUserContent(value any, engine *anonymizer.Anonymizer, stats map[anonymizer.EntityType]int) (any, bool) {
	switch typed := value.(type) {
	case string:
		return anonymizeUserText(typed, engine, stats)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := anonymizeUserContent(item, engine, stats)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			anonymized, itemChanged := anonymizeUserContent(item, engine, stats)
			if itemChanged {
				typed[key] = anonymized
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func anonymizeUserText(value string, engine *anonymizer.Anonymizer, stats map[anonymizer.EntityType]int) (string, bool) {
	if strings.HasPrefix(strings.TrimSpace(value), "<system-reminder>") {
		return value, false
	}
	if strings.Contains(value, sessionOpenTag) {
		return anonymizeSessionPromptString(value, engine, stats)
	}

	anonymized, result := engine.Anonymize([]byte(value))
	if len(result.Stats) == 0 {
		return value, false
	}
	addAnonymizedStats(stats, result)
	return string(anonymized), true
}

func anonymizeSessionPromptString(value string, engine *anonymizer.Anonymizer, stats map[anonymizer.EntityType]int) (string, bool) {
	var builder strings.Builder
	changed := false
	remaining := value

	for {
		openIndex := strings.Index(remaining, sessionOpenTag)
		if openIndex < 0 {
			builder.WriteString(remaining)
			break
		}

		contentStart := openIndex + len(sessionOpenTag)
		closeIndex := strings.Index(remaining[contentStart:], sessionCloseTag)
		if closeIndex < 0 {
			builder.WriteString(remaining)
			break
		}

		contentEnd := contentStart + closeIndex
		prompt := remaining[contentStart:contentEnd]
		anonymized, result := engine.Anonymize([]byte(prompt))
		addAnonymizedStats(stats, result)

		builder.WriteString(remaining[:contentStart])
		builder.Write(anonymized)
		builder.WriteString(sessionCloseTag)
		remaining = remaining[contentEnd+len(sessionCloseTag):]
		changed = true
	}

	if !changed {
		return value, false
	}
	return builder.String(), true
}

func addAnonymizedStats(stats map[anonymizer.EntityType]int, result anonymizer.Result) {
	for entityType, entityStats := range result.Stats {
		stats[entityType] += entityStats.Count
	}
}

func logAnonymizedStats(logger zerolog.Logger, stats map[anonymizer.EntityType]int) {
	event := logger.Info()
	for _, entityType := range sortedEntityTypes(stats) {
		event = event.Int(string(entityType), stats[entityType])
	}
	event.Msg("session prompt anonymized")
}

func sortedEntityTypes(stats map[anonymizer.EntityType]int) []anonymizer.EntityType {
	entityTypes := make([]anonymizer.EntityType, 0, len(stats))
	for entityType := range stats {
		entityTypes = append(entityTypes, entityType)
	}
	sort.Slice(entityTypes, func(i, j int) bool {
		return entityTypes[i] < entityTypes[j]
	})
	return entityTypes
}
