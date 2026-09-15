package completion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiutuan/CmdPilot/internal/ai"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
)

// fakeFS is an in-memory filesystem for path-completion tests.
type fakeFS struct {
	mu      sync.Mutex
	entries map[string][]string // dir -> entry names
	dirs    map[string]bool
	git     map[string]bool
}

func newFakeFS() *fakeFS {
	return &fakeFS{entries: map[string][]string{}, dirs: map[string]bool{}, git: map[string]bool{}}
}

func (f *fakeFS) addDir(dir string, entries []string, dirEntries []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[dir] = true
	f.entries[dir] = append(entries, dirEntries...)
	for _, d := range dirEntries {
		f.dirs[filepath.Join(dir, d)] = true
	}
}

func (f *fakeFS) List(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.dirs[dir]; !ok {
		return nil, errNoSuchDir
	}
	return f.entries[dir], nil
}

func (f *fakeFS) IsDir(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dirs[p]
}

func (f *fakeFS) IsGitRepo(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for d := dir; d != "" && d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
		if f.git[d] {
			return true
		}
	}
	return false
}

var errNoSuchDir = &pathError{}

type pathError struct{}

func (*pathError) Error() string { return "no such directory" }

// newTestEngine wires an engine with knowledge + optional store + config.
func newTestEngine(t *testing.T, store *db.DB, cfg *config.Config) *Engine {
	t.Helper()
	kb, err := knowledge.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = config.Default()
	}
	e := New(kb, store, cfg, nil, nil)
	return e
}

