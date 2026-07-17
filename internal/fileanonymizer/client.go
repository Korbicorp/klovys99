package fileanonymizer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/ner"
	"github.com/rs/zerolog"
	resty "gopkg.in/resty.v1"
	_ "modernc.org/sqlite"
)

const (
	ModeFull                  = "full"
	ModeOff                   = "off"
	PolicyRemove              = "remove"
	PolicyReject              = "reject"
	PolicyPassthrough         = "passthrough"
	DefaultURL                = "http://127.0.0.1:8092"
	DefaultMaxFileBytes int64 = 50 << 20
)

var ErrFileAnonymization = errors.New("presidio file anonymization failed")

type TextAnonymizer interface{ NewRun() *anonymizer.Run }

type Config struct {
	Mode          string
	URL           string
	Timeout       time.Duration
	MaxFileBytes  int64
	FailurePolicy string
	HTTPClient    *http.Client
	Anonymizer    TextAnonymizer
	Logger        *zerolog.Logger
	RegistryPath  string
	NERAnalyzer   ner.Analyzer
}

type Client struct {
	mode, failurePolicy string
	base                *url.URL
	readyEndpoint       *url.URL
	http                *http.Client
	resty               *resty.Client
	maxBytes            int64
	anonymizer          TextAnonymizer
	logger              zerolog.Logger
	trustedMu           sync.RWMutex
	trusted             map[string]struct{}
	registry            *sql.DB
	nerAnalyzer         ner.Analyzer
}

type extractResponse struct {
	Segments []segment `json:"segments"`
}
type segment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type replacement struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func NormalizeMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", ModeOff:
		return ModeOff
	case ModeFull:
		return ModeFull
	}
	return ""
}
func NormalizeFailurePolicy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", PolicyRemove:
		return PolicyRemove
	case PolicyReject:
		return PolicyReject
	case PolicyPassthrough:
		return PolicyPassthrough
	}
	return ""
}

func New(config Config) (*Client, error) {
	mode := NormalizeMode(config.Mode)
	if mode == "" {
		return nil, fmt.Errorf("invalid presidio mode %q", config.Mode)
	}
	policy := NormalizeFailurePolicy(config.FailurePolicy)
	if policy == "" {
		return nil, fmt.Errorf("invalid presidio failure policy %q", config.FailurePolicy)
	}
	if strings.TrimSpace(config.URL) == "" {
		config.URL = DefaultURL
	}
	base, err := url.Parse(config.URL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid presidio URL")
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = DefaultMaxFileBytes
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.Timeout}
	}
	if mode == ModeFull && config.Anonymizer == nil {
		return nil, errors.New("presidio text anonymizer is required")
	}
	logger := zerolog.Nop()
	if config.Logger != nil {
		logger = *config.Logger
	}
	client := &Client{mode: mode, failurePolicy: policy, base: base, http: config.HTTPClient, resty: resty.NewWithClient(config.HTTPClient), maxBytes: config.MaxFileBytes, anonymizer: config.Anonymizer, logger: logger, trusted: make(map[string]struct{}), nerAnalyzer: config.NERAnalyzer}
	readyEndpoint := *base
	readyEndpoint.Path = strings.TrimRight(readyEndpoint.Path, "/") + "/readyz"
	client.readyEndpoint = &readyEndpoint
	if strings.TrimSpace(config.RegistryPath) != "" {
		db, dbErr := sql.Open("sqlite", config.RegistryPath)
		if dbErr != nil {
			return nil, fmt.Errorf("open presidio file registry: %w", dbErr)
		}
		if _, dbErr = db.Exec(`CREATE TABLE IF NOT EXISTS anonymized_files (provider TEXT NOT NULL, file_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(provider, file_id))`); dbErr != nil {
			db.Close()
			return nil, fmt.Errorf("initialize presidio file registry: %w", dbErr)
		}
		client.registry = db
	}
	return client, nil
}

func (c *Client) Enabled() bool { return c != nil && c.mode == ModeFull }

