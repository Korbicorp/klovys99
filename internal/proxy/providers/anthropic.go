package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/ner"
	"github.com/rs/zerolog"
)

const (
	sessionOpenTag         = "<session>"
	sessionCloseTag        = "</session>"
	systemReminderOpenTag  = "<system-reminder>"
	systemReminderCloseTag = "</system-reminder>"
)

var anthropicMetadataKeys = map[string]struct{}{
	"cache_control": {},
	"id":            {},
	"media_type":    {},
	"model":         {},
	"name":          {},
	"role":          {},
	"tool_use_id":   {},
	"type":          {},
}

type AnthropicConfig struct {
	RoutePrefix    string
	Anonymizer     TextAnonymizer
	LogPIIFindings bool
	NERAnalyzer    ner.Analyzer
	FileAnonymizer FileAnonymizer
}

type Anthropic struct {
	routePrefix    string
	anonymizer     TextAnonymizer
	logPIIFindings bool
	nerAnalyzer    ner.Analyzer
	fileAnonymizer FileAnonymizer
}

type anthropicAnonymizationRun struct {
	engine      *anonymizer.Run
	stats       map[anonymizer.EntityType]int
	findings    []anonymizer.Finding
	readToolIDs map[string]struct{}
	nerMatches  ner.MatchSet
}

type anthropicContext struct {
	conversationContent     bool
	textValue               bool
	preserveSystemReminders bool
}

func NewAnthropic(config AnthropicConfig) (*Anthropic, error) {
	if config.Anonymizer == nil {
		return nil, fmt.Errorf("anthropic anonymizer is required")
	}
	if strings.TrimSpace(config.RoutePrefix) == "" {
		config.RoutePrefix = "/anthropic"
	}
	return &Anthropic{
		routePrefix:    strings.TrimRight(config.RoutePrefix, "/"),
		anonymizer:     config.Anonymizer,
		logPIIFindings: config.LogPIIFindings,
		nerAnalyzer:    config.NERAnalyzer,
		fileAnonymizer: config.FileAnonymizer,
	}, nil
}

func (p *Anthropic) ShouldAnonymizeHTTP(request *http.Request) bool {
	if request.Method != http.MethodPost {
		return false
	}
	switch p.requestPath(request.URL.Path) {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/files":
		return true
	default:
		return false
	}
}

func (p *Anthropic) AnonymizeHTTPRequest(request *http.Request, logger zerolog.Logger, body []byte) (AnonymizeResult, error) {
	forceStreamFalse := false
	ctx := context.Background()
	if request != nil {
		ctx = request.Context()
		if p.requestPath(request.URL.Path) == "/v1/files" {
			output, contentType, err := anonymizeMultipartUpload(ctx, "anthropic", request.Header.Get("Content-Type"), body, p.fileAnonymizer)
			if err == nil {
				request.Header.Set("Content-Type", contentType)
			}
			return AnonymizeResult{Body: output}, err
		}
		forceStreamFalse = p.requestPath(request.URL.Path) == "/v1/messages"
	}
	return p.anonymize(ctx, logger, body, forceStreamFalse)
}

// Anonymize is retained for callers that use the regex-only compatibility API.
func (p *Anthropic) Anonymize(ctx context.Context, logger zerolog.Logger, body []byte) AnonymizeResult {
	result, _ := p.anonymize(ctx, logger, body, false)
	return result
}

func (p *Anthropic) anonymize(ctx context.Context, logger zerolog.Logger, body []byte, forceStreamFalse bool) (AnonymizeResult, error) {
	var payload any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return AnonymizeResult{Body: body}, nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return AnonymizeResult{Body: body}, nil
	}
	filesChanged, fileResult, err := anonymizeInlineFiles(ctx, "anthropic", p.fileAnonymizer, payload)
	if err != nil {
		return AnonymizeResult{}, err
	}

	matches, err := ner.AnalyzeStrings(ctx, p.nerAnalyzer, anthropicUserPromptTexts(payload))
	if err != nil {
		return AnonymizeResult{}, err
	}
	run := p.newRun(matches)
	for typ, count := range fileResult.stats {
		run.stats[typ] += count
	}
	run.findings = append(run.findings, fileResult.findings...)
	run.readToolIDs = collectAnthropicReadToolIDs(payload)
	anonymized, changed := run.anonymizeRequestValue(payload, anthropicContext{})
	changed = changed || filesChanged
	if forceStreamFalse {
		changed = forceAnthropicNonStreaming(payload) || changed
	}
	if !changed {
		if len(run.stats) > 0 {
			logAnonymizedStats(logger, run.stats, run.findings, p.logPIIFindings)
		}
		return AnonymizeResult{Body: body, RestoreMapping: NewResponseRestoreMapping(run.findings), Stats: run.stats, Findings: run.findings}, nil
	}

	output, err := encodeJSON(anonymized)
	if err != nil {
		return AnonymizeResult{}, err
	}
	if len(run.stats) > 0 {
		logAnonymizedStats(logger, run.stats, run.findings, p.logPIIFindings)
	}
	return AnonymizeResult{
		Body:           output,
		RestoreMapping: NewResponseRestoreMapping(run.findings),
		Stats:          run.stats,
		Findings:       run.findings,
	}, nil
}

