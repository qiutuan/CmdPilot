// Package client is the HTTP client used by adapters, the companion binary
// and the CLI to talk to the local CmdPilot daemon over loopback.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qiutuan/CmdPilot/internal/completion"
)

// Defaults for the client.
const (
	DefaultTimeout = 2 * time.Second // complete must never block typing
	HealthTimeout  = 300 * time.Millisecond
)

// Client talks to one daemon instance.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client.
func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: DefaultTimeout}}
}

// request performs an authenticated JSON request.
func (c *Client) request(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("daemon unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon error: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("daemon bad response: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// CompleteReq mirrors completion.Request over the wire.
type CompleteReq struct {
	Input    string   `json:"input"`
	Shell    string   `json:"shell"`
	CWD      string   `json:"cwd"`
	History  []string `json:"history"`
	Trigger  string   `json:"trigger"`
	WaitAIMS int      `json:"wait_ai_ms"`
}

// Complete asks the daemon for suggestions.
func (c *Client) Complete(req CompleteReq) (*completion.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	var out completion.Response
	if err := c.request(ctx, http.MethodPost, "/complete", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReportUsage records an executed command (prompt-hook path).
func (c *Client) ReportUsage(command, dir, shell string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPost, "/report", map[string]string{
		"command": command, "dir": dir, "shell": shell,
	}, nil)
}

// Health checks liveness quickly.
func (c *Client) Health() (bool, string) {
	hc := &http.Client{Timeout: HealthTimeout}
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return false, ""
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode == http.StatusOK && out.OK, out.Version
}

// Shutdown asks the daemon to exit gracefully.
func (c *Client) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodPost, "/shutdown", nil, nil)
}

// AITest validates the configured AI endpoint and returns the model name.
func (c *Client) AITest() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct {
		OK    bool   `json:"ok"`
		Model string `json:"model"`
		Error string `json:"error"`
	}
	if err := c.request(ctx, http.MethodPost, "/ai/test", nil, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("AI test failed: %s", out.Error)
	}
	return out.Model, nil
}
