// Package completion orchestrates CmdPilot's suggestion engine: context-aware
// local matching + frequency ranking + asynchronous AI enhancement.
//
// The engine is terminal-agnostic (no terminal APIs) and fully testable: all
// I/O goes through injected interfaces (knowledge, db, config, AI client, FS).
package completion

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiutuan/CmdPilot/internal/ai"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
	"github.com/qiutuan/CmdPilot/internal/logx"
	"github.com/qiutuan/CmdPilot/internal/match"
	"github.com/qiutuan/CmdPilot/internal/rank"
	"github.com/qiutuan/CmdPilot/internal/sanitize"
)

// Defaults for candidate counts.
const (
	MaxList    = 8  // candidates returned in the list view
	MaxHistory = 50 // recent history lines considered as candidates
)

// FS abstracts filesystem access so path completion is testable.
type FS interface {
	List(dir string) ([]string, error) // entry names (files+dirs) in dir
	IsDir(path string) bool            // whether path is a directory
	IsGitRepo(dir string) bool         // whether dir (or ancestor) is a git repo
}

// osFS is the production implementation backed by the OS.
type osFS struct{}

// List implements FS.
func (osFS) List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += string(os.PathSeparator)
		}
		out = append(out, name)
	}
	return out, nil
}

// IsDir implements FS.
func (osFS) IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// IsGitRepo implements FS (checks dir and ancestors for a .git entry).
func (osFS) IsGitRepo(dir string) bool {
	d := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// Request is one completion query from a terminal adapter.
type Request struct {
	Input    string   // current input line
	Shell    string   // cmd | ps
	CWD      string   // working directory
	History  []string // recent raw history lines (sanitized before AI use)
	Trigger  string   // "auto" | "tab"
	WaitAIMS int      // >0: block up to this many ms for an AI result (Tab mode)
}

// Suggestion is one completion candidate returned to the adapter.
type Suggestion struct {
	Text     string  `json:"text"`      // suffix to append (IsSuffix) or full line
	Full     string  `json:"full"`      // full resulting command line
	Kind     string  `json:"kind"`      // command|subcommand|param|path|history|favorite|ai
	Source   string  `json:"source"`    // local|ai|history|favorite
	Score    float64 `json:"score"`     // combined score
	IsSuffix bool    `json:"is_suffix"` // Text is a suffix of the input
}

// Response is the engine's answer.
type Response struct {
	Top             *Suggestion  `json:"top,omitempty"`             // best candidate (ghost text)
	List            []Suggestion `json:"list"`                      // top-N candidates (list view)
	Recommendations []Suggestion `json:"recommendations,omitempty"` // usage-based suggestions for empty input
	Context         ContextInfo  `json:"context"`
	AIUsed          bool         `json:"ai_used"`
}

// ContextInfo describes detected input state (used by adapters for UX hints).
type ContextInfo struct {
	State   string `json:"state"` // command|subcommand|param|path|unknown
	GitRepo bool   `json:"git_repo"`
}

// aiEntry is a cached AI completion for one input prefix.
type aiEntry struct {
	suffix string
	at     time.Time
}

// Engine is the completion orchestrator. Create with New.
type Engine struct {
	KB    *knowledge.DB
	Store *db.DB // optional (nil allowed)
	Cfg   *config.Config
	AI    *ai.Client // optional (nil disables AI)
	Log   *logx.Logger
	FS    FS
	Now   func() time.Time

	aiMu       sync.Mutex
	aiCache    map[string]aiEntry
	aiInFlight atomic.Bool
}

// contextWithTimeout is a thin wrapper so tests can swap timeouts easily.
var contextWithTimeout = context.WithTimeout

// New builds an Engine with sane defaults.
func New(kb *knowledge.DB, store *db.DB, cfg *config.Config, aiClient *ai.Client, log *logx.Logger) *Engine {
	return &Engine{
		KB: kb, Store: store, Cfg: cfg, AI: aiClient, Log: log, FS: osFS{},
		Now: time.Now, aiCache: map[string]aiEntry{},
	}
}

// usageIndex aggregates usage stats for one request (single DB read set).
type usageIndex struct {
	byCmd  map[string]db.UsageRow
	recent []string
}

// loadUsage fetches stats + recent commands once per request.
func (e *Engine) loadUsage(req Request) *usageIndex {
	ui := &usageIndex{byCmd: map[string]db.UsageRow{}}
	if e.Store == nil {
		return ui
	}
	// Global top + per-dir top (bounded reads, no full scan).
	rows, err := e.Store.TopN(200, time.Time{})
	if err == nil {
		for _, r := range rows {
			ui.byCmd[r.Command] = r
		}
	}
	if req.CWD != "" {
		if rows, err := e.Store.TopNByDir(req.CWD, 50); err == nil {
			for _, r := range rows {
				if cur, ok := ui.byCmd[r.Command]; ok {
					if r.Count > cur.Count {
						ui.byCmd[r.Command] = r
					}
				} else {
					ui.byCmd[r.Command] = r
				}
			}
		}
	}
	ui.recent, _ = e.Store.RecentCommands(10, "")
	return ui
}

// chainWeights returns a per-command chain multiplier. The anchor is the most
// recently executed command sharing the parent token that is NOT itself a
// candidate (e.g. `git add` when suggesting `git c...`). The candidate whose
// last usage most recently *followed* the anchor gets 1.5x; all others 1.0.
func (e *Engine) chainWeights(ui *usageIndex, parent string, cmds []string) map[string]float64 {
	w := make(map[string]float64, len(cmds))
	if parent == "" || ui == nil {
		return w
	}
	inSet := map[string]bool{}
	for _, c := range cmds {
		inSet[c] = true
	}
	anchor := ""
	for _, line := range ui.recent {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != parent || inSet[line] {
			continue
		}
		anchor = line
		break // recent is ordered most-recent-first
	}
	if anchor == "" {
		return w
	}
	anchorRow, ok := ui.byCmd[anchor]
	if !ok {
		return w
	}
	t0 := anchorRow.LastUsed
	best := time.Duration(-1)
	bestCmd := ""
	for _, c := range cmds {
		row, ok := ui.byCmd[c]
		if !ok || row.Count <= 0 {
			continue
		}
		dt := row.LastUsed.Sub(t0)
		if dt <= 0 {
			continue // not executed after the anchor: not a follow-up
		}
		if best == -1 || dt < best {
			best, bestCmd = dt, c
		}
	}
	if bestCmd != "" && best > 0 {
		w[bestCmd] = 1.5
	}
	return w
}

// rankScore computes the frequency component for a candidate.
func (e *Engine) rankScore(ui *usageIndex, cmd, inputDir string, contextMatch bool) float64 {
	row, ok := ui.byCmd[cmd]
	if !ok || row.Count <= 0 {
		return 0
	}
	if !e.Cfg.RecommendWeighting {
		contextMatch = false
	}
	return rank.Score(row.Count, row.LastUsed, e.Now(), contextMatch)
}

// cand is an internal candidate before final ranking.
type cand struct {
	text      string // match key (command name / param / line)
	kind      string
	source    string
	display   string // full completion text
	isSuffix  bool
	mr        match.Result
	rankScore float64
}

// Complete produces suggestions; it always returns the local result
// synchronously and may kick off (or reuse) an asynchronous AI fetch.
func (e *Engine) Complete(req Request) (*Response, error) {
	req.Shell = normalizeShell(req.Shell)
	ctxInfo := e.detectContext(req)
	ui := e.loadUsage(req)

	cands := e.localCandidates(req, ctxInfo, ui)
	resp := e.buildResponse(req, ctxInfo, cands)

	// AI enhancement (async, debounced, cached, non-blocking).
	if e.AI != nil && e.Cfg.Engine != config.EngineLocal && req.Input != "" {
		e.maybeFetchAI(req, ui, ctxInfo)
	}
	if aiSuffix := e.cachedAI(req.Input); aiSuffix != "" {
		resp.applyAI(aiSuffix, req)
	}
	if req.WaitAIMS > 0 && e.AI != nil && e.Cfg.Engine != config.EngineLocal {
		if suffix := e.waitAI(req, time.Duration(req.WaitAIMS)*time.Millisecond); suffix != "" {
			resp.applyAI(suffix, req)
		}
	}
	return resp, nil
}

// LocalComplete returns only the local engine result (no AI at all).
func (e *Engine) LocalComplete(req Request) (*Response, error) {
	req.Shell = normalizeShell(req.Shell)
	ctxInfo := e.detectContext(req)
	ui := e.loadUsage(req)
	return e.buildResponse(req, ctxInfo, e.localCandidates(req, ctxInfo, ui)), nil
}

// buildResponse converts internal candidates into the public response shape.
func (e *Engine) buildResponse(req Request, ctxInfo ContextInfo, cands []cand) *Response {
	resp := &Response{Context: ctxInfo, List: []Suggestion{}}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.mr.Tier != b.mr.Tier {
			return a.mr.Tier > b.mr.Tier
		}
		if a.rankScore != b.rankScore {
			return a.rankScore > b.rankScore
		}
		return a.mr.Score > b.mr.Score
	})
	seen := map[string]bool{}
	for i := range cands {
		c := cands[i]
		key := c.display + "|" + c.kind
		if seen[key] {
			continue
		}
		seen[key] = true
		s := Suggestion{
			Text: c.text, Full: c.display, Kind: c.kind, Source: c.source,
			Score: c.mr.Score + c.rankScore, IsSuffix: c.isSuffix,
		}
		if len(resp.List) < MaxList {
			resp.List = append(resp.List, s)
		}
	}
	if len(resp.List) > 0 {
		top := resp.List[0]
		resp.Top = &top
	}
	// Empty-input usage recommendations (M5: 启动终端或输入前缀时优先推荐高频).
	if strings.TrimSpace(req.Input) == "" && e.Store != nil {
		resp.Recommendations = e.recommend(req, ctxInfo)
	}
	return resp
}

