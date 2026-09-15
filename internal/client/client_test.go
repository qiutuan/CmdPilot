package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiutuan/CmdPilot/internal/completion"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "tok-123")
}

func TestClientComplete(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/complete" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth = %q", got)
		}
		var req CompleteReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Input != "git st" || req.Shell != "cmd" {
			t.Errorf("req = %+v", req)
		}
		_, _ = w.Write([]byte(`{"ok":true,"top":{"full":"git stash ","source":"knowledge"},"list":[{"full":"git stash "}],"recommendations":[]}`))
	})
	resp, err := c.Complete(CompleteReq{Input: "git st", Shell: "cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Top == nil || resp.Top.Full != "git stash " || resp.Top.Source != "knowledge" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestClientCompleteDaemonError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	})
	if _, err := c.Complete(CompleteReq{Input: "x"}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientCompleteBadJSON(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	})
	if _, err := c.Complete(CompleteReq{Input: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClientUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1", "t")
	if _, err := c.Complete(CompleteReq{Input: "x"}); err == nil {
		t.Fatal("expected unreachable error")
	}
}

func TestClientReportUsage(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/report" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m["command"] != "git stash" || m["shell"] != "cmd" {
			t.Errorf("body = %v", m)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if err := c.ReportUsage("git stash", "/tmp", "cmd"); err != nil {
		t.Fatal(err)
	}
}

func TestClientHealthOKAndDown(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"v1"}`))
	})
	ok, ver := c.Health()
	if !ok || ver != "v1" {
		t.Fatalf("health ok=%v ver=%q", ok, ver)
	}
	down := New("http://127.0.0.1:1", "t")
	if ok, _ := down.Health(); ok {
		t.Fatal("expected unhealthy")
	}
}

func TestClientShutdown(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shutdown" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if err := c.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestClientAITest(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/test" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"model":"mock-llm"}`))
	})
	model, err := c.AITest()
	if err != nil || model != "mock-llm" {
		t.Fatalf("model=%q err=%v", model, err)
	}

	_, c2 := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"nope"}`))
	})
	if _, err := c2.AITest(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientPayloadMarshalErrorPath(t *testing.T) {
	c := New("http://127.0.0.1:1", "t")
	// payload that cannot be marshaled → request() returns the marshal error
	err := c.request(context.Background(), http.MethodPost, "/x", func() {}, nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

// compile-time guard: Completion response used over the wire.
var _ = completion.Response{}
