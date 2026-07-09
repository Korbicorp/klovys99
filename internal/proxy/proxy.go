package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/proxy/providers"
	statlog "github.com/Korbicorp/klovys99/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

const (
	DefaultLogPath       = "proxy.log"
	DefaultAnthropicURL  = "https://api.anthropic.com"
	DefaultOpenAIURL     = "https://api.openai.com"
	AnthropicRoutePrefix = "/anthropic"
	OpenAIRoutePrefix    = "/openai"
)

type TextAnonymizer interface {
	NewRun() *anonymizer.Run
}

type StatsRecorder interface {
	Record(event statlog.Event) error
}

type Config struct {
	Target         *url.URL
	RouteTargets   map[string]*url.URL
	ChatGPTTarget  *url.URL
	Logger         *zerolog.Logger
	Transport      http.RoundTripper
	Anonymizer     TextAnonymizer
	StatsRecorder  StatsRecorder
	LogPIIFindings bool
	Anthropic      *providers.Anthropic
	OpenAI         *providers.OpenAI
}

type responseTransformContextKey struct{}

type responseTransformContext struct {
	mapping   *providers.ResponseRestoreMapping
	transform func(*providers.ResponseRestoreMapping, *http.Response) error
}

func NewProxyHandler(config Config) (gin.HandlerFunc, error) {
	if config.Target == nil {
		return nil, fmt.Errorf("proxy target is required")
	}
	if err := validateTarget(config.Target); err != nil {
		return nil, err
	}
	for prefix, target := range config.RouteTargets {
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("proxy route prefix %q must start with /", prefix)
		}
		if err := validateTarget(target); err != nil {
			return nil, fmt.Errorf("proxy route %q: %w", prefix, err)
		}
	}
	if config.Logger == nil {
		logger := zerolog.Nop()
		config.Logger = &logger
	}
	if config.Anonymizer == nil {
		return nil, fmt.Errorf("proxy anonymizer is required")
	}

	logger := *config.Logger
	anthropicProvider := config.Anthropic
	if anthropicProvider == nil {
		var err error
		anthropicProvider, err = providers.NewAnthropic(providers.AnthropicConfig{
			RoutePrefix:    AnthropicRoutePrefix,
			Anonymizer:     config.Anonymizer,
			LogPIIFindings: config.LogPIIFindings,
		})
		if err != nil {
			return nil, err
		}
	}
	openAIProvider := config.OpenAI
	if openAIProvider == nil {
		openAITarget := config.RouteTargets[OpenAIRoutePrefix]
		if openAITarget == nil {
			parsedOpenAI, err := url.Parse(DefaultOpenAIURL)
			if err != nil {
				return nil, err
			}
			openAITarget = parsedOpenAI
		}
		httpClient := http.DefaultClient
		if config.Transport != nil {
			httpClient = &http.Client{Transport: config.Transport}
		}
		var err error
		openAIProvider, err = providers.NewOpenAI(providers.OpenAIConfig{
			APITarget:      openAITarget,
			ChatGPTTarget:  config.ChatGPTTarget,
			HTTPClient:     httpClient,
			Anonymizer:     config.Anonymizer,
			Logger:         config.Logger,
			StatsRecorder:  config.StatsRecorder,
			LogPIIFindings: config.LogPIIFindings,
		})
		if err != nil {
			return nil, err
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(config.Target)
	proxy.Director = newDirector(config.Target, config.RouteTargets, openAIProvider)
	if config.Transport != nil {
		proxy.Transport = config.Transport
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		transform, ok := responseTransformFromRequest(response.Request)
		if !ok || transform.mapping == nil || transform.transform == nil {
			return nil
		}
		return transform.transform(transform.mapping, response)
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventProxyError})
		logger.Error().Err(err).Msg("proxy error")
		http.Error(writer, err.Error(), http.StatusBadGateway)
	}

	return func(ctx *gin.Context) {
		if openAIProvider.MatchWebSocket(ctx.Request) {
			openAIProvider.HandleWebSocket(ctx.Writer, ctx.Request)
			return
		}
		if openAIProvider.HandleModelsHTTP(ctx.Writer, ctx.Request) {
			return
		}

		requestBody, err := readBody(ctx.Request.Body)
		if err != nil {
			recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventRequestBodyError})
			logger.Error().Err(err).Msg("read request body")
			ctx.String(http.StatusBadRequest, err.Error())
			return
		}

		logTrafficRequest(logger, "before_anonymization", string(requestBody))
		var responseTransform responseTransformContext
		if openAIProvider.ShouldAnonymizeHTTP(ctx.Request) {
			outcome, err := openAIProvider.AnonymizeHTTPRequest(ctx.Request, requestBody)
			if err != nil {
				recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventRequestBodyError})
				ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			recordStatsEvent(logger, config.StatsRecorder, requestProcessedEvent(outcome.Stats))
			if len(outcome.Stats) > 0 {
				logAnonymizedStats(logger, outcome.Stats, outcome.Findings, config.LogPIIFindings)
			}
			requestBody = outcome.Body
			responseTransform = responseTransformContext{
				mapping:   outcome.RestoreMapping,
				transform: openAIProvider.DeanonymizeHTTPResponse,
			}
		} else if anthropicProvider.ShouldAnonymizeHTTP(ctx.Request) {
			outcome, err := anthropicProvider.AnonymizeHTTPRequest(ctx.Request, logger, requestBody)
			if err != nil {
				recordStatsEvent(logger, config.StatsRecorder, statlog.Event{Event: statlog.EventRequestBodyError})
				ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			recordStatsEvent(logger, config.StatsRecorder, requestProcessedEvent(outcome.Stats))
			requestBody = outcome.Body
			responseTransform = responseTransformContext{
				mapping:   outcome.RestoreMapping,
				transform: anthropicProvider.DeanonymizeHTTPResponse,
			}
		}
		ctx.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
		ctx.Request.ContentLength = int64(len(requestBody))
		if responseTransform.mapping != nil && responseTransform.transform != nil {
			ctx.Request.Header.Del("Accept-Encoding")
			ctx.Request = ctx.Request.WithContext(
				contextWithResponseTransform(ctx.Request.Context(), responseTransform),
			)
		}
		logTrafficRequest(logger, "after_anonymization", string(requestBody))
		proxy.ServeHTTP(ctx.Writer, ctx.Request)
	}, nil
}

