package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	statlog "github.com/Korbicorp/klovis/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

const (
	DefaultLogPath      = "proxy.log"
	DefaultAnthropicURL = "https://api.anthropic.com"
	sessionOpenTag      = "<session>"
	sessionCloseTag     = "</session>"
)

var metadataKeys = map[string]struct{}{
	"cache_control": {},
	"id":            {},
	"media_type":    {},
	"model":         {},
	"name":          {},
	"role":          {},
	"tool_use_id":   {},
	"type":          {},
}

type sessionPromptAnonymizer struct {
	engine        TextAnonymizer
	matchFinder   MatchFinder
	statsRecorder StatsRecorder
}

type promptAnonymizationRun struct {
	anonymizer *sessionPromptAnonymizer
	engine     *anonymizer.Run
	ctx        context.Context
	logger     zerolog.Logger
	stats      map[anonymizer.EntityType]int
}

type TextAnonymizer interface {
	NewRun() *anonymizer.Run
}

type MatchFinder interface {
	FindMatches(ctx context.Context, input string) ([]anonymizer.Match, error)
}

type StatsRecorder interface {
	Record(event statlog.Event) error
}

type Config struct {
	Target        *url.URL
	Logger        *zerolog.Logger
	Transport     http.RoundTripper
	Anonymizer    TextAnonymizer
	MatchFinder   MatchFinder
	StatsRecorder StatsRecorder
}

func NewProxyHandler(config Config) (gin.HandlerFunc, error) {
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
	if config.Anonymizer == nil {
		return nil, fmt.Errorf("proxy anonymizer is required")
	}

	logger := *config.Logger
	promptAnonymizer := newSessionPromptAnonymizer(config.Anonymizer, config.MatchFinder, config.StatsRecorder)
	proxy := httputil.NewSingleHostReverseProxy(config.Target)
	proxy.Director = newDirector(config.Target, proxy.Director)
	if config.Transport != nil {
		proxy.Transport = config.Transport
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventProxyError})
		logger.Error().Err(err).Msg("proxy error")
		http.Error(writer, err.Error(), http.StatusBadGateway)
	}

	return func(ctx *gin.Context) {
		requestBody, err := readBody(ctx.Request.Body)
		if err != nil {
			recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventRequestBodyError})
			logger.Error().Err(err).Msg("read request body")
			ctx.String(http.StatusBadRequest, err.Error())
			return
		}

		outcome := promptAnonymizer.anonymizeWithStats(ctx.Request.Context(), logger, string(requestBody))
		anonymizedBody := outcome.body
		recordStatsEvent(logger, config.StatsRecorder, requestProcessedEvent(outcome.stats))
		requestBody = []byte(anonymizedBody)
		ctx.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
		ctx.Request.ContentLength = int64(len(requestBody))
		logTrafficRequest(logger, anonymizedBody)
		proxy.ServeHTTP(ctx.Writer, ctx.Request)
	}, nil
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

func logTrafficRequest(logger zerolog.Logger, body string) {
	logger.Debug().
		Str("direction", "request").
		Str("body", body).
		Msg("traffic body")
}

func newSessionPromptAnonymizer(engine TextAnonymizer, matchFinder MatchFinder, statsRecorders ...StatsRecorder) *sessionPromptAnonymizer {
	var statsRecorder StatsRecorder
	if len(statsRecorders) > 0 {
		statsRecorder = statsRecorders[0]
	}
	return &sessionPromptAnonymizer{
		engine:        engine,
		matchFinder:   matchFinder,
		statsRecorder: statsRecorder,
	}
}

func (a *sessionPromptAnonymizer) anonymize(ctx context.Context, logger zerolog.Logger, body string) string {
	return a.anonymizeWithStats(ctx, logger, body).body
}

type anonymizationOutcome struct {
	body  string
	stats map[anonymizer.EntityType]int
}

func (a *sessionPromptAnonymizer) anonymizeWithStats(ctx context.Context, logger zerolog.Logger, body string) anonymizationOutcome {
	var payload any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return anonymizationOutcome{body: body}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return anonymizationOutcome{body: body}
	}

	run := a.newRun(ctx, logger)
	anonymized, changed := run.anonymizeRequestValue(payload, anonymizationContext{})
	if !changed {
		return anonymizationOutcome{body: body, stats: run.stats}
	}
	logAnonymizedStats(logger, run.stats)

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(anonymized); err != nil {
		return anonymizationOutcome{body: body}
	}
	return anonymizationOutcome{
		body:  strings.TrimSuffix(output.String(), "\n"),
		stats: run.stats,
	}
}