// recommend returns top usage-based suggestions for the current directory.
func (e *Engine) recommend(req Request, ctx ContextInfo) []Suggestion {
	ui := e.loadUsage(req)
	var cands []cand
	seen := map[string]bool{}
	add := func(cmd string, dirContext bool) {
		row, ok := ui.byCmd[cmd]
		if !ok || seen[cmd] {
			return
		}
		seen[cmd] = true
		s := rank.Score(row.Count, row.LastUsed, e.Now(), dirContext && e.Cfg.RecommendWeighting)
		if s <= 0 {
			return
		}
		cands = append(cands, cand{text: cmd, kind: "command", source: "history", display: cmd, rankScore: s})
	}
	for _, c := range ui.recent {
		add(c, false)
	}
	for cmd := range ui.byCmd {
		add(cmd, false)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].rankScore > cands[j].rankScore })
	out := make([]Suggestion, 0, 5)
	for i, c := range cands {
		if i >= 5 {
			break
		}
		out = append(out, Suggestion{Text: c.text, Full: c.display, Kind: c.kind, Source: c.source, Score: c.rankScore, IsSuffix: false})
	}
	return out
}

// applyAI merges an AI suffix into the response as the top suggestion.
func (r *Response) applyAI(suffix string, req Request) {
	clean := strings.TrimSpace(suffix)
	if clean == "" {
		return
	}
	full := req.Input + clean
	// The full line must look like a real command line: starts with a
	// non-space token.
	if strings.TrimSpace(full) == "" {
		return
	}
	aiSug := Suggestion{
		Text: clean, Full: full, Kind: "ai", Source: "ai",
		Score: 1000 + float64(len(clean)), IsSuffix: true,
	}
	if r.Top == nil {
		r.Top = &aiSug
	} else if r.Top.Source == "ai" {
		*r.Top = aiSug
	} else {
		// hybrid: AI result becomes the ghost text, local list is preserved.
		*r.Top = aiSug
	}
	r.AIUsed = true
}