func (c *Client) FailurePolicy() string {
	if c == nil {
		return PolicyPassthrough
	}
	return c.failurePolicy
}

func (c *Client) Probe(ctx context.Context) error {
	if c == nil || !c.Enabled() {
		return nil
	}
	var payload struct {
		Status string `json:"status"`
	}
	response, err := c.resty.R().
		SetContext(ctx).
		ExpectContentType("application/json").
		SetResult(&payload).
		Get(c.readyEndpoint.String())
	if err != nil {
		return fmt.Errorf("%w: ready probe: %v", ErrFileAnonymization, err)
	}
	if response.StatusCode() != http.StatusOK {
		return presidioStatusError("ready", response)
	}
	if payload.Status != "ready" {
		return fmt.Errorf("%w: ready returned status %q", ErrFileAnonymization, payload.Status)
	}
	return nil
}

func (c *Client) RegisterFileID(provider, id string) {
	if c == nil || id == "" {
		return
	}
	c.trustedMu.Lock()
	c.trusted[provider+"\x00"+id] = struct{}{}
	c.trustedMu.Unlock()
	if c.registry != nil {
		if _, err := c.registry.Exec(`INSERT OR IGNORE INTO anonymized_files(provider, file_id) VALUES(?, ?)`, provider, id); err != nil {
			c.logger.Error().Err(err).Str("provider", provider).Msg("failed to persist anonymized file id")
		}
	}
}

func (c *Client) IsTrustedFileID(provider, id string) bool {
	if c == nil {
		return false
	}
	c.trustedMu.RLock()
	_, ok := c.trusted[provider+"\x00"+id]
	c.trustedMu.RUnlock()
	if ok {
		return true
	}
	if c.registry != nil {
		var exists int
		if c.registry.QueryRow(`SELECT 1 FROM anonymized_files WHERE provider = ? AND file_id = ?`, provider, id).Scan(&exists) == nil {
			return true
		}
	}
	return false
}

func (c *Client) AnonymizePlainText(data []byte) ([]byte, anonymizer.Result) {
	result := anonymizer.Result{Stats: make(map[anonymizer.EntityType]anonymizer.EntityStats)}
	if c == nil || c.anonymizer == nil {
		return data, result
	}
	run := c.anonymizer.NewRun()
	defer run.Close()
	text, result := run.Anonymize(string(data))
	return []byte(text), result
}

func (c *Client) Anonymize(ctx context.Context, provider, mediaType string, data []byte) ([]byte, anonymizer.Result, error) {
	result := anonymizer.Result{Stats: make(map[anonymizer.EntityType]anonymizer.EntityStats)}
	if !c.Enabled() {
		return data, result, nil
	}
	if int64(len(data)) > c.maxBytes {
		return nil, result, fmt.Errorf("%w: file exceeds %d bytes", ErrFileAnonymization, c.maxBytes)
	}
	c.logger.Info().
		Str("provider", provider).
		Str("file_type", mediaType).
		Int("file_size_bytes", len(data)).
		Msg("sending file to Presidio")
	started := time.Now()
	status := "error"
	stage := "extract"
	var failure error
	count := 0
	defer func() {
		event := c.logger.Info().Str("provider", provider).Str("file_type", mediaType).Int("file_size_bytes", len(data)).Str("elapsed", time.Since(started).String()).Str("status", status).Str("stage", stage).Int("replacement_count", count).Str("failure_policy", c.failurePolicy)
		if failure != nil {
			event = event.Err(failure)
		}
		event.Msg("Presidio exec time")
	}()
	extracted, err := c.extract(ctx, mediaType, data)
	if err != nil {
		failure = err
		return nil, result, err
	}
	stage = "analyze"
	run := c.anonymizer.NewRun()
	defer run.Close()
	texts := make([]string, 0, len(extracted.Segments))
	for _, item := range extracted.Segments {
		texts = append(texts, item.Text)
	}
	matches, err := ner.AnalyzeStrings(ctx, c.nerAnalyzer, texts)
	if err != nil {
		failure = fmt.Errorf("%w: analyze extracted text: %v", ErrFileAnonymization, err)
		return nil, result, failure
	}
	replacements := make([]replacement, 0, len(extracted.Segments))
	for _, item := range extracted.Segments {
		anonymized, itemResult := run.AnonymizeWithMatches(item.Text, matches[item.Text])
		for typ, s := range itemResult.Stats {
			count += s.Count
			current := result.Stats[typ]
			current.Count += s.Count
			result.Stats[typ] = current
		}
		result.Findings = append(result.Findings, itemResult.Findings...)
		replacements = append(replacements, replacement{ID: item.ID, Text: anonymized})
	}
	stage = "render"
	output, err := c.render(ctx, mediaType, data, replacements)
	if err != nil {
		failure = err
		return nil, result, err
	}
	status = "ok"
	stage = "complete"
	return output, result, nil
}

