package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/ner"
	statlog "github.com/Korbicorp/klovys99/internal/stats"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const (
	DefaultChatGPTURL            = "https://chatgpt.com"
	RequiredResponsesWSBetaToken = "responses_websockets=2026-02-06"
)

var codexResponsePaths = map[string]struct{}{
	"/v1/responses":                {},
	"/v1/codex/responses":          {},
	"/backend-api/responses":       {},
	"/backend-api/codex/responses": {},
}

var hopByHopHeaders = map[string]struct{}{
	"connection":               {},
	"upgrade":                  {},
	"host":                     {},
	"content-length":           {},
	"transfer-encoding":        {},
	"sec-websocket-accept":     {},
	"sec-websocket-extensions": {},
	"sec-websocket-key":        {},
	"sec-websocket-protocol":   {},
	"sec-websocket-version":    {},
}

type TextAnonymizer interface {
	NewRun() *anonymizer.Run
}

type StatsRecorder interface {
	Record(event statlog.Event) error
}

type OpenAIConfig struct {
	APITarget      *url.URL
	ChatGPTTarget  *url.URL
	HTTPClient     *http.Client
	Anonymizer     TextAnonymizer
	Logger         *zerolog.Logger
	StatsRecorder  StatsRecorder
	LogPIIFindings bool
	NERAnalyzer    ner.Analyzer
}

type OpenAI struct {
	apiTarget         *url.URL
	chatGPTTarget     *url.URL
	httpClient        *http.Client
	anonymizer        TextAnonymizer
	logger            zerolog.Logger
	statsRecorder     StatsRecorder
	logPIIFindings    bool
	nerAnalyzer       ner.Analyzer
	responseMappings  map[string]*ResponseRestoreMapping
	responseMappingsM sync.RWMutex
}

type AnonymizeResult struct {
	Body           []byte
	RestoreMapping *ResponseRestoreMapping
	Stats          map[anonymizer.EntityType]int
	Findings       []anonymizer.Finding
}

type openAIAnonymizationRun struct {
	engine     *anonymizer.Run
	stats      map[anonymizer.EntityType]int
	findings   []anonymizer.Finding
	nerMatches ner.MatchSet
}

type webSocketMappingState struct {
	mu      sync.RWMutex
	mapping *ResponseRestoreMapping
}

func newWebSocketMappingState(mapping *ResponseRestoreMapping) *webSocketMappingState {
	return &webSocketMappingState{mapping: mapping}
}

func (s *webSocketMappingState) Load() *ResponseRestoreMapping {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mapping
}

