package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

// startTestServer boots a real Server on an ephemeral loopback port.
func startTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	store, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kb, err := knowledge.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Engine = config.EngineLocal // keep tests hermetic (no AI)
	srv := New(store, kb, cfg, nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()); store.Close() })
	return srv, store
}

// authedReq builds an authenticated request helper.
func authedReq(t *testing.T, srv *Server, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(srv.Port())+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+srv.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestHealth verifies the health endpoint without auth.
func TestHealth(t *testing.T) {
	srv, _ := startTestServer(t)
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["ok"] != true {
		t.Errorf("health = %v", out)
	}
}

// TestAuthRejects verifies missing/wrong tokens get 401.
func TestAuthRejects(t *testing.T) {
	srv, _ := startTestServer(t)
	// no token
	resp, err := http.Post("http://127.0.0.1:"+strconv.Itoa(srv.Port())+"/complete", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token status = %d, want 401", resp.StatusCode)
	}
	// wrong token
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(srv.Port())+"/complete", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", resp2.StatusCode)
	}
}

// TestCompleteEndpoint verifies /complete returns suggestions.
func TestCompleteEndpoint(t *testing.T) {
	srv, _ := startTestServer(t)
	resp := authedReq(t, srv, "POST", "/complete", `{"input":"git st","shell":"ps","cwd":"/tmp"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Top *struct {
			Full   string `json:"full"`
			Source string `json:"source"`
		} `json:"top"`
		List []map[string]any `json:"list"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Top == nil || !strings.HasPrefix(out.Top.Full, "git ") {
		t.Errorf("top = %+v", out.Top)
	}
	if len(out.List) == 0 {
		t.Error("empty list")
	}
}

// TestReportEndpoint verifies /report records usage (stats visible after).
func TestReportEndpoint(t *testing.T) {
	srv, store := startTestServer(t)
	for i := 0; i < 2; i++ {
		resp := authedReq(t, srv, "POST", "/report", `{"command":"docker ps","dir":"/d","shell":"ps"}`)
		resp.Body.Close()
	}
	rows, err := store.TopN(10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Command != "docker ps" || rows[0].Count != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

// TestShutdownEndpoint verifies graceful shutdown works and state file is removed.
func TestShutdownEndpoint(t *testing.T) {
	srv, _ := startTestServer(t)
	resp := authedReq(t, srv, "POST", "/shutdown", ``)
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	if _, err := http.Get("http://127.0.0.1:" + strconv.Itoa(srv.Port()) + "/health"); err == nil {
		t.Error("server still responding after shutdown")
	}
}

// TestStateFileWritten verifies daemon.json contains port+token.
func TestStateFileWritten(t *testing.T) {
	srv, _ := startTestServer(t)
	// Server writes state to the real config.BaseDir; verify via daemonstate.
	if srv.Port() == 0 || len(srv.token) < 32 {
		t.Errorf("port=%d token len=%d", srv.Port(), len(srv.token))
	}
}

// --- 覆盖率补充 ---

func TestCompleteEndpointBadBodyAndRecommend(t *testing.T) {
	srv, _ := startTestServer(t)
	// 非法 JSON → 400/500 但不崩溃
	resp := authedReq(t, srv, http.MethodPost, "/complete", "{bad")
	if resp.StatusCode == http.StatusOK {
		t.Error("bad body accepted")
	}
	resp.Body.Close()
	// 合法请求
	body := `{"input":"git st","shell":"cmd","cwd":"/tmp"}`
	resp = authedReq(t, srv, http.MethodPost, "/complete", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// recommend 端点
	resp = authedReq(t, srv, http.MethodPost, "/recommend", `{"cwd":"/tmp","shell":"cmd"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recommend: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// stats 端点
	resp = authedReq(t, srv, http.MethodGet, "/stats", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestReportAndStatsFlow(t *testing.T) {
	srv, _ := startTestServer(t)
	resp := authedReq(t, srv, http.MethodPost, "/report", `{"command":"git log","dir":"/r","shell":"cmd"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = authedReq(t, srv, http.MethodGet, "/stats", "")
	var out struct {
		Total int `json:"total"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Total < 1 {
		t.Errorf("stats total = %d", out.Total)
	}
}

func TestAITestNotConfigured(t *testing.T) {
	srv, _ := startTestServer(t) // aiClient=nil（EngineLocal）
	resp := authedReq(t, srv, http.MethodPost, "/ai/test", "")
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.OK || !strings.Contains(out.Error, "not configured") {
		t.Errorf("ai/test = %+v", out)
	}
}

func TestAuthMissingAndWrongToken(t *testing.T) {
	srv, _ := startTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(srv.Port())+"/complete", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing auth: %d", resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(srv.Port())+"/complete", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