// cachedAI returns a fresh cached AI suffix for the prefix, or "".
func (e *Engine) cachedAI(prefix string) string {
	e.aiMu.Lock()
	defer e.aiMu.Unlock()
	ent, ok := e.aiCache[prefix]
	if !ok {
		return ""
	}
	ttl := time.Duration(e.Cfg.AICacheTTLMinutes) * time.Minute
	if e.Now().Sub(ent.at) > ttl {
		delete(e.aiCache, prefix)
		return ""
	}
	return ent.suffix
}

// maybeFetchAI debounces + dedupes AI fetches (at most one in-flight).
func (e *Engine) maybeFetchAI(req Request, ui *usageIndex, ctx ContextInfo) {
	prefix := req.Input
	e.aiMu.Lock()
	_, fresh := e.aiCache[prefix]
	e.aiMu.Unlock()
	if fresh || e.aiInFlight.Load() {
		return
	}
	if e.aiInFlight.CompareAndSwap(false, true) {
		go func() {
			defer e.aiInFlight.Store(false)
			debounce := time.Duration(e.Cfg.DebounceMS) * time.Millisecond
			time.Sleep(debounce) // let the user pause before spending a request
			suffix, err := e.fetchAI(req, ui, ctx)
			if err != nil {
				e.logAI("ai_fetch_fail", prefix, err.Error())
				return
			}
			e.aiMu.Lock()
			e.aiCache[prefix] = aiEntry{suffix: suffix, at: e.Now()}
			e.aiMu.Unlock()
			e.logAI("ai_fetch_ok", prefix, "")
		}()
	}
}

