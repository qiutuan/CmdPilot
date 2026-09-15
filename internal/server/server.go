// Package server implements the CmdPilot daemon: a loopback-only HTTP JSON
// API protected by a per-user random bearer token. Adapters, the companion
// binary and the CLI all talk to the same daemon so the core stays terminal-
// agnostic and only one process holds state.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/ai"
	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/daemonstate"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
	"github.com/qiutuan/CmdPilot/internal/logx"
	"github.com/qiutuan/CmdPilot/internal/version"
)

// Server is the daemon.
type Server struct {
	eng      *completion.Engine
	store    *db.DB
	cfg      *config.Config
	log      *logx.Logger
	token    string
	listener net.Listener
	httpSrv  *http.Server
	kb       *knowledge.DB
	aiClient *ai.Client
}

// New builds a Server. store/kb are required; log may be nil.
func New(store *db.DB, kb *knowledge.DB, cfg *config.Config, log *logx.Logger) *Server {
	s := &Server{store: store, cfg: cfg, log: log, kb: kb, token: newToken()}
	if aiClient, err := buildAIClient(cfg); err == nil {
		s.aiClient = aiClient
	}
	s.eng = completion.New(kb, store, cfg, s.aiClient, log)
	return s
}

// buildAIClient constructs the AI client from config when configured.
func buildAIClient(cfg *config.Config) (*ai.Client, error) {
	if cfg.AI.BaseURL == "" {
		return nil, fmt.Errorf("ai: base_url not configured")
	}
	key, err := cfg.APIKey()
	if err != nil {
		return nil, err
	}
	return ai.New(cfg.AI.BaseURL, key, cfg.AI.Model, time.Duration(cfg.AI.TimeoutMS)*time.Millisecond), nil
}

// newToken generates a random 32-byte hex bearer token.
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("server: cannot read randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Start binds 127.0.0.1 on an ephemeral port and serves until Shutdown.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	s.listener = ln
	s.httpSrv = &http.Server{Handler: s.routes()}
	port := ln.Addr().(*net.TCPAddr).Port

	st := daemonstate.State{PID: os.Getpid(), Port: port, Token: s.token, StartedAt: time.Now().Format(time.RFC3339)}
	if err := daemonstate.Write(st); err != nil {
		s.logWarn("write state: %v", err)
	}
	s.logInfo("daemon listening on 127.0.0.1:%d pid=%d", port, os.Getpid())
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logWarn("http serve: %v", err)
		}
	}()
	return nil
}

// Port returns the bound port (valid after Start).
func (s *Server) Port() int {
	if s.listener == nil {
		return 0
	}
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Shutdown gracefully stops the server and cleans the state file.
func (s *Server) Shutdown(ctx context.Context) error {
	daemonstate.Remove()
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

// logInfo/logWarn write via logx when available.
func (s *Server) logInfo(format string, args ...any) {
	if s.log != nil {
		s.log.Infof("server: "+format, args...)
	}
}

func (s *Server) logWarn(format string, args ...any) {
	if s.log != nil {
		s.log.Warnf("server: "+format, args...)
	}
}

// routes registers the API handlers.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/complete", s.auth(s.handleComplete))
	mux.HandleFunc("/report", s.auth(s.handleReport))
	mux.HandleFunc("/recommend", s.auth(s.handleRecommend))
	mux.HandleFunc("/ai/test", s.auth(s.handleAITest))
	mux.HandleFunc("/shutdown", s.auth(s.handleShutdown))
	mux.HandleFunc("/stats", s.auth(s.handleStats))
	return mux
}

// auth wraps a handler with bearer-token verification (constant-time compare).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !secureEqual(got, s.token) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// secureEqual compares two strings in constant time.
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.String()})
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req client.CompleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	resp, err := s.eng.Complete(completion.Request{
		Input: req.Input, Shell: req.Shell, CWD: req.CWD,
		History: req.History, Trigger: req.Trigger, WaitAIMS: req.WaitAIMS,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
		Dir     string `json:"dir"`
		Shell   string `json:"shell"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	if req.Command != "" && s.store != nil {
		if err := s.store.RecordUsage(db.UsageRecord{Command: req.Command, Dir: req.Dir, Shell: req.Shell, At: time.Now()}); err != nil {
			s.logWarn("record usage: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRecommend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := s.eng.Complete(completion.Request{Input: "", Shell: q.Get("shell"), CWD: q.Get("cwd")})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp.Recommendations)
}

func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request) {
	if s.aiClient == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "AI not configured (set base_url/model/api_key)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, err := s.aiClient.Complete(ctx, ai.Request{
		Model: s.cfg.AI.Model, Temperature: 0, MaxTokens: 8,
		Messages: []ai.Message{{Role: "user", Content: "Reply with the single word: ok"}},
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": resp.Model})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": nil})
		return
	}
	byDir, byShell, total, err := s.store.StatsDistribution()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "by_dir": byDir, "by_shell": byShell, "total": total})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(50 * time.Millisecond) // let the response flush
		_ = s.Shutdown(context.Background())
	}()
}
