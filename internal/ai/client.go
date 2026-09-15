// Package ai implements a minimal OpenAI-compatible chat-completions client
// (POST {base_url}/v1/chat/completions). It is deliberately dependency-free
// beyond the standard library; response parsing is a pure function with 100%
// unit-test coverage. All failures surface as typed errors so the caller can
// silently degrade to the local engine.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Defaults for the client.
const (
	DefaultTimeout    = 5 * time.Second
	DefaultMaxRetries = 0 // no retries: AI must never block typing for long
)

// Usage mirrors the OpenAI usage object.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is the parsed completion result.
type Response struct {
	Content string `json:"content"`
	Model   string `json:"model"`
	Usage   Usage  `json:"usage"`
}

// Request is a chat completion request.
type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// ErrNon200 carries the HTTP status for non-2xx responses.
type ErrNon200 struct {
	Status int
	Body   string
}

// Error implements error.
func (e *ErrNon200) Error() string {
	return fmt.Sprintf("ai: HTTP %d: %s", e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	BaseURL string // e.g. "https://api.openai.com" (no trailing slash, no /v1)
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// New builds a Client; baseURL may include a /v1 suffix which is normalized.
func New(baseURL, apiKey, model string, timeout time.Duration) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// Endpoint returns the chat completions URL.
func (c *Client) Endpoint() (string, error) {
	if c.BaseURL == "" {
		return "", fmt.Errorf("ai: base_url not configured")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", fmt.Errorf("ai: invalid base_url: %w", err)
	}
	if strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/chat/completions"
	} else if strings.HasSuffix(u.Path, "/v1/") {
		u.Path += "chat/completions"
	} else if strings.HasSuffix(u.Path, "/chat/completions") {
		// already full endpoint
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/chat/completions"
	}
	return u.String(), nil
}

// Complete performs one chat completion request. ctx controls cancellation
// and timeout. Any failure (network, non-200, malformed body) is returned as
// an error; callers must treat it as "AI unavailable, use local".
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	endpoint, err := c.Endpoint()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ErrNon200{Status: resp.StatusCode, Body: string(raw)}
	}
	out, err := ParseResponse(raw)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseResponse extracts the completion from a raw OpenAI-format JSON body.
// Pure function: unit-testable without network.
func ParseResponse(raw []byte) (*Response, error) {
	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"` // legacy completions fallback
		} `json:"choices"`
		Usage Usage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("ai: malformed response JSON: %w", err)
	}
	if envelope.Error != nil && envelope.Error.Message != "" {
		return nil, fmt.Errorf("ai: provider error: %s (%s)", envelope.Error.Message, envelope.Error.Type)
	}
	if len(envelope.Choices) == 0 {
		return nil, fmt.Errorf("ai: response has no choices")
	}
	content := envelope.Choices[0].Message.Content
	if content == "" {
		content = envelope.Choices[0].Text // legacy style
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("ai: empty completion content")
	}
	return &Response{
		Content: content,
		Model:   envelope.Model,
		Usage:   envelope.Usage,
	}, nil
}