// fetchAI performs the actual model call (sanitized history + local top-5).
func (e *Engine) fetchAI(req Request, ui *usageIndex, ctx ContextInfo) (string, error) {
	local := e.localCandidates(req, ctx, ui)
	top := make([]string, 0, 5)
	for _, c := range local {
		if len(top) >= 5 {
			break
		}
		top = append(top, c.display)
	}
	hist := sanitizeLines(req.History)
	if len(hist) > e.Cfg.HistoryLines {
		hist = hist[len(hist)-e.Cfg.HistoryLines:]
	}
	prompt := ai.BuildPrompt(ai.PromptInput{
		Shell: req.Shell, CWD: req.CWD, Input: req.Input,
		History: hist, LocalTop: top, GitRepo: ctx.GitRepo,
	})
	ctx2, cancel := contextWithTimeout(context.Background(), time.Duration(e.Cfg.AI.TimeoutMS)*time.Millisecond)
	defer cancel()
	resp, err := e.AI.Complete(ctx2, ai.Request{
		Model:       e.Cfg.AI.Model,
		Temperature: e.Cfg.AI.Temperature,
		MaxTokens:   e.Cfg.AI.MaxTokens,
		Messages: []ai.Message{
			{Role: "system", Content: ai.SystemPrompt},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", err
	}
	return ai.ValidateSuffix(req.Input, resp.Content), nil
}

// waitAI blocks up to timeout for an AI result for the prefix (Tab mode).
func (e *Engine) waitAI(req Request, timeout time.Duration) string {
	prefix := req.Input
	deadline := time.Now().Add(timeout)
	for {
		if s := e.cachedAI(prefix); s != "" {
			return s
		}
		if e.aiInFlight.Load() {
			time.Sleep(25 * time.Millisecond)
		} else {
			// Kick off a fresh fetch (Tab = explicit request, minimal debounce).
			ui := e.loadUsage(req)
			ctxInfo := e.detectContext(req)
			e.aiInFlight.Store(true)
			go func() {
				defer e.aiInFlight.Store(false)
				suffix, err := e.fetchAI(req, ui, ctxInfo)
				if err != nil {
					e.logAI("ai_wait_fail", prefix, err.Error())
					return
				}
				e.aiMu.Lock()
				e.aiCache[prefix] = aiEntry{suffix: suffix, at: e.Now()}
				e.aiMu.Unlock()
			}()
		}
		if time.Now().After(deadline) {
			return ""
		}
	}
}

// logAI writes AI metadata only (no key, no prompt contents).
func (e *Engine) logAI(event, prefix, detail string) {
	if e.Log == nil {
		return
	}
	if detail != "" {
		e.Log.Infof("ai: %s prefix_len=%d err=%s", event, len(prefix), detail)
	} else {
		e.Log.Infof("ai: %s prefix_len=%d", event, len(prefix))
	}
}

// sanitizeLines drops secret-bearing lines before anything leaves the machine.
func sanitizeLines(lines []string) []string {
	return sanitize.Sanitize(lines)
}

func normalizeShell(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cmd", "cmd.exe", "clink":
		return "cmd"
	case "ps", "powershell", "pwsh":
		return "ps"
	}
	return "ps"
}

// --- context detection ---

func (e *Engine) detectContext(req Request) ContextInfo {
	ctx := ContextInfo{GitRepo: false, State: "unknown"}
	if req.CWD != "" && e.FS != nil {
		ctx.GitRepo = e.FS.IsGitRepo(req.CWD)
	}
	state, _, _ := analyzeInput(req.Input, req.Shell, e.KB)
	ctx.State = state
	return ctx
}

// analyzeInput classifies the current input state. Pure-ish (uses KB).
func analyzeInput(input, shell string, kb *knowledge.DB) (state, first, current string) {
	hasTrailingSpace := strings.HasSuffix(input, " ")
	input = strings.TrimRight(input, " ")
	if input == "" {
		return "command", "", ""
	}
	fields := strings.Fields(input)
	first = fields[0]
	current = fields[len(fields)-1]
	if !hasTrailingSpace && strings.Contains(input, " ") {
		// mid-token of a later word
		cur := fields[len(fields)-1]
		if isParamFlag(cur, shell) {
			return "param", first, cur
		}
		if looksLikePath(cur) {
			return "path", first, cur
		}
		if knownParent(first, kb) {
			return "subcommand", first, cur
		}
		return "param", first, cur
	}
	if hasTrailingSpace {
		if knownParent(first, kb) {
			return "subcommand", first, ""
		}
		if kb.Find(first) != nil {
			return "param", first, ""
		}
		return "command", first, ""
	}
	// single token
	cur := fields[0]
	if isParamFlag(cur, shell) {
		return "param", cur, cur
	}
	if looksLikePath(cur) {
		return "path", first, cur
	}
	return "command", first, cur
}

// isParamFlag reports whether cur looks like a flag token:
// "-x"/"--x" always; "/x" only for CMD when no further path characters follow.
func isParamFlag(cur, shell string) bool {
	if strings.HasPrefix(cur, "-") {
		return true
	}
	if strings.HasPrefix(cur, "/") && !strings.ContainsAny(cur[1:], `/\`) {
		return shell == "cmd"
	}
	return false
}

// knownParent reports whether first is a multi-word command parent ("git").
func knownParent(first string, kb *knowledge.DB) bool {
	for _, c := range kb.Commands {
		if strings.HasPrefix(c.Name, first+" ") {
			return true
		}
	}
	return false
}

// looksLikePath guesses whether a token is a path (drive, separator, dot).
func looksLikePath(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.ContainsAny(tok, `/\`) || strings.HasPrefix(tok, ".") ||
		(len(tok) >= 2 && tok[1] == ':') || tok == "~" || strings.HasPrefix(tok, "~") {
		return true
	}
	return false
}

// shellRelevant filters knowledge commands for a terminal.
func shellRelevant(c knowledge.Command, shell string) bool {
	switch shell {
	case "cmd":
		return c.Shell == knowledge.ShellCmd
	default:
		return c.Shell == knowledge.ShellPS || c.Shell == knowledge.ShellExternal
	}
}