func (s *webSocketMappingState) Store(mapping *ResponseRestoreMapping) {
	s.mu.Lock()
	s.mapping = mapping
	s.mu.Unlock()
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	if config.APITarget == nil {
		return nil, fmt.Errorf("openai api target is required")
	}
	if config.ChatGPTTarget == nil {
		target, err := url.Parse(DefaultChatGPTURL)
		if err != nil {
			return nil, err
		}
		config.ChatGPTTarget = target
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	if config.Anonymizer == nil {
		return nil, fmt.Errorf("openai anonymizer is required")
	}
	logger := zerolog.Nop()
	if config.Logger != nil {
		logger = *config.Logger
	}
	return &OpenAI{
		apiTarget:        config.APITarget,
		chatGPTTarget:    config.ChatGPTTarget,
		httpClient:       config.HTTPClient,
		anonymizer:       config.Anonymizer,
		logger:           logger,
		statsRecorder:    config.StatsRecorder,
		logPIIFindings:   config.LogPIIFindings,
		nerAnalyzer:      config.NERAnalyzer,
		responseMappings: make(map[string]*ResponseRestoreMapping),
	}, nil
}

func (p *OpenAI) MatchHTTP(request *http.Request) bool {
	path := NormalizeOpenAIPath(request.URL.Path)
	if _, ok := codexResponsePaths[path]; ok {
		return true
	}
	if path == "/v1/chat/completions" {
		return true
	}
	if path == "/v1/models" || strings.HasPrefix(path, "/v1/models/") {
		return true
	}
	return false
}

func (p *OpenAI) MatchWebSocket(request *http.Request) bool {
	if !isWebSocketUpgrade(request) {
		return false
	}
	path := NormalizeOpenAIPath(request.URL.Path)
	_, ok := codexResponsePaths[path]
	return ok
}

func (p *OpenAI) ShouldAnonymizeHTTP(request *http.Request) bool {
	if request.Method != http.MethodPost {
		return false
	}
	path := NormalizeOpenAIPath(request.URL.Path)
	if _, ok := codexResponsePaths[path]; ok {
		return true
	}
	return path == "/v1/chat/completions"
}

func (p *OpenAI) AnonymizeHTTPRequest(request *http.Request, body []byte) (AnonymizeResult, error) {
	path := NormalizeOpenAIPath(request.URL.Path)
	switch {
	case isCodexResponsesPath(path):
		return p.anonymizeResponsesBody(request.Context(), body)
	case path == "/v1/chat/completions":
		return p.anonymizeChatCompletionsBody(request.Context(), body)
	default:
		return AnonymizeResult{Body: body}, nil
	}
}

func (p *OpenAI) ResolveHTTPRoute(request *http.Request) (*url.URL, string, bool) {
	path := NormalizeOpenAIPath(request.URL.Path)
	switch {
	case isCodexResponsesPath(path):
		if headers, chatGPTAuth := ResolveCodexRoutingHeaders(request.Header); chatGPTAuth {
			copyResolvedHeaders(request.Header, headers)
			return p.chatGPTTarget, "/backend-api/codex/responses", true
		}
		return p.apiTarget, "/v1/responses", true
	case path == "/v1/chat/completions":
		return p.apiTarget, "/v1/chat/completions", true
	case request.Method == http.MethodGet && (path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")):
		return p.apiTarget, path, true
	default:
		return nil, "", false
	}
}

func (p *OpenAI) HandleModelsHTTP(writer http.ResponseWriter, request *http.Request) bool {
	path := NormalizeOpenAIPath(request.URL.Path)
	if request.Method != http.MethodGet {
		return false
	}
	if path != "/v1/models" && !strings.HasPrefix(path, "/v1/models/") {
		return false
	}
	headers, chatGPTAuth := ResolveCodexRoutingHeaders(request.Header)
	if !chatGPTAuth {
		return false
	}
	requestedClientVersion := request.URL.Query().Get("client_version")
	var response []byte
	var status int
	var err error
	if path == "/v1/models" {
		response, status, err = p.chatGPTModelsList(request.Context(), headers, requestedClientVersion)
	} else {
		modelID := strings.TrimPrefix(path, "/v1/models/")
		response, status, err = p.chatGPTModelGet(request.Context(), headers, modelID, requestedClientVersion)
	}
	if err != nil {
		p.logger.Warn().Err(err).Msg("codex model registry fetch failed; using synthetic fallback")
	}
	writer.Header().Set("content-type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(response)
	return true
}

func (p *OpenAI) HandleWebSocket(writer http.ResponseWriter, request *http.Request) {
	clientProtocols := parseSubprotocols(request.Header.Get("Sec-WebSocket-Protocol"))
	upgrader := websocket.Upgrader{
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: clientProtocols,
	}
	clientConn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		p.logger.Error().Err(err).Msg("websocket upgrade failed")
		return
	}
	defer clientConn.Close()

	clientConn.SetReadDeadline(time.Now().Add(30 * time.Second))
	firstType, firstMessage, err := clientConn.ReadMessage()
	clientConn.SetReadDeadline(time.Time{})
	if err != nil {
		p.closeWebSocket(clientConn, websocket.CloseGoingAway, "first-frame timeout")
		return
	}
	if firstType != websocket.TextMessage {
		p.closeWebSocket(clientConn, websocket.CloseUnsupportedData, "first frame must be text")
		return
	}
	p.logTrafficFrame("request", "before_anonymization", firstMessage)
	mapping, firstMessage, stats, findings, err := p.anonymizeWebSocketFrame(nil, firstMessage)
	if err != nil {
		p.closeWebSocket(clientConn, websocket.ClosePolicyViolation, "invalid sensitive frame")
		return
	}
	p.recordAnonymizedStats(stats, findings)

	upstreamURL, upstreamHeaders := p.upstreamWebSocket(request)
	dialer := websocket.Dialer{Subprotocols: clientProtocols}
	upstreamConn, _, err := dialer.DialContext(request.Context(), upstreamURL, upstreamHeaders)
	if err != nil {
		p.logger.Error().Err(err).Str("upstream", upstreamURL).Msg("upstream websocket dial failed")
		p.closeWebSocket(clientConn, websocket.CloseTryAgainLater, "upstream websocket failed")
		return
	}
	defer upstreamConn.Close()

	if err := upstreamConn.WriteMessage(websocket.TextMessage, firstMessage); err != nil {
		p.logger.Error().Err(err).Msg("write first websocket frame upstream")
		return
	}

	done := make(chan struct{}, 2)
	mappingState := newWebSocketMappingState(mapping)
	go p.relayClientToUpstream(mappingState, clientConn, upstreamConn, done)
	go p.relayUpstreamToClient(mappingState, clientConn, upstreamConn, done)
	<-done
}

func (p *OpenAI) relayClientToUpstream(mappingState *webSocketMappingState, clientConn, upstreamConn *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		messageType, message, err := clientConn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			p.closeWebSocket(clientConn, websocket.CloseUnsupportedData, "client frames must be text")
			return
		}
		p.logTrafficFrame("request", "before_anonymization", message)
		nextMapping, anonymized, stats, findings, err := p.anonymizeWebSocketFrame(mappingState.Load(), message)
		if err != nil {
			p.closeWebSocket(clientConn, websocket.ClosePolicyViolation, "invalid sensitive frame")
			return
		}
		p.logTrafficFrame("request", "after_anonymization", anonymized)
		mappingState.Store(nextMapping)
		p.recordAnonymizedStats(stats, findings)
		if err := upstreamConn.WriteMessage(websocket.TextMessage, anonymized); err != nil {
			return
		}
	}
}