func contextWithResponseTransform(ctx context.Context, transform responseTransformContext) context.Context {
	return context.WithValue(ctx, responseTransformContextKey{}, transform)
}

func responseTransformFromRequest(request *http.Request) (responseTransformContext, bool) {
	if request == nil {
		return responseTransformContext{}, false
	}
	transform, ok := request.Context().Value(responseTransformContextKey{}).(responseTransformContext)
	return transform, ok
}

func validateTarget(target *url.URL) error {
	if target == nil {
		return fmt.Errorf("proxy target is required")
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("proxy target must include scheme and host")
	}
	return nil
}

func newDirector(defaultTarget *url.URL, routeTargets map[string]*url.URL, openAIProvider *providers.OpenAI) func(*http.Request) {
	routes := compileTargetRoutes(routeTargets)
	return func(request *http.Request) {
		target, requestPath, ok := openAIProvider.ResolveHTTPRoute(request)
		if !ok {
			target, requestPath = resolveTarget(request.URL.Path, defaultTarget, routes)
		}
		request.URL.Path = joinURLPath(target.Path, requestPath)
		request.Host = target.Host
		request.URL.Host = target.Host
		request.URL.Scheme = target.Scheme
	}
}

type targetRoute struct {
	prefix string
	target *url.URL
}

func compileTargetRoutes(routeTargets map[string]*url.URL) []targetRoute {
	if len(routeTargets) == 0 {
		return nil
	}

	routes := make([]targetRoute, 0, len(routeTargets))
	for prefix, target := range routeTargets {
		routes = append(routes, targetRoute{
			prefix: strings.TrimRight(prefix, "/"),
			target: target,
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		return len(routes[i].prefix) > len(routes[j].prefix)
	})
	return routes
}

func resolveTarget(requestPath string, defaultTarget *url.URL, routes []targetRoute) (*url.URL, string) {
	for _, route := range routes {
		if route.prefix == "" {
			continue
		}
		if requestPath == route.prefix {
			return route.target, "/"
		}
		if strings.HasPrefix(requestPath, route.prefix+"/") {
			return route.target, strings.TrimPrefix(requestPath, route.prefix)
		}
	}
	return defaultTarget, requestPath
}

func joinURLPath(basePath, requestPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if requestPath == "" {
		requestPath = "/"
	}
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}
	if basePath == "" {
		return requestPath
	}
	return basePath + requestPath
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

func logTrafficRequest(logger zerolog.Logger, stage string, body string) {
	logger.Debug().
		Str("direction", "request").
		Str("stage", stage).
		Str("body", body).
		Msg("traffic body")
}

func logAnonymizedStats(logger zerolog.Logger, stats map[anonymizer.EntityType]int, findings []anonymizer.Finding, includePII bool) {
	event := logger.Info()
	for _, entityType := range sortedEntityTypes(stats) {
		event = event.Int(string(entityType), stats[entityType])
	}
	if includePII {
		event = event.Interface("pii_findings", providers.PIILogFindings(findings))
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
