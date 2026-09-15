package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mustJSON marshals v to a compact JSON string (test helper).
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParseResponseValid covers the happy path.
func TestParseResponseValid(t *testing.T) {
	raw := []byte(mustJSON(t, map[string]any{
		"id": "chatcmpl-1", "model": "gpt-4o-mini",
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "  commit --amend  "}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}))
	r, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != "commit --amend" { // trimmed
		t.Errorf("Content = %q", r.Content)
	}
	if r.Model != "gpt-4o-mini" || r.Usage.TotalTokens != 15 {
		t.Errorf("meta mismatch: %+v", r)
	}
}

// TestParseResponseLegacyText handles legacy completions-style `text` field.
func TestParseResponseLegacyText(t *testing.T) {
	raw := []byte(`{"choices":[{"text":" checkout"}]}`)
	r, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != "checkout" {
		t.Errorf("legacy text = %q", r.Content)
	}
}

// TestParseResponseMalformed covers garbage bodies.
func TestParseResponseMalformed(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(""),
		[]byte("not json at all"),
		[]byte(`{"choices":`),
		[]byte(`[]`),
		[]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit"}}`),
		[]byte(`{"choices":[]}`),
		[]byte(`{"choices":[{"message":{"content":"   "}}]}`),
	} {
		if _, err := ParseResponse(raw); err == nil {
			t.Errorf("ParseResponse(%q) should fail", raw)
		}
	}
}

// TestParseResponseProviderError asserts error field yields typed error.
func TestParseResponseProviderError(t *testing.T) {
	raw := []byte(`{"error":{"message":"insufficient_quota","type":"insufficient_quota"}}`)
	_, err := ParseResponse(raw)
	if err == nil || !strings.Contains(err.Error(), "insufficient_quota") {
		t.Errorf("expected provider error, got %v", err)
	}
}

// TestEndpointNormalization covers base_url variants.
func TestEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com":                     "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/":                    "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1":                  "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/":                 "https://api.openai.com/v1/chat/completions",
		"https://api.deepseek.com/v1":                "https://api.deepseek.com/v1/chat/completions",
		"http://127.0.0.1:9999":                      "http://127.0.0.1:9999/v1/chat/completions",
		"https://gw.example.com/v1/chat/completions": "https://gw.example.com/v1/chat/completions",
	}
	for in, want := range cases {
		c := New(in, "k", "m", time.Second)
		got, err := c.Endpoint()
		if err != nil {
			t.Errorf("Endpoint(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Endpoint(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := New("", "k", "m", time.Second).Endpoint(); err == nil {
		t.Error("empty base_url should error")
	}
}

// TestCompleteIntegration exercises the full client against an httptest server:
// verifies URL, method, auth header, payload and parsed response.
func TestCompleteIntegration(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"m-test","choices":[{"message":{"content":"suffix"}}],"usage":{"total_tokens":7}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-key-value", "m-test", 2*time.Second)
	resp, err := c.Complete(context.Background(), Request{
		Model: "m-test", Temperature: 0.2, MaxTokens: 64,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "suffix" {
		t.Errorf("content = %q", resp.Content)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer fake-key-value" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"m-test"`) || !strings.Contains(gotBody, `"max_tokens":64`) {
		t.Errorf("body = %s", gotBody)
	}
}

// TestCompleteHTTP500 verifies non-200 returns ErrNon200.
func TestCompleteHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "m", time.Second)
	_, err := c.Complete(context.Background(), Request{Model: "m"})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	var e *ErrNon200
	if !errorsAs(err, &e) || e.Status != 500 {
		t.Errorf("expected ErrNon200{500}, got %v", err)
	}
}

// TestCompleteTimeout verifies context timeout surfaces as error (silent-degrade path).
func TestCompleteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "m", 50*time.Millisecond)
	start := time.Now()
	_, err := c.Complete(context.Background(), Request{Model: "m"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > time.Second {
		t.Errorf("timeout took too long: %v", time.Since(start))
	}
}

// TestCompleteNetworkDown verifies connection refused errors.
func TestCompleteNetworkDown(t *testing.T) {
	c := New("http://127.0.0.1:1", "k", "m", 300*time.Millisecond)
	if _, err := c.Complete(context.Background(), Request{Model: "m"}); err == nil {
		t.Fatal("expected network error")
	}
}

// TestCompleteNoKey verifies requests work without a key (header simply absent).
func TestCompleteNoKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "", "m", time.Second)
	if _, err := c.Complete(context.Background(), Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("auth should be absent, got %q", gotAuth)
	}
}

// errorsAs is a tiny helper (errors.As needs a non-nil target pointer-to-interface).
func errorsAs(err error, target **ErrNon200) bool {
	e, ok := err.(*ErrNon200)
	if ok {
		*target = e
	}
	return ok
}

func TestEndpointErrors(t *testing.T) {
	if _, err := New("", "k", "m", time.Second).Endpoint(); err == nil {
		t.Error("empty base_url should error")
	}
	if _, err := New("://bad", "k", "m", time.Second).Endpoint(); err == nil {
		t.Error("bad base_url should error")
	}
	e, err := New("https://api.example.com", "k", "m", time.Second).Endpoint()
	if err != nil || e != "https://api.example.com/v1/chat/completions" {
		t.Errorf("endpoint = %q %v", e, err)
	}
}