func (c *Client) extract(ctx context.Context, mediaType string, data []byte) (extractResponse, error) {
	var result extractResponse
	body, contentType, err := multipartBody(data, mediaType, nil)
	if err != nil {
		return result, err
	}
	response, err := c.resty.R().
		SetContext(ctx).
		SetHeader("Content-Type", contentType).
		ExpectContentType("application/json").
		SetBody(body).
		SetResult(&result).
		Post(c.base.ResolveReference(&url.URL{Path: "/v1/extract"}).String())
	if err != nil {
		return result, fmt.Errorf("%w: extract: %v", ErrFileAnonymization, err)
	}
	if response.StatusCode() != http.StatusOK {
		return result, presidioStatusError("extract", response)
	}
	return result, nil
}

func (c *Client) render(ctx context.Context, mediaType string, data []byte, replacements []replacement) ([]byte, error) {
	payload, _ := json.Marshal(replacements)
	body, contentType, err := multipartBody(data, mediaType, payload)
	if err != nil {
		return nil, err
	}
	response, err := c.resty.R().
		SetContext(ctx).
		SetHeader("Content-Type", contentType).
		SetBody(body).
		Post(c.base.ResolveReference(&url.URL{Path: "/v1/render"}).String())
	if err != nil {
		return nil, fmt.Errorf("%w: render: %v", ErrFileAnonymization, err)
	}
	if response.StatusCode() != http.StatusOK {
		return nil, presidioStatusError("render", response)
	}
	output := response.Body()
	if int64(len(output)) > c.maxBytes {
		return nil, fmt.Errorf("%w: invalid render response", ErrFileAnonymization)
	}
	return output, nil
}

func presidioStatusError(stage string, response interface {
	StatusCode() int
	Body() []byte
}) error {
	body := response.Body()
	if len(body) > 4096 {
		body = body[:4096]
	}
	var payload struct {
		Error string `json:"error"`
	}
	detail := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error) != "" {
		detail = strings.TrimSpace(payload.Error)
	}
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	if detail == "" {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrFileAnonymization, stage, response.StatusCode())
	}
	return fmt.Errorf("%w: %s returned HTTP %d: %s", ErrFileAnonymization, stage, response.StatusCode(), detail)
}

func multipartBody(data []byte, mediaType string, replacements []byte) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "document")
	if err != nil {
		return nil, "", err
	}
	if _, err = part.Write(data); err != nil {
		return nil, "", err
	}
	if err = writer.WriteField("media_type", mediaType); err != nil {
		return nil, "", err
	}
	if replacements != nil {
		if err = writer.WriteField("replacements", string(replacements)); err != nil {
			return nil, "", err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return &body, writer.FormDataContentType(), nil
}

func DecodeDataURL(value string) (string, []byte, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", nil, false
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return "", nil, false
	}
	header := value[5:comma]
	if !strings.HasSuffix(header, ";base64") {
		return "", nil, false
	}
	mediaType := strings.TrimSuffix(header, ";base64")
	decoded, err := base64.StdEncoding.DecodeString(value[comma+1:])
	return mediaType, decoded, err == nil
}

func EncodeDataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
