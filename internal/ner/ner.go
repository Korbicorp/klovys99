// Package ner implements the bounded, local contextual entity analyzer.
package ner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
)

const (
	DefaultURL            = "http://127.0.0.1:8091"
	DefaultTimeout        = 5 * time.Second
	DefaultThreshold      = 0.50
	DefaultMaxConcurrency = 2
	DefaultMaxQueue       = 16
	DefaultMaxBatchChars  = 32768
	DefaultPriority       = 650
	ModeOff               = "off"
	ModeFull              = "full"
)

var (
	ErrSaturated = errors.New("local NER queue is full")
	ErrTooLarge  = errors.New("local NER batch is too large")
)

// Analyzer is the provider-facing, context-aware batch boundary.
type Analyzer interface {
	AnalyzeBatch(ctx context.Context, texts []string) ([][]anonymizer.Match, error)
	Status() Status
}

type Config struct {
	Mode           string
	URL            string
	Model          string
	ModelRevision  string
	Timeout        time.Duration
	Threshold      float64
	LabelThreshold map[string]float64
	MaxConcurrency int
	MaxQueue       int
	MaxBatchChars  int
	HTTPClient     *http.Client
}

type Status struct {
	Enabled       bool      `json:"enabled"`
	State         string    `json:"state"`
	Mode          string    `json:"mode,omitempty"`
	Model         string    `json:"model,omitempty"`
	ModelRevision string    `json:"model_revision,omitempty"`
	LastSuccess   time.Time `json:"last_success,omitempty"`
	LastFailure   time.Time `json:"last_failure,omitempty"`
	LatencyMS     int64     `json:"latency_ms,omitempty"`
}

type Client struct {
	endpoint       *url.URL
	readyEndpoint  *url.URL
	model          string
	revision       string
	mode           string
	labels         []string
	timeout        time.Duration
	threshold      float64
	labelThreshold map[string]float64
	maxBatchChars  int
	httpClient     *http.Client
	slots          chan struct{}
	queue          chan struct{}
	mu             sync.RWMutex
	status         Status
}

type analyzeRequest struct {
	Texts          []string           `json:"texts"`
	Labels         []string           `json:"labels"`
	Threshold      float64            `json:"threshold"`
	LabelThreshold map[string]float64 `json:"label_thresholds,omitempty"`
	Model          string             `json:"model"`
	ModelRevision  string             `json:"model_revision"`
}

type analyzeResponse struct {
	Model         string             `json:"model"`
	ModelRevision string             `json:"model_revision"`
	Results       [][]responseEntity `json:"results"`
	LatencyMS     int64              `json:"latency_ms,omitempty"`
}

type responseEntity struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

var labelTypes = map[string]anonymizer.EntityType{
	"person name":                       anonymizer.EntityName,
	"organization":                      anonymizer.EntityOrganization,
	"location":                          anonymizer.EntityLocation,
	"employer":                          anonymizer.EntityEmployer,
	"school or educational institution": anonymizer.EntitySchool,
	"medical provider or healthcare institution": anonymizer.EntityMedicalProvider,
	"street address": anonymizer.EntityAddress,
}

func Labels() []string {
	return []string{
		"person name",
		"organization",
		"location",
		"employer",
		"school or educational institution",
		"medical provider or healthcare institution",
		"street address",
	}
}

func NormalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeFull:
		return ModeFull
	case ModeOff:
		return ModeOff
	default:
		return ""
	}
}

func NewClient(config Config) (*Client, error) {
	config.Mode = NormalizeMode(config.Mode)
	if config.Mode == "" {
		return nil, fmt.Errorf("GLiNER mode must be one of %q or %q", ModeFull, ModeOff)
	}
	if config.Mode == ModeOff {
		return nil, fmt.Errorf("GLiNER off mode should not create a client")
	}
	if config.URL == "" {
		config.URL = DefaultURL
	}
	endpoint, err := url.Parse(config.URL)
	if err != nil || endpoint.Scheme != "http" || !isLoopback(endpoint.Hostname()) {
		return nil, fmt.Errorf("KLOVIS_GLINER_URL must be an HTTP loopback endpoint")
	}
	if strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.ModelRevision) == "" {
		return nil, fmt.Errorf("GLiNER model and immutable revision are required")
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}
	if config.Threshold <= 0 || config.Threshold > 1 {
		config.Threshold = DefaultThreshold
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = DefaultMaxConcurrency
	}
	if config.MaxQueue <= 0 {
		config.MaxQueue = DefaultMaxQueue
	}
	if config.MaxBatchChars <= 0 {
		config.MaxBatchChars = DefaultMaxBatchChars
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/analyze"
	readyEndpoint := *endpoint
	readyEndpoint.Path = strings.TrimSuffix(endpoint.Path, "/v1/analyze") + "/readyz"
	client := &Client{
		endpoint:       endpoint,
		readyEndpoint:  &readyEndpoint,
		model:          strings.TrimSpace(config.Model),
		revision:       strings.TrimSpace(config.ModelRevision),
		mode:           config.Mode,
		labels:         Labels(),
		timeout:        config.Timeout,
		threshold:      config.Threshold,
		labelThreshold: config.LabelThreshold,
		maxBatchChars:  config.MaxBatchChars,
		httpClient:     config.HTTPClient,
		slots:          make(chan struct{}, config.MaxConcurrency),
		queue:          make(chan struct{}, config.MaxQueue),
		status: Status{
			Enabled:       true,
			State:         "unavailable",
			Mode:          config.Mode,
			Model:         strings.TrimSpace(config.Model),
			ModelRevision: strings.TrimSpace(config.ModelRevision),
		},
	}
	return client, nil
}