func (p *OpenAI) relayUpstreamToClient(mappingState *webSocketMappingState, clientConn, upstreamConn *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		messageType, message, err := upstreamConn.ReadMessage()
		if err != nil {
			return
		}
		if messageType == websocket.TextMessage {
			mapping := mappingState.Load()
			message, err = p.deanonymizeWebSocketFrame(mapping, message)
			if err != nil {
				return
			}
			if responseID := responseIDFromBody(message); responseID != "" {
				p.storeResponseMapping(responseID, mapping)
			}
		}
		if err := clientConn.WriteMessage(messageType, message); err != nil {
			return
		}
	}
}

func (p *OpenAI) closeWebSocket(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second),
	)
}

func (p *OpenAI) upstreamWebSocket(request *http.Request) (string, http.Header) {
	headers, chatGPTAuth := ResolveCodexRoutingHeaders(request.Header)
	upstreamHeaders := websocketForwardHeaders(headers)
	mergeOpenAIBeta(upstreamHeaders, RequiredResponsesWSBetaToken)
	if chatGPTAuth {
		return joinWebSocketURL(p.chatGPTTarget, "/backend-api/codex/responses"), upstreamHeaders
	}
	return joinWebSocketURL(p.apiTarget, "/v1/responses"), upstreamHeaders
}

func (p *OpenAI) anonymizeWebSocketFrame(mapping *ResponseRestoreMapping, message []byte) (*ResponseRestoreMapping, []byte, map[anonymizer.EntityType]int, []anonymizer.Finding, error) {
	payload, err := decodeJSONObject(message)
	if err != nil {
		return mapping, nil, nil, nil, err
	}
	matches, err := ner.AnalyzeStrings(context.Background(), p.nerAnalyzer, openAIUserPromptTexts(payload))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	run := p.newRun(matches)
	statsBefore := cloneEntityCountMap(run.stats)
	findingsBefore := len(run.findings)
	if response, ok := payload["response"]; ok {
		anonymized, changed := run.anonymizeResponsesValue(response, responsesContext{})
		if changed {
			payload["response"] = anonymized
		}
	} else {
		anonymized, changed := run.anonymizeResponsesValue(payload, responsesContext{})
		if changed {
			payload = anonymized.(map[string]any)
		}
	}
	output, err := encodeJSON(payload)
	if err != nil {
		return mapping, nil, nil, nil, err
	}
	nextMapping := p.mappingForPayload(previousResponseIDFromPayload(payload))
	if mapping != nil && nextMapping.Empty() {
		nextMapping = mapping.Clone()
	}
	findings := append([]anonymizer.Finding(nil), run.findings[findingsBefore:]...)
	replacements := nextMapping.MergeFindings(findings)
	if len(replacements) > 0 {
		output = rewriteBodyTokens(output, replacements)
		findings = rewriteFindingsTokens(findings, replacements)
	}
	if nextMapping.Empty() {
		nextMapping = nil
	}
	return nextMapping, output, entityCountDelta(statsBefore, run.stats), findings, nil
}