func (a *sessionPromptAnonymizer) newRun(ctx context.Context, logger zerolog.Logger) *promptAnonymizationRun {
	return &promptAnonymizationRun{
		anonymizer: a,
		engine:     a.engine.NewRun(),
		ctx:        ctx,
		logger:     logger,
		stats:      make(map[anonymizer.EntityType]int),
	}
}

type anonymizationContext struct {
	textValue bool
}

func (r *promptAnonymizationRun) anonymizeRequestValue(value any, context anonymizationContext) (any, bool) {
	switch typed := value.(type) {
	case string:
		return r.anonymizeString(typed, context.textValue)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := r.anonymizeRequestValue(item, context)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			if isMetadataKey(key) {
				continue
			}

			itemContext := anonymizationContext{
				textValue: isTextValue(key, typed, context),
			}
			anonymized, itemChanged := r.anonymizeRequestValue(item, itemContext)
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

func isMetadataKey(key string) bool {
	_, ok := metadataKeys[key]
	return ok
}

func isTextValue(key string, parent map[string]any, context anonymizationContext) bool {
	if key == "data" {
		return stringMapValue(parent, "type") == "text"
	}

	if key == "source" {
		return false
	}

	if key == "content" && stringMapValue(parent, "type") == "tool_result" {
		return true
	}

	if isTextualKey(key) {
		return true
	}

	return context.textValue && !isMetadataKey(key)
}

func stringMapValue(value map[string]any, key string) string {
	typed, _ := value[key].(string)
	return typed
}

func isTextualKey(key string) bool {
	switch key {
	case "content", "error", "input", "output", "result", "system", "text":
		return true
	default:
		return false
	}
}

func (r *promptAnonymizationRun) anonymizeString(value string, textValue bool) (string, bool) {
	if textValue {
		return r.anonymizeTextValue(value)
	}
	return r.anonymizeSessionPromptString(value)
}

func (r *promptAnonymizationRun) anonymizeTextValue(value string) (string, bool) {
	anonymized, result := r.anonymizeText(value)
	if len(result.Stats) == 0 {
		return value, false
	}
	r.addStats(result)
	return anonymized, true
}

func (r *promptAnonymizationRun) anonymizeSessionPromptString(value string) (string, bool) {
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
		anonymized, result := r.anonymizeText(prompt)
		r.addStats(result)

		builder.WriteString(remaining[:contentStart])
		builder.WriteString(anonymized)
		builder.WriteString(sessionCloseTag)
		remaining = remaining[contentEnd+len(sessionCloseTag):]
		changed = true
	}

	if !changed {
		return value, false
	}
	return builder.String(), true
}

func (r *promptAnonymizationRun) anonymizeText(value string) (string, anonymizer.Result) {
	var llmMatches []anonymizer.Match
	if r.anonymizer.matchFinder != nil {
		matches, err := r.anonymizer.matchFinder.FindMatches(r.ctx, value)
		if err != nil {
			recordStatsEvent(r.logger, r.anonymizer.statsRecorder, statlog.Event{Event: statlog.EventLLMError})
			r.logger.Error().Err(err).Msg("llm anonymization failed")
		} else {
			llmMatches = matches
		}
	}

	return r.engine.AnonymizeWithMatches(value, llmMatches)
}

func (r *promptAnonymizationRun) addStats(result anonymizer.Result) {
	for entityType, entityStats := range result.Stats {
		r.stats[entityType] += entityStats.Count
	}
}

func logAnonymizedStats(logger zerolog.Logger, stats map[anonymizer.EntityType]int) {
	event := logger.Info()
	for _, entityType := range sortedEntityTypes(stats) {
		event = event.Int(string(entityType), stats[entityType])
	}
	event.Msg("request body anonymized")
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

func requestProcessedEvent(counts map[anonymizer.EntityType]int) statlog.Event {
	stringCounts := make(map[string]int, len(counts))
	total := 0
	for entityType, count := range counts {
		if count <= 0 {
			continue
		}
		stringCounts[string(entityType)] += count
		total += count
	}
	return statlog.Event{
		Event:             statlog.EventRequestProcessed,
		Anonymized:        total > 0,
		TotalReplacements: total,
		Counts:            stringCounts,
	}
}

func recordStatsEvent(logger zerolog.Logger, recorder StatsRecorder, event statlog.Event) {
	if recorder == nil {
		return
	}
	if err := recorder.Record(event); err != nil {
		logger.Error().Err(err).Msg("stats record failed")
	}
}
