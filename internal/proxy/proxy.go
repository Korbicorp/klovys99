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
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

const (
	DefaultLogPath      = "proxy.log"
	DefaultAnthropicURL = "https://api.anthropic.com"
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
	engine      TextAnonymizer
	matchFinder MatchFinder
}

type promptAnonymizationRun struct {
	anonymizer  *sessionPromptAnonymizer
	engine      *anonymizer.Run
	ctx         context.Context
	logger      zerolog.Logger
	stats       map[anonymizer.EntityType]int
	readToolIDs map[string]struct{}
}

type TextAnonymizer interface {
	NewRun() *anonymizer.Run
}

type MatchFinder interface {
	FindMatches(ctx context.Context, input string) ([]anonymizer.Match, error)
}

type Config struct {
	Target      *url.URL
	Logger      *zerolog.Logger
	Transport   http.RoundTripper
	Anonymizer  TextAnonymizer
	MatchFinder MatchFinder
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
	promptAnonymizer := newSessionPromptAnonymizer(config.Anonymizer, config.MatchFinder)
	proxy := httputil.NewSingleHostReverseProxy(config.Target)
	proxy.Director = newDirector(config.Target, proxy.Director)
	if config.Transport != nil {
		proxy.Transport = config.Transport
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

		if shouldAnonymizeRequest(ctx.Request) {
			requestBody = []byte(promptAnonymizer.anonymize(ctx.Request.Context(), logger, string(requestBody)))
		}
		ctx.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
		ctx.Request.ContentLength = int64(len(requestBody))
		logTrafficRequest(logger, string(requestBody))
		proxy.ServeHTTP(ctx.Writer, ctx.Request)
	}, nil
}

func shouldAnonymizeRequest(request *http.Request) bool {
	if request.Method != http.MethodPost {
		return false
	}
	switch request.URL.Path {
	case "/v1/messages", "/v1/messages/count_tokens":
		return true
	default:
		return false
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

func logTrafficRequest(logger zerolog.Logger, body string) {
	logger.Debug().
		Str("direction", "request").
		Str("body", body).
		Msg("traffic body")
}

func newSessionPromptAnonymizer(engine TextAnonymizer, matchFinder MatchFinder) *sessionPromptAnonymizer {
	return &sessionPromptAnonymizer{
		engine:      engine,
		matchFinder: matchFinder,
	}
}

func (a *sessionPromptAnonymizer) anonymize(ctx context.Context, logger zerolog.Logger, body string) string {
	var payload any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return body
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return body
	}

	run := a.newRun(ctx, logger)
	run.readToolIDs = collectReadToolIDs(payload)
	anonymized, changed := run.anonymizeRequestValue(payload, anonymizationContext{})
	if !changed {
		return body
	}
	logAnonymizedStats(logger, run.stats)

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(anonymized); err != nil {
		return body
	}
	return strings.TrimSuffix(output.String(), "\n")
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
	conversationContent bool
	textValue           bool
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

			itemContext := r.childContext(key, typed, context)
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

func (r *promptAnonymizationRun) childContext(key string, parent map[string]any, context anonymizationContext) anonymizationContext {
	if key == "content" && isConversationMessage(parent) {
		return anonymizationContext{
			conversationContent: true,
			textValue:           isStringContent(parent, key),
		}
	}

	if key == "source" {
		return anonymizationContext{
			conversationContent: context.conversationContent,
		}
	}

	if key == "content" && stringMapValue(parent, "type") == "tool_result" {
		return anonymizationContext{
			conversationContent: context.conversationContent,
			textValue:           r.isReadToolResult(parent),
		}
	}

	if key == "text" && stringMapValue(parent, "type") == "text" && context.conversationContent {
		return anonymizationContext{
			conversationContent: true,
			textValue:           true,
		}
	}

	if key == "data" && stringMapValue(parent, "type") == "text" && context.conversationContent {
		return anonymizationContext{
			conversationContent: true,
			textValue:           true,
		}
	}

	return anonymizationContext{
		conversationContent: context.conversationContent,
		textValue:           context.textValue && !isMetadataKey(key),
	}
}

func isConversationMessage(value map[string]any) bool {
	switch stringMapValue(value, "role") {
	case "user", "assistant":
		return true
	default:
		return false
	}
}

func stringMapValue(value map[string]any, key string) string {
	typed, _ := value[key].(string)
	return typed
}

func isStringContent(value map[string]any, key string) bool {
	_, ok := value[key].(string)
	return ok
}

func (r *promptAnonymizationRun) isReadToolResult(value map[string]any) bool {
	toolUseID := stringMapValue(value, "tool_use_id")
	if toolUseID == "" {
		return false
	}
	_, ok := r.readToolIDs[toolUseID]
	return ok
}

func collectReadToolIDs(value any) map[string]struct{} {
	ids := make(map[string]struct{})
	collectReadToolIDsInto(value, ids)
	return ids
}

func collectReadToolIDsInto(value any, ids map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectReadToolIDsInto(item, ids)
		}
	case map[string]any:
		if stringMapValue(typed, "type") == "tool_use" && isFileReadToolName(stringMapValue(typed, "name")) {
			if id := stringMapValue(typed, "id"); id != "" {
				ids[id] = struct{}{}
			}
		}
		for _, item := range typed {
			collectReadToolIDsInto(item, ids)
		}
	}
}

func isFileReadToolName(name string) bool {
	switch strings.ToLower(name) {
	case "read", "notebookread", "glob", "grep", "ls":
		return true
	default:
		return false
	}
}

func (r *promptAnonymizationRun) anonymizeString(value string, textValue bool) (string, bool) {
	if textValue {
		return r.anonymizeTextValue(value)
	}
	return value, false
}

func (r *promptAnonymizationRun) anonymizeTextValue(value string) (string, bool) {
	anonymized, result := r.anonymizeText(value)
	if len(result.Stats) == 0 {
		return value, false
	}
	r.addStats(result)
	return anonymized, true
}

func (r *promptAnonymizationRun) anonymizeText(value string) (string, anonymizer.Result) {
	var llmMatches []anonymizer.Match
	if r.anonymizer.matchFinder != nil {
		matches, err := r.anonymizer.matchFinder.FindMatches(r.ctx, value)
		if err != nil {
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