func (p *OpenAI) deanonymizeWebSocketFrame(mapping *ResponseRestoreMapping, message []byte) ([]byte, error) {
	output, _, err := restoreJSONBody(mapping, message)
	if err != nil {
		return message, nil
	}
	return output, nil
}

func (p *OpenAI) anonymizeResponsesBody(ctx context.Context, body []byte) (AnonymizeResult, error) {
	payload, err := decodeJSONObject(body)
	if err != nil {
		return AnonymizeResult{}, err
	}
	matches, err := ner.AnalyzeStrings(ctx, p.nerAnalyzer, openAIUserPromptTexts(payload))
	if err != nil {
		return AnonymizeResult{}, err
	}
	run := p.newRun(matches)
	restoreMapping := p.mappingForPayload(previousResponseIDFromPayload(payload))
	anonymized, changed := run.anonymizeResponsesValue(payload, responsesContext{})
	output := body
	if changed {
		output, err = encodeJSON(anonymized)
		if err != nil {
			return AnonymizeResult{}, err
		}
	}
	replacements := restoreMapping.MergeFindings(run.findings)
	if len(replacements) > 0 {
		output = rewriteBodyTokens(output, replacements)
		run.findings = rewriteFindingsTokens(run.findings, replacements)
	}
	if restoreMapping.Empty() {
		restoreMapping = nil
	}
	return AnonymizeResult{Body: output, RestoreMapping: restoreMapping, Stats: run.stats, Findings: run.findings}, nil
}

func (p *OpenAI) anonymizeChatCompletionsBody(ctx context.Context, body []byte) (AnonymizeResult, error) {
	payload, err := decodeJSONObject(body)
	if err != nil {
		return AnonymizeResult{}, err
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return AnonymizeResult{}, fmt.Errorf("chat completions messages must be an array")
	}
	matches, err := ner.AnalyzeStrings(ctx, p.nerAnalyzer, openAIChatUserPromptTexts(messages))
	if err != nil {
		return AnonymizeResult{}, err
	}
	run := p.newRun(matches)
	changed := false
	for index, message := range messages {
		messageMap, ok := message.(map[string]any)
		if !ok {
			return AnonymizeResult{}, fmt.Errorf("chat completions message %d must be an object", index)
		}
		messageChanged := run.anonymizeChatMessage(messageMap)
		changed = changed || messageChanged
	}
	if !changed {
		return AnonymizeResult{Body: body, RestoreMapping: NewResponseRestoreMapping(run.findings), Stats: run.stats, Findings: run.findings}, nil
	}
	output, err := encodeJSON(payload)
	if err != nil {
		return AnonymizeResult{}, err
	}
	return AnonymizeResult{Body: output, RestoreMapping: NewResponseRestoreMapping(run.findings), Stats: run.stats, Findings: run.findings}, nil
}

func (p *OpenAI) DeanonymizeHTTPResponse(mapping *ResponseRestoreMapping, response *http.Response) error {
	if err := restoreHTTPJSONResponse(mapping, response); err != nil {
		return err
	}
	if responseID := responseIDFromHTTPResponse(response); responseID != "" {
		p.storeResponseMapping(responseID, mapping)
	}
	return nil
}

func (p *OpenAI) newRun(matches ner.MatchSet) *openAIAnonymizationRun {
	return &openAIAnonymizationRun{
		engine:     p.anonymizer.NewRun(),
		stats:      make(map[anonymizer.EntityType]int),
		nerMatches: matches,
	}
}