// newTestStore opens a temp SQLite store.
func newTestStore(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestCommandPrefixRecommendation: usage frequency breaks prefix ties.
func TestCommandPrefixRecommendation(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	for i := 0; i < 10; i++ {
		_ = store.RecordUsage(db.UsageRecord{Command: "git status", Dir: "/proj", Shell: "ps", At: now})
	}
	_ = store.RecordUsage(db.UsageRecord{Command: "git stash", Dir: "/proj", Shell: "ps", At: now})

	e := newTestEngine(t, store, nil)
	resp, err := e.Complete(Request{Input: "git st", Shell: "ps", CWD: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Top == nil || resp.Top.Full != "git status" {
		t.Fatalf("Top = %+v, want git status", resp.Top)
	}
}

// TestSubcommandCompletion: "git co" suggests git commands, not unrelated ones.
func TestSubcommandCompletion(t *testing.T) {
	e := newTestEngine(t, nil, nil)
	resp, err := e.Complete(Request{Input: "git co", Shell: "ps", CWD: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range resp.List {
		if s.Full == "git commit" || s.Full == "git checkout" || s.Full == "git config" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no git subcommand in list: %+v", resp.List)
	}
	if resp.Context.State != "subcommand" {
		t.Errorf("state = %q, want subcommand", resp.Context.State)
	}
}

// TestParamCompletion: "git commit -a" suggests the -am flag.
func TestParamCompletion(t *testing.T) {
	e := newTestEngine(t, nil, nil)
	resp, err := e.Complete(Request{Input: "git commit -a", Shell: "ps", CWD: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.State != "param" {
		t.Errorf("state = %q, want param", resp.Context.State)
	}
	var sawAm bool
	for _, s := range resp.List {
		if s.Full == "git commit -am" {
			sawAm = true
		}
	}
	if !sawAm {
		t.Errorf("expected -am param: %+v", resp.List)
	}
}

// TestPathCompletion uses the fake FS.
func TestPathCompletion(t *testing.T) {
	fs := newFakeFS()
	fs.addDir("/proj", []string{"main.go", "util.go", "README.md"}, []string{"src", "build"})
	fs.addDir("/proj/src", []string{"app.go", "lib.go", "main.go"}, nil)
	fs.git["/proj"] = true

	e := newTestEngine(t, nil, nil)
	e.FS = fs

	// "cd src/" should list dir entries.
	resp, err := e.Complete(Request{Input: "cd src/", Shell: "cmd", CWD: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Context.State != "path" {
		t.Errorf("state = %q, want path", resp.Context.State)
	}
	var sawApp bool
	for _, s := range resp.List {
		if s.Full == "cd src/app.go" {
			sawApp = true
		}
	}
	if !sawApp {
		t.Errorf("expected src/app.go in list: %+v", resp.List)
	}

	// Prefix filtering: "cd src/m" -> main.go
	resp2, _ := e.Complete(Request{Input: "cd src/m", Shell: "cmd", CWD: "/proj"})
	var sawMainGo bool
	for _, s := range resp2.List {
		if s.Full == "cd src/main.go" {
			sawMainGo = true
		}
	}
	if !sawMainGo {
		t.Errorf("expected src/main.go: %+v", resp2.List)
	}

	// Git repo detection
	if !resp.Context.GitRepo {
		t.Error("git repo not detected")
	}
}

// TestGitRepoDetectionAncestor verifies ancestor lookup.
func TestGitRepoDetectionAncestor(t *testing.T) {
	fs := newFakeFS()
	fs.addDir("/repo", nil, nil)
	fs.git["/repo"] = true
	fs.addDir("/repo/sub", nil, nil)
	e := newTestEngine(t, nil, nil)
	e.FS = fs
	resp, _ := e.Complete(Request{Input: "git st", Shell: "ps", CWD: "/repo/sub"})
	if !resp.Context.GitRepo {
		t.Error("ancestor git repo not detected")
	}
}

// TestFavoriteBoost verifies favorites outrank plain history.
func TestFavoriteBoost(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// history has git stash used 3x, but the most recent command is kubectl
	// so no git chain-bonus leaks into the comparison.
	for i := 0; i < 3; i++ {
		_ = store.RecordUsage(db.UsageRecord{Command: "git stash", Dir: "/p", Shell: "ps", At: now})
	}
	_ = store.RecordUsage(db.UsageRecord{Command: "kubectl get pods", Dir: "/p", Shell: "ps", At: now.Add(time.Second)})
	// favorite named "git st..." matching the same prefix
	if _, err := store.AddFavorite(db.Favorite{Name: "git stage", Command: "git stage .", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t, store, nil)
	resp, _ := e.Complete(Request{Input: "git st", Shell: "ps", CWD: "/p"})
	if resp.Top == nil || resp.Top.Source != "favorite" {
		t.Fatalf("favorite should win over history: %+v", resp.Top)
	}
}

// TestHistoryCandidates verifies recent executed lines appear as candidates.
func TestHistoryCandidates(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	_ = store.RecordUsage(db.UsageRecord{Command: "docker compose up -d", Dir: "/p", Shell: "ps", At: now})
	e := newTestEngine(t, store, nil)
	resp, _ := e.Complete(Request{Input: "docker", Shell: "ps", CWD: "/p"})
	var saw bool
	for _, s := range resp.List {
		if s.Source == "history" && strings.Contains(s.Full, "docker compose up") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("history candidate missing: %+v", resp.List)
	}
}

// TestChainRecommendation: after "git add", "git commit" gets the context bonus.
func TestChainRecommendation(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// Equal usage for commit and checkout; chain bonus must break the tie.
	_ = store.RecordUsage(db.UsageRecord{Command: "git commit", Dir: "/p", Shell: "ps", At: now})
	_ = store.RecordUsage(db.UsageRecord{Command: "git checkout", Dir: "/p", Shell: "ps", At: now})
	_ = store.RecordUsage(db.UsageRecord{Command: "git add", Dir: "/p", Shell: "ps", At: now.Add(time.Second)})

	e := newTestEngine(t, store, nil)
	resp, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p"})
	if resp.Top == nil || resp.Top.Full != "git commit" {
		t.Fatalf("chain bonus should prefer git commit, got %+v", resp.Top)
	}
}

// TestRecommendationsForEmptyInput verifies M5 recommendations.
func TestRecommendationsForEmptyInput(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	for i := 0; i < 4; i++ {
		_ = store.RecordUsage(db.UsageRecord{Command: "kubectl get pods", Dir: "/k", Shell: "ps", At: now})
	}
	e := newTestEngine(t, store, nil)
	resp, _ := e.Complete(Request{Input: "", Shell: "ps", CWD: "/k"})
	if len(resp.Recommendations) == 0 {
		t.Fatal("no recommendations for empty input")
	}
	if resp.Recommendations[0].Full != "kubectl get pods" {
		t.Errorf("top recommendation = %+v", resp.Recommendations[0])
	}
}

// --- AI integration ---

// mockAIServer returns a fixed suffix and records requests.
func mockAIServer(t *testing.T, suffix string, status int) (*httptest.Server, *[]map[string]any) {
	var mu sync.Mutex
	var captured []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		captured = append(captured, body)
		mu.Unlock()
		if status != 200 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"mock failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"mock","choices":[{"message":{"content":"` + suffix + `"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// aiEngine builds an engine with a mock AI client (debounce shortened).
func aiEngine(t *testing.T, baseURL string) *Engine {
	cfg := config.Default()
	cfg.Engine = config.EngineHybrid
	cfg.DebounceMS = 50
	cfg.AI.BaseURL = baseURL
	cfg.AI.Model = "mock"
	client := ai.New(baseURL, "sk-mock", "mock", 2*time.Second)
	e := newTestEngine(t, nil, cfg)
	e.AI = client
	return e
}

// TestAIAsyncCached: AI suffix appears after the debounced fetch completes.
func TestAIAsyncCached(t *testing.T) {
	srv, _ := mockAIServer(t, "ommit -m \\\"wip\\\"", 200)
	e := aiEngine(t, srv.URL)

	resp, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p", History: []string{"git status"}})
	if resp.AIUsed {
		t.Fatal("AI should not be ready on the first keystroke batch")
	}
	// Wait for debounce + fetch to finish.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp2, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p", History: []string{"git status"}})
		if resp2.AIUsed && resp2.Top != nil && resp2.Top.Source == "ai" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AI result never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAIWaitMode: WaitAIMS blocks until the AI result is ready.
func TestAIWaitMode(t *testing.T) {
	srv, _ := mockAIServer(t, "heckout", 200)
	e := aiEngine(t, srv.URL)
	start := time.Now()
	resp, _ := e.Complete(Request{Input: "git che", Shell: "ps", CWD: "/p", WaitAIMS: 2000})
	if !resp.AIUsed || resp.Top == nil || !strings.Contains(resp.Top.Full, "heckout") {
		t.Fatalf("wait mode did not produce AI result: %+v", resp.Top)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("wait mode too slow: %v", time.Since(start))
	}
}

// TestAIDegradesOn500: AI failure must not break local completion.
func TestAIDegradesOn500(t *testing.T) {
	srv, _ := mockAIServer(t, "x", 500)
	e := aiEngine(t, srv.URL)
	resp, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p", WaitAIMS: 1500})
	if resp.Top == nil {
		t.Fatal("local completion lost on AI failure")
	}
	if resp.AIUsed {
		t.Error("AIUsed must be false on failure")
	}
	if resp.Top.Source == "ai" {
		t.Error("AI suggestion must not appear on failure")
	}
}

// TestAIDegradesOnMalformed: garbage body must degrade silently.
func TestAIDegradesOnMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	e := aiEngine(t, srv.URL)
	resp, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p", WaitAIMS: 1500})
	if resp.Top == nil || resp.Top.Source == "ai" {
		t.Fatalf("malformed AI response must degrade to local: %+v", resp.Top)
	}
}

// TestAISanitizesHistory: secrets never reach the mock server.
func TestAISanitizesHistory(t *testing.T) {
	srv, captured := mockAIServer(t, " status", 200)
	e := aiEngine(t, srv.URL)
	hist := []string{
		"git status",
		"export API_KEY=sk-supersecret123",
		"curl -H \"Authorization: Bearer abc.def\" https://x",
	}
	resp, _ := e.Complete(Request{Input: "git s", Shell: "ps", CWD: "/p", History: hist, WaitAIMS: 2000})
	if !resp.AIUsed {
		t.Fatal("AI not used")
	}
	// Wait a moment for the request to be recorded, then inspect the payload.
	time.Sleep(200 * time.Millisecond)
	if len(*captured) == 0 {
		t.Fatal("no AI request captured")
	}
	msgs := (*captured)[0]["messages"].([]any)
	userContent := ""
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] == "user" {
			userContent = mm["content"].(string)
		}
	}
	if strings.Contains(userContent, "sk-supersecret123") || strings.Contains(userContent, "Bearer abc.def") {
		t.Fatalf("secret leaked into AI prompt: %s", userContent)
	}
	if !strings.Contains(userContent, "git status") {
		t.Errorf("benign history missing from prompt: %s", userContent)
	}
}

// TestAnalyzeInputStates covers the state machine directly.
func TestAnalyzeInputStates(t *testing.T) {
	kb, _ := knowledge.Load()
	cases := []struct {
		input, shell, want string
	}{
		{"", "ps", "command"},
		{"git", "ps", "command"},
		{"git ", "ps", "subcommand"},
		{"git sta", "ps", "subcommand"},
		{"git commit -a", "ps", "param"},
		{"git commit --amend", "ps", "param"},
		{"cd /usr/bin", "ps", "path"},
		{"ls ./src", "ps", "path"},
		{"cd D:\\proj\\", "cmd", "path"},
	}
	for _, tc := range cases {
		state, _, _ := analyzeInput(tc.input, tc.shell, kb)
		if state != tc.want {
			t.Errorf("analyzeInput(%q, %q) = %q, want %q", tc.input, tc.shell, state, tc.want)
		}
	}
}

// TestLocalOnlyEngine verifies engine=local never touches AI.
func TestLocalOnlyEngine(t *testing.T) {
	cfg := config.Default()
	cfg.Engine = config.EngineLocal
	srv, _ := mockAIServer(t, "x", 200)
	e := newTestEngine(t, nil, cfg)
	e.AI = ai.New(srv.URL, "k", "m", time.Second)
	resp, _ := e.Complete(Request{Input: "git c", Shell: "ps", CWD: "/p", WaitAIMS: 500})
	if resp.AIUsed || resp.Top == nil || resp.Top.Source == "ai" {
		t.Fatalf("local engine must not use AI: %+v", resp.Top)
	}
}