func (c *Client) Probe(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	started := time.Now()
	request, err := http.NewRequestWithContext(callCtx, http.MethodGet, c.readyEndpoint.String(), nil)
	if err != nil {
		c.failure()
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		c.failure()
		return fmt.Errorf("local NER unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		c.failure()
		return fmt.Errorf("local NER readiness status %d", response.StatusCode)
	}
	var payload struct {
		Status        string `json:"status"`
		Model         string `json:"model"`
		ModelRevision string `json:"model_revision"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil ||
		payload.Status != "ready" || payload.Model != c.model || payload.ModelRevision != c.revision {
		c.failure()
		return fmt.Errorf("local NER readiness identity mismatch")
	}
	c.success(time.Since(started))
	return nil
}

func DisabledStatus() Status {
	return Status{Enabled: false, State: "disabled", Mode: ModeOff}
}

func (c *Client) AnalyzeBatch(ctx context.Context, texts []string) ([][]anonymizer.Match, error) {
	if len(texts) == 0 {
		return make([][]anonymizer.Match, 0), nil
	}
	total := 0
	for _, text := range texts {
		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("local NER input is not valid UTF-8")
		}
		total += utf8.RuneCountInString(text)
	}
	if total > c.maxBatchChars {
		c.failure()
		return nil, ErrTooLarge
	}
	select {
	case c.queue <- struct{}{}:
	default:
		c.failure()
		return nil, ErrSaturated
	}
	defer func() { <-c.queue }()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		c.failure()
		return nil, ctx.Err()
	}

	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	payload, err := json.Marshal(analyzeRequest{
		Texts: texts, Labels: c.labels, Threshold: c.threshold,
		LabelThreshold: c.labelThreshold, Model: c.model, ModelRevision: c.revision,
	})
	if err != nil {
		c.failure()
		return nil, fmt.Errorf("encode local NER request: %w", err)
	}
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		c.failure()
		return nil, fmt.Errorf("create local NER request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		c.failure()
		return nil, fmt.Errorf("local NER unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		c.failure()
		return nil, fmt.Errorf("local NER returned status %d", response.StatusCode)
	}
	var decoded analyzeResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		c.failure()
		return nil, fmt.Errorf("invalid local NER response")
	}
	if decoded.Model != c.model || decoded.ModelRevision != c.revision {
		c.failure()
		return nil, fmt.Errorf("local NER model identity mismatch")
	}
	if len(decoded.Results) != len(texts) {
		c.failure()
		return nil, fmt.Errorf("local NER result count mismatch")
	}
	results := make([][]anonymizer.Match, len(texts))
	for index, entities := range decoded.Results {
		matches, err := convertEntities(texts[index], entities, c.threshold, c.labelThreshold)
		if err != nil {
			c.failure()
			return nil, err
		}
		results[index] = matches
	}
	c.success(time.Since(started))
	return results, nil
}

func convertEntities(text string, entities []responseEntity, threshold float64, thresholds map[string]float64) ([]anonymizer.Match, error) {
	runeToByte := make([]int, 0, utf8.RuneCountInString(text)+1)
	for index := range text {
		runeToByte = append(runeToByte, index)
	}
	runeToByte = append(runeToByte, len(text))
	matches := make([]anonymizer.Match, 0, len(entities))
	for _, entity := range entities {
		entityType, ok := labelTypes[entity.Label]
		if !ok {
			return nil, fmt.Errorf("local NER returned an unknown label")
		}
		required := threshold
		if value, exists := thresholds[entity.Label]; exists {
			required = value
		}
		if entity.Score < required {
			continue
		}
		if entity.Start < 0 || entity.End <= entity.Start || entity.End >= len(runeToByte) {
			return nil, fmt.Errorf("local NER returned an invalid span")
		}
		start, end := runeToByte[entity.Start], runeToByte[entity.End]
		matches = append(matches, anonymizer.Match{
			Start: start, End: end, Type: entityType, Priority: DefaultPriority,
			Normalized: strings.ToLower(strings.TrimSpace(text[start:end])),
		})
	}
	if hasOverlap(matches) {
		return nil, fmt.Errorf("local NER returned overlapping spans")
	}
	return matches, nil
}

func hasOverlap(matches []anonymizer.Match) bool {
	for i := range matches {
		for j := i + 1; j < len(matches); j++ {
			if matches[i].Start < matches[j].End && matches[j].Start < matches[i].End {
				return true
			}
		}
	}
	return false
}

func (c *Client) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Client) success(latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = "ready"
	c.status.LastSuccess = time.Now().UTC()
	c.status.LatencyMS = latency.Milliseconds()
}

func (c *Client) failure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = "unavailable"
	c.status.LastFailure = time.Now().UTC()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