func (p *OpenAI) recordAnonymizedStats(stats map[anonymizer.EntityType]int, findings []anonymizer.Finding) {
	if len(stats) == 0 {
		recordStatsEvent(p.logger, p.statsRecorder, requestProcessedEvent(stats))
		return
	}
	event := p.logger.Info()
	for _, entityType := range sortedEntityTypes(stats) {
		event = event.Int(string(entityType), stats[entityType])
	}
	_ = findings
	event.Msg("request body anonymized")
	recordStatsEvent(p.logger, p.statsRecorder, requestProcessedEvent(stats))
}

func (p *OpenAI) logTrafficFrame(direction string, stage string, payload []byte) {
	p.logger.Debug().
		Str("direction", direction).
		Str("stage", stage).
		Bytes("body", payload).
		Msg("websocket traffic")
}

type responsesContext struct {
	textValue bool
}

func (r *openAIAnonymizationRun) anonymizeResponsesValue(value any, context responsesContext) (any, bool) {
	switch typed := value.(type) {
	case string:
		if !context.textValue {
			return typed, false
		}
		return r.anonymizeString(typed)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := r.anonymizeResponsesValue(item, context)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			childContext := responsesChildContext(key, typed, context)
			anonymized, itemChanged := r.anonymizeResponsesValue(item, childContext)
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

func responsesChildContext(key string, parent map[string]any, context responsesContext) responsesContext {
	if key == "instructions" || key == "input" || key == "text" || key == "output" {
		return responsesContext{textValue: true}
	}
	if key == "content" && isResponsesContentParent(parent) {
		return responsesContext{textValue: true}
	}
	return responsesContext{textValue: context.textValue && !isOpenAIMetadataKey(key)}
}

func isResponsesContentParent(parent map[string]any) bool {
	switch stringMapValue(parent, "role") {
	case "user", "assistant", "tool", "function":
		return true
	}
	switch stringMapValue(parent, "type") {
	case "message", "input_text", "output_text", "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}

func (r *openAIAnonymizationRun) anonymizeChatMessage(message map[string]any) bool {
	changed := false
	if content, ok := message["content"]; ok {
		anonymized, itemChanged := r.anonymizeChatContent(content)
		if itemChanged {
			message["content"] = anonymized
			changed = true
		}
	}
	if toolCalls, ok := message["tool_calls"]; ok {
		anonymized, itemChanged := r.anonymizeToolCalls(toolCalls)
		if itemChanged {
			message["tool_calls"] = anonymized
			changed = true
		}
	}
	return changed
}

func (r *openAIAnonymizationRun) anonymizeChatContent(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		return r.anonymizeString(typed)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := r.anonymizeChatContent(item)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			if key != "text" && key != "content" && key != "input" && key != "output" {
				continue
			}
			anonymized, itemChanged := r.anonymizeChatContent(item)
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

func (r *openAIAnonymizationRun) anonymizeToolCalls(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		return r.anonymizeString(typed)
	case []any:
		changed := false
		for index, item := range typed {
			anonymized, itemChanged := r.anonymizeToolCalls(item)
			if itemChanged {
				typed[index] = anonymized
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			if isOpenAIMetadataKey(key) {
				continue
			}
			anonymized, itemChanged := r.anonymizeToolCalls(item)
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

func (r *openAIAnonymizationRun) anonymizeString(value string) (string, bool) {
	anonymized, result := r.engine.AnonymizeWithMatches(value, r.nerMatches[value])
	if len(result.Stats) == 0 {
		return value, false
	}
	for entityType, stats := range result.Stats {
		r.stats[entityType] += stats.Count
	}
	r.findings = append(r.findings, result.Findings...)
	return anonymized, true
}

func openAIUserPromptTexts(payload map[string]any) []string {
	if payload == nil {
		return nil
	}
	if response, ok := payload["response"].(map[string]any); ok {
		return openAIUserPromptTexts(response)
	}
	var texts []string
	switch input := payload["input"].(type) {
	case string:
		texts = append(texts, input)
	case []any:
		texts = append(texts, openAIInputUserPromptTexts(input)...)
	case map[string]any:
		texts = append(texts, openAIInputItemUserPromptTexts(input)...)
	}
	return texts
}

func openAIInputUserPromptTexts(items []any) []string {
	var texts []string
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			if text, ok := item.(string); ok {
				texts = append(texts, text)
			}
			continue
		}
		texts = append(texts, openAIInputItemUserPromptTexts(object)...)
	}
	return texts
}

func openAIInputItemUserPromptTexts(item map[string]any) []string {
	if item == nil {
		return nil
	}
	if stringMapValue(item, "role") == "user" {
		return collectOpenAIChatContentTexts(item["content"])
	}
	switch stringMapValue(item, "type") {
	case "input_text", "text":
		if text, ok := item["text"].(string); ok {
			return []string{text}
		}
	}
	return nil
}

func openAIChatUserPromptTexts(messages []any) []string {
	var texts []string
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok || stringMapValue(message, "role") != "user" {
			continue
		}
		texts = append(texts, collectOpenAIChatContentTexts(message["content"])...)
	}
	return texts
}

func collectOpenAIChatContentTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var texts []string
		for _, item := range typed {
			texts = append(texts, collectOpenAIChatContentTexts(item)...)
		}
		return texts
	case map[string]any:
		var texts []string
		for key, item := range typed {
			if key != "text" && key != "content" && key != "input" {
				continue
			}
			texts = append(texts, collectOpenAIChatContentTexts(item)...)
		}
		return texts
	default:
		return nil
	}
}

func (p *OpenAI) chatGPTModelsList(ctx context.Context, headers http.Header, clientVersion string) ([]byte, int, error) {
	entries, err := p.fetchChatGPTModelEntries(ctx, headers, clientVersion)
	if err != nil {
		return encodeSyntheticModelsList(), http.StatusOK, err
	}
	return encodeModelsList(entries), http.StatusOK, nil
}

func (p *OpenAI) chatGPTModelGet(ctx context.Context, headers http.Header, modelID, clientVersion string) ([]byte, int, error) {
	entries, err := p.fetchChatGPTModelEntries(ctx, headers, clientVersion)
	if err != nil {
		return encodeSyntheticModelGet(modelID), syntheticModelGetStatus(modelID), err
	}
	for _, entry := range entries {
		if entry.Slug == modelID {
			return encodeModelGet(modelID), http.StatusOK, nil
		}
	}
	return encodeModelNotFound(modelID), http.StatusNotFound, nil
}

type codexModelEntry struct {
	Slug string `json:"slug"`
}

func (p *OpenAI) fetchChatGPTModelEntries(ctx context.Context, headers http.Header, clientVersion string) ([]codexModelEntry, error) {
	upstreamURL := *p.chatGPTTarget
	upstreamURL.Path = "/backend-api/codex/models"
	query := upstreamURL.Query()
	if clientVersion != "" {
		query.Set("client_version", clientVersion)
	}
	upstreamURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header = modelRegistryHeaders(headers)
	response, err := p.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("model registry status %d", response.StatusCode)
	}
	var payload struct {
		Models []codexModelEntry `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Models) == 0 {
		return nil, fmt.Errorf("model registry returned no models")
	}
	return payload.Models, nil
}

func (p *OpenAI) mappingForPayload(previousResponseID string) *ResponseRestoreMapping {
	if strings.TrimSpace(previousResponseID) == "" {
		return &ResponseRestoreMapping{
			tokenToValue: make(map[string]string),
			valueToToken: make(map[string]string),
			nextID:       make(map[anonymizer.EntityType]int),
		}
	}
	p.responseMappingsM.RLock()
	mapping := p.responseMappings[previousResponseID]
	p.responseMappingsM.RUnlock()
	if mapping == nil {
		return &ResponseRestoreMapping{
			tokenToValue: make(map[string]string),
			valueToToken: make(map[string]string),
			nextID:       make(map[anonymizer.EntityType]int),
		}
	}
	return mapping.Clone()
}

func (p *OpenAI) storeResponseMapping(responseID string, mapping *ResponseRestoreMapping) {
	if strings.TrimSpace(responseID) == "" || mapping == nil || mapping.Empty() {
		return
	}
	p.responseMappingsM.Lock()
	p.responseMappings[responseID] = mapping.Clone()
	p.responseMappingsM.Unlock()
}

func previousResponseIDFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if previousResponseID := stringMapValue(payload, "previous_response_id"); previousResponseID != "" {
		return previousResponseID
	}
	response, _ := payload["response"].(map[string]any)
	return stringMapValue(response, "previous_response_id")
}

func responseIDFromBody(body []byte) string {
	payload, err := decodeJSONObject(body)
	if err != nil {
		return ""
	}
	return responseIDFromPayload(payload)
}

func responseIDFromHTTPResponse(response *http.Response) string {
	if response == nil || response.Body == nil {
		return ""
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ""
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return responseIDFromBody(body)
}

func responseIDFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if responseID := stringMapValue(payload, "response_id"); responseID != "" {
		return responseID
	}
	if responseID := stringMapValue(payload, "id"); responseID != "" {
		return responseID
	}
	response, _ := payload["response"].(map[string]any)
	if responseID := stringMapValue(response, "id"); responseID != "" {
		return responseID
	}
	return ""
}

func NormalizeOpenAIPath(path string) string {
	if path == "/openai" {
		return "/"
	}
	if strings.HasPrefix(path, "/openai/") {
		return strings.TrimPrefix(path, "/openai")
	}
	return path
}

func ResolveCodexRoutingHeaders(headers http.Header) (http.Header, bool) {
	resolved := headers.Clone()
	if resolved.Get("ChatGPT-Account-ID") != "" {
		return resolved, true
	}
	if resolved.Get("chatgpt-account-id") != "" {
		return resolved, true
	}
	accountID := chatGPTAccountIDFromBearer(resolved.Get("Authorization"))
	if accountID == "" {
		accountID = chatGPTAccountIDFromBearer(resolved.Get("authorization"))
	}
	if accountID == "" {
		return resolved, false
	}
	resolved.Set("ChatGPT-Account-ID", accountID)
	return resolved, true
}

func copyResolvedHeaders(destination http.Header, source http.Header) {
	for name, values := range source {
		destination.Del(name)
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func chatGPTAccountIDFromBearer(value string) string {
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || strings.Count(token, ".") < 2 {
		return ""
	}
	parts := strings.Split(token, ".")
	payload := parts[1]
	payload += strings.Repeat("=", (4-len(payload)%4)%4)
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal(decoded, &data); err != nil {
		return ""
	}
	if accountID, ok := data["https://api.openai.com/auth.chatgpt_account_id"].(string); ok {
		return strings.TrimSpace(accountID)
	}
	auth, _ := data["https://api.openai.com/auth"].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	return strings.TrimSpace(accountID)
}

func isCodexResponsesPath(path string) bool {
	_, ok := codexResponsePaths[path]
	return ok
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON content")
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON payload must be an object")
	}
	return object, nil
}

func encodeJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func isOpenAIMetadataKey(key string) bool {
	switch key {
	case "id", "model", "name", "role", "type", "tool_call_id", "call_id", "status", "encrypted_content":
		return true
	default:
		return false
	}
}

func stringMapValue(value map[string]any, key string) string {
	typed, _ := value[key].(string)
	return typed
}

func isWebSocketUpgrade(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("Upgrade"), "websocket")
}

func parseSubprotocols(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	protocols := make([]string, 0, len(parts))
	for _, part := range parts {
		if protocol := strings.TrimSpace(part); protocol != "" {
			protocols = append(protocols, protocol)
		}
	}
	return protocols
}

func websocketForwardHeaders(headers http.Header) http.Header {
	forwarded := make(http.Header)
	for name, values := range headers {
		if _, skip := hopByHopHeaders[strings.ToLower(name)]; skip {
			continue
		}
		for _, value := range values {
			forwarded.Add(name, value)
		}
	}
	return forwarded
}

func modelRegistryHeaders(headers http.Header) http.Header {
	forwarded := websocketForwardHeaders(headers)
	forwarded.Del("Accept")
	forwarded.Set("accept", "application/json")
	if accountID := forwarded.Get("ChatGPT-Account-ID"); accountID != "" {
		forwarded.Set("chatgpt-account-id", accountID)
		forwarded.Del("ChatGPT-Account-ID")
	}
	return forwarded
}

func mergeOpenAIBeta(headers http.Header, requiredToken string) {
	existing := headers.Get("OpenAI-Beta")
	tokens := make([]string, 0)
	seen := make(map[string]struct{})
	for _, part := range strings.Split(existing, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		key := strings.ToLower(token)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tokens = append(tokens, token)
	}
	key := strings.ToLower(requiredToken)
	if _, ok := seen[key]; !ok {
		tokens = append(tokens, requiredToken)
	}
	headers.Del("OpenAI-Beta")
	headers.Set("OpenAI-Beta", strings.Join(tokens, ", "))
}

func joinWebSocketURL(target *url.URL, path string) string {
	joined := *target
	switch joined.Scheme {
	case "http":
		joined.Scheme = "ws"
	case "https":
		joined.Scheme = "wss"
	}
	joined.Path = path
	joined.RawQuery = ""
	return joined.String()
}

var syntheticCodexModels = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.3",
	"gpt-5.2",
	"gpt-5.1",
	"gpt-5",
}

func encodeSyntheticModelsList() []byte {
	entries := make([]codexModelEntry, 0, len(syntheticCodexModels))
	for _, modelID := range syntheticCodexModels {
		entries = append(entries, codexModelEntry{Slug: modelID})
	}
	return encodeModelsList(entries)
}

func encodeModelsList(entries []codexModelEntry) []byte {
	models := make([]map[string]any, 0, len(entries))
	data := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Slug) == "" {
			continue
		}
		data = append(data, modelObject(entry.Slug))
		models = append(models, codexRegistryEntry(entry.Slug))
	}
	payload := map[string]any{
		"object": "list",
		"data":   data,
		"models": models,
	}
	output, _ := encodeJSON(payload)
	return output
}

func encodeSyntheticModelGet(modelID string) []byte {
	if syntheticModelGetStatus(modelID) == http.StatusNotFound {
		return encodeModelNotFound(modelID)
	}
	return encodeModelGet(modelID)
}

func syntheticModelGetStatus(modelID string) int {
	for _, known := range syntheticCodexModels {
		if modelID == known {
			return http.StatusOK
		}
	}
	return http.StatusNotFound
}

func encodeModelGet(modelID string) []byte {
	output, _ := encodeJSON(modelObject(modelID))
	return output
}

func encodeModelNotFound(modelID string) []byte {
	output, _ := encodeJSON(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("Model %q not available under ChatGPT auth", modelID),
			"type":    "invalid_request_error",
			"code":    "model_not_found",
		},
	})
	return output
}

func modelObject(modelID string) map[string]any {
	return map[string]any{
		"id":       modelID,
		"object":   "model",
		"created":  0,
		"owned_by": "openai",
	}
}

func codexRegistryEntry(modelID string) map[string]any {
	return map[string]any{
		"slug":                         modelID,
		"display_name":                 displayNameFromModelID(modelID),
		"description":                  "Codex model available through ChatGPT subscription auth.",
		"default_reasoning_level":      "medium",
		"supported_reasoning_levels":   []map[string]string{{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}, {"effort": "xhigh"}},
		"shell_type":                   "shell_command",
		"visibility":                   "list",
		"supported_in_api":             true,
		"priority":                     50,
		"context_window":               272000,
		"max_context_window":           272000,
		"supports_parallel_tool_calls": true,
		"supports_reasoning_summaries": true,
	}
}

func displayNameFromModelID(modelID string) string {
	parts := strings.Split(modelID, "-")
	for index, part := range parts {
		if part == "gpt" {
			parts[index] = "GPT"
		} else if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "-")
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

func cloneEntityCountMap(input map[anonymizer.EntityType]int) map[anonymizer.EntityType]int {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[anonymizer.EntityType]int, len(input))
	for entityType, count := range input {
		cloned[entityType] = count
	}
	return cloned
}

func entityCountDelta(before map[anonymizer.EntityType]int, after map[anonymizer.EntityType]int) map[anonymizer.EntityType]int {
	delta := make(map[anonymizer.EntityType]int, len(after))
	for entityType, count := range after {
		change := count - before[entityType]
		if change > 0 {
			delta[entityType] = change
		}
	}
	return delta
}

func recordStatsEvent(logger zerolog.Logger, recorder StatsRecorder, event statlog.Event) {
	if recorder == nil {
		return
	}
	if err := recorder.Record(event); err != nil {
		logger.Error().Err(err).Msg("stats record failed")
	}
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