func forceAnthropicNonStreaming(payload any) bool {
	root, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	if current, ok := root["stream"].(bool); ok && !current {
		return false
	}
	root["stream"] = false
	return true
}

func (p *Anthropic) DeanonymizeHTTPResponse(mapping *ResponseRestoreMapping, response *http.Response) error {
	return restoreHTTPJSONResponseWithDebug(
		mapping,
		response,
		"[anthropic raw response]",
		"[anthropic restored response]",
	)
}

func (p *Anthropic) requestPath(path string) string {
	if path == p.routePrefix {
		return "/"
	}
	if p.routePrefix != "" && strings.HasPrefix(path, p.routePrefix+"/") {
		return strings.TrimPrefix(path, p.routePrefix)
	}
	return path
}

func (p *Anthropic) newRun(matches ner.MatchSet) *anthropicAnonymizationRun {
	return &anthropicAnonymizationRun{
		engine:     p.anonymizer.NewRun(),
		stats:      make(map[anonymizer.EntityType]int),
		nerMatches: matches,
	}
}

func (r *anthropicAnonymizationRun) anonymizeRequestValue(value any, context anthropicContext) (any, bool) {
	switch typed := value.(type) {
	case string:
		return r.anonymizeString(typed, context)
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
			if isAnthropicMetadataKey(key) {
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

func isAnthropicMetadataKey(key string) bool {
	_, ok := anthropicMetadataKeys[key]
	return ok
}

func (r *anthropicAnonymizationRun) childContext(key string, parent map[string]any, context anthropicContext) anthropicContext {
	if key == "content" && isAnthropicConversationMessage(parent) {
		return anthropicContext{
			conversationContent:     true,
			textValue:               isStringContent(parent, key),
			preserveSystemReminders: isStringContent(parent, key),
		}
	}

	if key == "source" {
		return anthropicContext{
			conversationContent: context.conversationContent,
		}
	}

	if key == "content" && stringMapValue(parent, "type") == "tool_result" {
		return anthropicContext{
			conversationContent: context.conversationContent,
			textValue:           r.isReadToolResult(parent),
		}
	}

	if key == "text" && stringMapValue(parent, "type") == "text" && context.conversationContent {
		return anthropicContext{
			conversationContent:     true,
			textValue:               true,
			preserveSystemReminders: true,
		}
	}

	if key == "thinking" && stringMapValue(parent, "type") == "thinking" && context.conversationContent {
		return anthropicContext{
			conversationContent: true,
			textValue:           true,
		}
	}

	if key == "data" && stringMapValue(parent, "type") == "text" && context.conversationContent {
		return anthropicContext{
			conversationContent: true,
			textValue:           true,
		}
	}

	return anthropicContext{
		conversationContent: context.conversationContent,
		textValue:           context.textValue && !isAnthropicMetadataKey(key),
	}
}

func isAnthropicConversationMessage(value map[string]any) bool {
	switch stringMapValue(value, "role") {
	case "user", "assistant":
		return true
	default:
		return false
	}
}

func isStringContent(value map[string]any, key string) bool {
	_, ok := value[key].(string)
	return ok
}

func (r *anthropicAnonymizationRun) isReadToolResult(value map[string]any) bool {
	toolUseID := stringMapValue(value, "tool_use_id")
	if toolUseID == "" {
		return false
	}
	_, ok := r.readToolIDs[toolUseID]
	return ok
}

func collectAnthropicReadToolIDs(value any) map[string]struct{} {
	ids := make(map[string]struct{})
	collectAnthropicReadToolIDsInto(value, ids)
	return ids
}

func collectAnthropicReadToolIDsInto(value any, ids map[string]struct{}) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectAnthropicReadToolIDsInto(item, ids)
		}
	case map[string]any:
		if stringMapValue(typed, "type") == "tool_use" && isFileReadToolName(stringMapValue(typed, "name")) {
			if id := stringMapValue(typed, "id"); id != "" {
				ids[id] = struct{}{}
			}
		}
		for _, item := range typed {
			collectAnthropicReadToolIDsInto(item, ids)
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

func (r *anthropicAnonymizationRun) anonymizeString(value string, context anthropicContext) (string, bool) {
	if !context.textValue {
		return value, false
	}
	if context.preserveSystemReminders {
		return r.anonymizeTextValuePreservingSystemReminders(value)
	}
	return r.anonymizeTextValue(value)
}

func (r *anthropicAnonymizationRun) anonymizeTextValue(value string) (string, bool) {
	anonymized, result := r.engine.AnonymizeWithMatches(value, r.nerMatches[value])
	if len(result.Stats) == 0 {
		return value, false
	}
	r.addStats(result)
	return anonymized, true
}

func (r *anthropicAnonymizationRun) anonymizeTextValuePreservingSystemReminders(value string) (string, bool) {
	if !strings.Contains(value, systemReminderOpenTag) {
		return r.anonymizeTextValue(value)
	}

	var output strings.Builder
	changed := false
	remaining := value
	base := 0
	for {
		openIndex := strings.Index(remaining, systemReminderOpenTag)
		if openIndex < 0 {
			anonymized, segmentChanged := r.anonymizeTextRange(remaining, value, base)
			output.WriteString(anonymized)
			return output.String(), changed || segmentChanged
		}

		closeSearchStart := openIndex + len(systemReminderOpenTag)
		closeIndex := strings.Index(remaining[closeSearchStart:], systemReminderCloseTag)
		if closeIndex < 0 {
			anonymized, segmentChanged := r.anonymizeTextRange(remaining, value, base)
			output.WriteString(anonymized)
			return output.String(), changed || segmentChanged
		}

		anonymized, segmentChanged := r.anonymizeTextRange(remaining[:openIndex], value, base)
		output.WriteString(anonymized)
		changed = changed || segmentChanged

		closeEnd := closeSearchStart + closeIndex + len(systemReminderCloseTag)
		output.WriteString(remaining[openIndex:closeEnd])
		remaining = remaining[closeEnd:]
		base += closeEnd
	}
}

func (r *anthropicAnonymizationRun) anonymizeTextRange(value, source string, base int) (string, bool) {
	if value == source {
		return r.anonymizeTextValue(value)
	}
	var projected []anonymizer.Match
	for _, match := range r.nerMatches[source] {
		if match.Start < base || match.End > base+len(value) {
			continue
		}
		match.Start -= base
		match.End -= base
		projected = append(projected, match)
	}
	anonymized, result := r.engine.AnonymizeWithMatches(value, projected)
	if len(result.Stats) == 0 {
		return value, false
	}
	r.addStats(result)
	return anonymized, true
}

func (r *anthropicAnonymizationRun) addStats(result anonymizer.Result) {
	for entityType, entityStats := range result.Stats {
		r.stats[entityType] += entityStats.Count
	}
	r.findings = append(r.findings, result.Findings...)
}

func logAnonymizedStats(logger zerolog.Logger, stats map[anonymizer.EntityType]int, findings []anonymizer.Finding, includePII bool) {
	event := logger.Info()
	for _, entityType := range sortedEntityTypes(stats) {
		event = event.Int(string(entityType), stats[entityType])
	}
	_ = findings
	_ = includePII
	event.Msg("request body anonymized")
}

func anthropicUserPromptTexts(payload any) []string {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return nil
	}
	var texts []string
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok || stringMapValue(message, "role") != "user" {
			continue
		}
		texts = append(texts, collectAnthropicUserContentTexts(message["content"])...)
	}
	return texts
}

func collectAnthropicUserContentTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var texts []string
		for _, item := range typed {
			texts = append(texts, collectAnthropicUserContentTexts(item)...)
		}
		return texts
	case map[string]any:
		switch stringMapValue(typed, "type") {
		case "text":
			var texts []string
			if text, ok := typed["text"].(string); ok {
				texts = append(texts, text)
			}
			if data, ok := typed["data"].(string); ok {
				texts = append(texts, data)
			}
			return texts
		default:
			return nil
		}
	default:
		return nil
	}
}
