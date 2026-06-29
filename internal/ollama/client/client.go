package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type Client struct {
	httpClient *resty.Client
}

func New(baseURL string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("ollama base URL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ollama URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("ollama URL must include scheme and host")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		httpClient: resty.New().
			SetBaseURL(strings.TrimRight(parsed.String(), "/")).
			SetTimeout(timeout),
	}, nil
}

func (c *Client) Generate(ctx context.Context, model, prompt string, format any) (string, error) {
	if c == nil {
		return "", fmt.Errorf("nil ollama client")
	}

	response, err := c.httpClient.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(generateRequest{
			Model:  model,
			Prompt: prompt,
			Stream: false,
			Format: format,
		}).
		Post("/api/generate")
	if err != nil {
		return "", fmt.Errorf("call ollama: %w", err)
	}
	if response.IsError() {
		return "", fmt.Errorf("ollama returned HTTP %d: %s", response.StatusCode(), strings.TrimSpace(response.String()))
	}

	var payload generateResponse
	if err := json.Unmarshal(response.Body(), &payload); err != nil {
		return "", fmt.Errorf("parse ollama response: %w", err)
	}
	if payload.Response == "" {
		return "", fmt.Errorf("ollama response is empty")
	}

	return payload.Response, nil
}

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format any    `json:"format"`
}

type generateResponse struct {
	Response string `json:"response"`
}
