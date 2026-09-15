package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// overrideBaseDir points config at a temp dir for the duration of a test.
func overrideBaseDir(t *testing.T, dir string) {
	t.Helper()
	old := baseDirOverride
	baseDirOverride = dir
	t.Cleanup(func() { baseDirOverride = old })
}

// TestLoadDefaults verifies a fresh environment yields sane defaults.
func TestLoadDefaults(t *testing.T) {
	overrideBaseDir(t, t.TempDir())
	cfg, corrupted, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if corrupted {
		t.Fatal("fresh env reported corruption")
	}
	if cfg.Engine != EngineHybrid || cfg.DebounceMS != 300 || cfg.AI.TimeoutMS != 5000 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

// TestSetPersistsRoundTrip verifies Set() values survive a reload.
func TestSetPersistsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	overrideBaseDir(t, dir)
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("engine", "local"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("ai.model", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("ai.base_url", "https://api.example.com/"); err != nil {
		t.Fatal(err)
	}

	cfg2, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Engine != EngineLocal {
		t.Errorf("engine not persisted: %v", cfg2.Engine)
	}
	if cfg2.AI.Model != "gpt-4o-mini" {
		t.Errorf("model not persisted: %v", cfg2.AI.Model)
	}
	// Trailing slash must be trimmed.
	if cfg2.AI.BaseURL != "https://api.example.com" {
		t.Errorf("base_url not trimmed: %q", cfg2.AI.BaseURL)
	}
}

// TestSetAPIKeyEncrypts verifies the key is stored encrypted, never in plaintext.
func TestSetAPIKeyEncrypts(t *testing.T) {
	dir := t.TempDir()
	overrideBaseDir(t, dir)
	cfg, _, _ := Load()
	if err := cfg.Set("ai.api_key", "sk-plaintext-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-plaintext-secret") {
		t.Fatal("plaintext API key leaked into config file")
	}
	got, err := cfg.APIKey()
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-plaintext-secret" {
		t.Fatalf("decrypted key mismatch: %q", got)
	}
}

// TestCorruptConfigFallsBack verifies a corrupt file is backed up and defaults are used.
func TestCorruptConfigFallsBack(t *testing.T) {
	dir := t.TempDir()
	overrideBaseDir(t, dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "{ this is not valid json ###"
	if err := os.WriteFile(Path(), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, corrupted, err := Load()
	if err != nil {
		t.Fatalf("Load on corrupt file must not fail: %v", err)
	}
	if !corrupted {
		t.Fatal("corruption not reported")
	}
	if cfg.Engine != EngineHybrid {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "config.json.bak-*"))
	if len(matches) == 0 {
		t.Fatal("corrupt file was not backed up")
	}
}

// TestInvalidSetValues verifies bad values are rejected.
func TestInvalidSetValues(t *testing.T) {
	overrideBaseDir(t, t.TempDir())
	cfg, _, _ := Load()
	for _, bad := range [][2]string{
		{"engine", "quantum"}, {"trigger", "shift"}, {"ai.temperature", "99"},
		{"ai.max_tokens", "-5"}, {"log_level", "loud"}, {"nonexistent.path", "x"},
	} {
		if err := cfg.Set(bad[0], bad[1]); err == nil {
			t.Errorf("Set(%q,%q) should fail", bad[0], bad[1])
		}
	}
}

// TestGetPaths verifies every supported path can be read back.
func TestGetPaths(t *testing.T) {
	overrideBaseDir(t, t.TempDir())
	cfg, _, _ := Load()
	for _, p := range Paths() {
		if _, err := cfg.Get(p); err != nil {
			t.Errorf("Get(%q): %v", p, err)
		}
	}
}

// TestEnvOverride verifies environment variables take precedence over file values.
func TestEnvOverride(t *testing.T) {
	dir := t.TempDir()
	overrideBaseDir(t, dir)
	cfg, _, _ := Load()
	_ = cfg.Set("engine", "local")
	t.Setenv("CMDPILOT_ENGINE", "hybrid")
	t.Setenv("CMDPILOT_AI_BASE_URL", "http://127.0.0.1:9999")
	t.Setenv("CMDPILOT_AI_API_KEY", "env-key-not-persisted")
	t.Setenv("CMDPILOT_AI_TIMEOUT_MS", "1234")

	cfg2, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Engine != EngineHybrid {
		t.Errorf("env engine override failed: %v", cfg2.Engine)
	}
	if cfg2.AI.BaseURL != "http://127.0.0.1:9999" {
		t.Errorf("env base_url override failed: %q", cfg2.AI.BaseURL)
	}
	key, err := cfg2.APIKey()
	if err != nil || key != "env-key-not-persisted" {
		t.Errorf("env api key not honored: %q err=%v", key, err)
	}
	if cfg2.AI.TimeoutMS != 1234 {
		t.Errorf("env timeout override failed: %d", cfg2.AI.TimeoutMS)
	}
	// The env key must never be persisted to disk.
	raw, _ := os.ReadFile(Path())
	if strings.Contains(string(raw), "env-key-not-persisted") {
		t.Fatal("env-provided key was persisted to disk")
	}
}

// TestBaseDir sanity: never empty.
func TestBaseDir(t *testing.T) {
	if BaseDir() == "" {
		t.Fatal("BaseDir is empty")
	}
}

// --- 覆盖率补充：全部 Set/Get 路径与错误分支 ---

func TestSetAllPathsAndValidate(t *testing.T) {
	dir := t.TempDir()
	old := BaseDirOverrideForTest(dir)
	defer BaseDirOverrideForTest(old)
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	sets := map[string]string{
		"engine":               "ai",
		"trigger":              "tab",
		"ai.base_url":          "https://api.openai.com/",
		"ai.model":             "gpt-4o-mini",
		"ai.temperature":       "0.7",
		"ai.max_tokens":        "128",
		"ai.timeout_ms":        "8000",
		"debounce_ms":          "250",
		"ai_cache_ttl_minutes": "10",
		"history_lines":        "20",
		"recommend_weighting":  "false",
		"enable_prompt_line":   "false",
		"log_enabled":          "false",
		"log_level":            "warn",
	}
	for k, v := range sets {
		if err := cfg.Set(k, v); err != nil {
			t.Fatalf("Set(%s): %v", k, err)
		}
		got, err := cfg.Get(k)
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		if k == "ai.base_url" && got != "https://api.openai.com" {
			t.Errorf("base_url not trimmed: %q", got)
		}
	}
	// 错误分支
	bad := map[string]string{
		"engine": "nope", "trigger": "nope", "ai.temperature": "9",
		"ai.max_tokens": "0", "ai.timeout_ms": "-1", "debounce_ms": "-1",
		"ai_cache_ttl_minutes": "0", "history_lines": "-1",
		"recommend_weighting": "x", "enable_prompt_line": "x",
		"log_enabled": "x", "log_level": "nope", "bogus.path": "1",
	}
	for k, v := range bad {
		if err := cfg.Set(k, v); err == nil {
			t.Errorf("Set(%s=%q) should fail", k, v)
		}
	}
	if _, err := cfg.Get("bogus.path"); err == nil {
		t.Error("Get bogus should fail")
	}
	if cfg.Effective() != "AI+local" {
		t.Errorf("Effective = %q", cfg.Effective())
	}
}

func TestAPIKeyEnvOverrideAndGet(t *testing.T) {
	dir := t.TempDir()
	old := BaseDirOverrideForTest(dir)
	defer BaseDirOverrideForTest(old)
	t.Setenv("CMDPILOT_AI_API_KEY", "sk-env-whatever")
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKeyEnv != "sk-env-whatever" {
		t.Fatalf("env key not loaded: %q", cfg.AI.APIKeyEnv)
	}
	k, err := cfg.APIKey()
	if err != nil || k != "sk-env-whatever" {
		t.Fatalf("APIKey: %v %v", k, err)
	}
	got, err := cfg.Get("ai.api_key")
	if err != nil || got != "(set, hidden)" {
		t.Errorf("get ai.api_key: %q %v", got, err)
	}
}

func TestAPIKeyNotSetGet(t *testing.T) {
	dir := t.TempDir()
	old := BaseDirOverrideForTest(dir)
	defer BaseDirOverrideForTest(old)
	cfg, _, _ := Load()
	got, err := cfg.Get("ai.api_key")
	if err != nil || got != "(not set)" {
		t.Errorf("get ai.api_key: %q %v", got, err)
	}
}
