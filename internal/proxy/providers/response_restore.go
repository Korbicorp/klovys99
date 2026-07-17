package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/rs/zerolog/log"
)

type ResponseRestoreMapping struct {
	tokenToValue map[string]string
	valueToToken map[string]string
	nextID       map[anonymizer.EntityType]int
}

func NewResponseRestoreMapping(findings []anonymizer.Finding) *ResponseRestoreMapping {
	mapping := &ResponseRestoreMapping{
		tokenToValue: make(map[string]string, len(findings)),
		valueToToken: make(map[string]string, len(findings)),
		nextID:       make(map[anonymizer.EntityType]int),
	}
	mapping.MergeFindings(findings)
	if mapping.Empty() {
		return nil
	}
	return mapping
}

func (m *ResponseRestoreMapping) Clone() *ResponseRestoreMapping {
	if m == nil {
		return nil
	}
	cloned := &ResponseRestoreMapping{
		tokenToValue: make(map[string]string, len(m.tokenToValue)),
		valueToToken: make(map[string]string, len(m.valueToToken)),
		nextID:       make(map[anonymizer.EntityType]int, len(m.nextID)),
	}
	for token, value := range m.tokenToValue {
		cloned.tokenToValue[token] = value
	}
	for key, token := range m.valueToToken {
		cloned.valueToToken[key] = token
	}
	for entityType, nextID := range m.nextID {
		cloned.nextID[entityType] = nextID
	}
	return cloned
}

func (m *ResponseRestoreMapping) Empty() bool {
	return m == nil || len(m.tokenToValue) == 0
}

func (m *ResponseRestoreMapping) MergeFindings(findings []anonymizer.Finding) map[string]string {
	if m == nil || len(findings) == 0 {
		return nil
	}

	replacements := make(map[string]string)
	for _, finding := range findings {
		desiredToken := m.tokenForFinding(finding)
		if desiredToken != finding.Token {
			replacements[finding.Token] = desiredToken
		}
	}
	if len(replacements) == 0 {
		return nil
	}
	return replacements
}

func (m *ResponseRestoreMapping) tokenForFinding(finding anonymizer.Finding) string {
	valueKey := responseRestoreValueKey(finding.Type, finding.Value)
	if token, ok := m.valueToToken[valueKey]; ok {
		return token
	}

	entityType, numericID, ok := responseTokenParts(finding.Token)
	if !ok || entityType != finding.Type || numericID <= m.nextID[finding.Type] || m.tokenToValue[finding.Token] != "" {
		numericID = m.nextID[finding.Type] + 1
	}
	if numericID > m.nextID[finding.Type] {
		m.nextID[finding.Type] = numericID
	}

	token := fmt.Sprintf("[%s_%d]", finding.Type, numericID)
	m.valueToToken[valueKey] = token
	m.tokenToValue[token] = finding.Value
	return token
}

func responseRestoreValueKey(entityType anonymizer.EntityType, value string) string {
	return string(entityType) + "\x00" + strings.ToLower(strings.TrimSpace(value))
}

func responseTokenParts(token string) (anonymizer.EntityType, int, bool) {
	if len(token) < 4 || token[0] != '[' || token[len(token)-1] != ']' {
		return "", 0, false
	}
	trimmed := token[1 : len(token)-1]
	separator := strings.LastIndex(trimmed, "_")
	if separator <= 0 || separator == len(trimmed)-1 {
		return "", 0, false
	}
	numericID, err := strconv.Atoi(trimmed[separator+1:])
	if err != nil {
		return "", 0, false
	}
	return anonymizer.EntityType(trimmed[:separator]), numericID, true
}

func rewriteFindingsTokens(findings []anonymizer.Finding, replacements map[string]string) []anonymizer.Finding {
	if len(findings) == 0 || len(replacements) == 0 {
		return findings
	}
	rewritten := make([]anonymizer.Finding, len(findings))
	for index, finding := range findings {
		rewritten[index] = finding
		if token, ok := replacements[finding.Token]; ok {
			rewritten[index].Token = token
		}
	}
	return rewritten
}

func rewriteBodyTokens(body []byte, replacements map[string]string) []byte {
	if len(body) == 0 || len(replacements) == 0 {
		return body
	}
	output := string(body)
	for _, token := range sortedRestoreTokens(replacements) {
		output = strings.ReplaceAll(output, token, replacements[token])
	}
	return []byte(output)
}

func restoreHTTPJSONResponse(mapping *ResponseRestoreMapping, response *http.Response) error {
	return restoreHTTPJSONResponseWithDebug(mapping, response, "", "")
}

func restoreHTTPJSONResponseWithDebug(mapping *ResponseRestoreMapping, response *http.Response, rawLabel string, restoredLabel string) error {
	if mapping == nil || mapping.Empty() || response == nil || response.Body == nil {
		return nil
	}
	_ = rawLabel
	_ = restoredLabel
	start := time.Now()
	path := ""
	if response.Request != nil && response.Request.URL != nil {
		path = response.Request.URL.Path
	}
	log.Debug().
		Str("step", "response_deanonymization").
		Str("path", path).
		Msg("debug step started")
	defer log.Debug().
		Str("step", "response_deanonymization").
		Str("path", path).
		Dur("elapsed", time.Since(start)).
		Msg("debug step finished")

	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return err
	}
	if len(body) == 0 {
		response.Body = io.NopCloser(bytes.NewReader(nil))
		response.ContentLength = 0
		response.Header.Set("Content-Length", "0")
		return nil
	}
	log.Debug().Str("body", string(body)).Msg("body before restore")
	output, changed, err := restoreJSONBody(mapping, body)
	if err != nil {
		output = body
		changed = false
	}
	if !changed {
		output = body
	}

	response.Body = io.NopCloser(bytes.NewReader(output))
	response.ContentLength = int64(len(output))
	response.Header.Set("Content-Length", strconv.Itoa(len(output)))
	return nil
}

func restoreJSONBody(mapping *ResponseRestoreMapping, body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false, err
	}

	restored, changed := restoreJSONValue(mapping, payload)
	if !changed {
		return body, false, nil
	}
	output, err := encodeJSON(restored)
	if err != nil {
		return nil, false, err
	}
	return output, true, nil
}

func restoreJSONValue(mapping *ResponseRestoreMapping, value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		return mapping.restoreString(typed)
	case []any:
		changed := false
		for index, item := range typed {
			restored, itemChanged := restoreJSONValue(mapping, item)
			if itemChanged {
				typed[index] = restored
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			restored, itemChanged := restoreJSONValue(mapping, item)
			if itemChanged {
				typed[key] = restored
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func (m *ResponseRestoreMapping) restoreString(value string) (string, bool) {
	if m == nil || len(m.tokenToValue) == 0 {
		return value, false
	}
	restored := value
	changed := false
	for _, token := range sortedRestoreTokens(m.tokenToValue) {
		original := m.tokenToValue[token]
		if !strings.Contains(restored, token) {
			continue
		}
		restored = strings.ReplaceAll(restored, token, original)
		changed = true
	}
	return restored, changed
}

func sortedRestoreTokens[V any](values map[string]V) []string {
	tokens := make([]string, 0, len(values))
	for token := range values {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool {
		if len(tokens[i]) == len(tokens[j]) {
			return tokens[i] < tokens[j]
		}
		return len(tokens[i]) > len(tokens[j])
	})
	return tokens
}
