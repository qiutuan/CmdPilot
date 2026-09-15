// Package config manages CmdPilot's single-file JSON configuration.
//
// Precedence (lowest to highest): built-in defaults < config.json <
// CMDPILOT_* environment variables. A corrupted config.json is backed up and
// replaced with defaults so the tool never fails to start.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/secrets"
)

// EngineMode selects which completion engine is active.
type EngineMode string

// Supported engine modes.
const (
	EngineLocal  EngineMode = "local"  // local knowledge base only
	EngineHybrid EngineMode = "hybrid" // local base + AI enhancement (default)
	EngineAI     EngineMode = "ai"     // AI preferred, local as fallback
)

// TriggerMode selects when AI completion is requested.
type TriggerMode string

// Supported trigger modes.
const (
	TriggerAuto TriggerMode = "auto" // automatic on typing pause (debounced)
	TriggerTab  TriggerMode = "tab"  // AI only on explicit Tab
)

// AIConfig holds the OpenAI-compatible endpoint settings.
type AIConfig struct {
	BaseURL         string  `json:"base_url"`                    // e.g. https://api.openai.com
	APIKeyEncrypted string  `json:"api_key_encrypted,omitempty"` // DPAPI-protected key
	Model           string  `json:"model"`                       // e.g. gpt-4o-mini
	Temperature     float64 `json:"temperature"`
	MaxTokens       int     `json:"max_tokens"`
	TimeoutMS       int     `json:"timeout_ms"` // per-request timeout (default 5000)

	APIKeyEnv string `json:"-"` // in-memory key from CMDPILOT_AI_API_KEY (never persisted)
}

// Config is the root configuration document (schema version 1).
type Config struct {
	Version            int         `json:"version"`
	Engine             EngineMode  `json:"engine"`
	Trigger            TriggerMode `json:"trigger"`
	AI                 AIConfig    `json:"ai"`
	DebounceMS         int         `json:"debounce_ms"`          // AI debounce window
	AICacheTTLMinutes  int         `json:"ai_cache_ttl_minutes"` // per-prefix AI cache TTL
	HistoryLines       int         `json:"history_lines"`        // recent lines sent to AI (sanitized)
	RecommendWeighting bool        `json:"recommend_weighting"`  // context bonus x1.5
	EnablePromptLine   bool        `json:"enable_prompt_line"`   // one-line enable banner on load
	LogEnabled         bool        `json:"log_enabled"`
	LogLevel           string      `json:"log_level"` // debug|info|warn|error
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Version:            1,
		Engine:             EngineHybrid,
		Trigger:            TriggerAuto,
		AI:                 AIConfig{BaseURL: "", Model: "", Temperature: 0.2, MaxTokens: 64, TimeoutMS: 5000},
		DebounceMS:         300,
		AICacheTTLMinutes:  5,
		HistoryLines:       10,
		RecommendWeighting: true,
		EnablePromptLine:   true,
		LogEnabled:         true,
		LogLevel:           "info",
	}
}

// envPrefix is the environment-variable prefix for overrides.
const envPrefix = "CMDPILOT_"

// baseDirOverride lets tests redirect the data directory (empty in production).
var baseDirOverride string

// BaseDir returns the per-user data directory for CmdPilot.
// Windows: %LOCALAPPDATA%\CmdPilot ; others: $XDG_DATA_HOME/cmdpilot or ~/.local/share/cmdpilot.
func BaseDir() string {
	if baseDirOverride != "" {
		return baseDirOverride
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "CmdPilot")
		}
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "cmdpilot")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "cmdpilot")
}

// Path returns the config file path.
func Path() string { return filepath.Join(BaseDir(), "config.json") }

// Load reads the config file, applies env overrides and returns it.
// If the file is corrupted it is backed up (config.json.bak-<ts>), defaults are
// written, and corrupted=true is returned so callers can surface a warning.
func Load() (cfg *Config, corrupted bool, err error) {
	cfg = Default()
	path := Path()
	data, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		if uerr := json.Unmarshal(data, cfg); uerr != nil {
			backupCorrupted(path, data)
			cfg = Default()
			corrupted = true
		}
	case os.IsNotExist(rerr):
		// first run: nothing to load
	default:
		return nil, false, fmt.Errorf("config: read %s: %w", path, rerr)
	}
	cfg.normalize()
	applyEnv(cfg)
	return cfg, corrupted, nil
}

// normalize clamps values to sane ranges and fills zero fields with defaults.
func (c *Config) normalize() {
	d := Default()
	if c.Version <= 0 {
		c.Version = d.Version
	}
	if c.Engine != EngineLocal && c.Engine != EngineHybrid && c.Engine != EngineAI {
		c.Engine = d.Engine
	}
	if c.Trigger != TriggerAuto && c.Trigger != TriggerTab {
		c.Trigger = d.Trigger
	}
	if c.DebounceMS <= 0 {
		c.DebounceMS = d.DebounceMS
	}
	if c.AICacheTTLMinutes <= 0 {
		c.AICacheTTLMinutes = d.AICacheTTLMinutes
	}
	if c.HistoryLines < 0 {
		c.HistoryLines = 0
	}
	if c.AI.Temperature < 0 || c.AI.Temperature > 2 {
		c.AI.Temperature = d.AI.Temperature
	}
	if c.AI.MaxTokens <= 0 {
		c.AI.MaxTokens = d.AI.MaxTokens
	}
	if c.AI.TimeoutMS <= 0 {
		c.AI.TimeoutMS = d.AI.TimeoutMS
	}
}

// applyEnv overlays CMDPILOT_* environment variables (highest precedence).
func applyEnv(c *Config) {
	env := func(k string) (string, bool) { v, ok := os.LookupEnv(envPrefix + k); return strings.TrimSpace(v), ok }
	if v, ok := env("ENGINE"); ok && v != "" {
		c.Engine = EngineMode(strings.ToLower(v))
	}
	if v, ok := env("TRIGGER"); ok && v != "" {
		c.Trigger = TriggerMode(strings.ToLower(v))
	}
	if v, ok := env("AI_BASE_URL"); ok {
		c.AI.BaseURL = v
	}
	if v, ok := env("AI_API_KEY"); ok {
		// Env-provided keys are kept in memory only (never persisted).
		c.AI.APIKeyEncrypted = ""
		c.AI.APIKeyEnv = v
	}
	if v, ok := env("AI_MODEL"); ok {
		c.AI.Model = v
	}
	if v, ok := env("AI_TIMEOUT_MS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AI.TimeoutMS = n
		}
	}
	if v, ok := env("AI_TEMPERATURE"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.AI.Temperature = f
		}
	}
	if v, ok := env("AI_MAX_TOKENS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AI.MaxTokens = n
		}
	}
	if v, ok := env("DEBOUNCE_MS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.DebounceMS = n
		}
	}
	if v, ok := env("LOG_LEVEL"); ok {
		c.LogLevel = v
	}
	if v, ok := env("LOG_ENABLED"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			c.LogEnabled = b
		}
	}
}

// backupCorrupted renames a corrupt config file aside before it is overwritten.
func backupCorrupted(path string, data []byte) {
	ts := time.Now().Format("20060102T150405")
	_ = os.WriteFile(path+".bak-"+ts, data, 0o644) //nolint:errcheck // best-effort
}

// Save writes the config atomically (tmp + rename).
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("config: write tmp: %w", err)
	}
	if err := os.Rename(tmp, Path()); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// APIKey returns the decrypted AI API key (from env override or DPAPI store).
func (c *Config) APIKey() (string, error) {
	if c.AI.APIKeyEnv != "" {
		return c.AI.APIKeyEnv, nil
	}
	if c.AI.APIKeyEncrypted == "" {
		return "", nil
	}
	return secrets.Unprotect(c.AI.APIKeyEncrypted)
}

// SetAPIKey encrypts and stores the AI API key in the config.
func (c *Config) SetAPIKey(plain string) error {
	enc, err := secrets.Protect(plain)
	if err != nil {
		return err
	}
	c.AI.APIKeyEncrypted = enc
	return nil
}

// configPaths is the set of dotted paths supported by `config get/set`.
var configPaths = []string{
	"engine", "trigger",
	"ai.base_url", "ai.api_key", "ai.model", "ai.temperature", "ai.max_tokens", "ai.timeout_ms",
	"debounce_ms", "ai_cache_ttl_minutes", "history_lines",
	"recommend_weighting", "enable_prompt_line", "log_enabled", "log_level",
}

// Paths returns the supported dotted config paths (for CLI help).
func Paths() []string { return configPaths }

// Get returns the value at a dotted path as a string.
func (c *Config) Get(path string) (string, error) {
	switch path {
	case "engine":
		return string(c.Engine), nil
	case "trigger":
		return string(c.Trigger), nil
	case "ai.base_url":
		return c.AI.BaseURL, nil
	case "ai.api_key":
		k, err := c.APIKey()
		if err != nil {
			return "", err
		}
		if k == "" {
			return "(not set)", nil
		}
		return "(set, hidden)", nil
	case "ai.model":
		return c.AI.Model, nil
	case "ai.temperature":
		return strconv.FormatFloat(c.AI.Temperature, 'f', -1, 64), nil
	case "ai.max_tokens":
		return strconv.Itoa(c.AI.MaxTokens), nil
	case "ai.timeout_ms":
		return strconv.Itoa(c.AI.TimeoutMS), nil
	case "debounce_ms":
		return strconv.Itoa(c.DebounceMS), nil
	case "ai_cache_ttl_minutes":
		return strconv.Itoa(c.AICacheTTLMinutes), nil
	case "history_lines":
		return strconv.Itoa(c.HistoryLines), nil
	case "recommend_weighting":
		return strconv.FormatBool(c.RecommendWeighting), nil
	case "enable_prompt_line":
		return strconv.FormatBool(c.EnablePromptLine), nil
	case "log_enabled":
		return strconv.FormatBool(c.LogEnabled), nil
	case "log_level":
		return c.LogLevel, nil
	}
	return "", fmt.Errorf("config: unknown path %q (valid: %s)", path, strings.Join(configPaths, ", "))
}

// Set assigns a value at a dotted path and persists the config.
// ai.api_key is encrypted with DPAPI before storage.
func (c *Config) Set(path, value string) error {
	switch path {
	case "engine":
		m := EngineMode(strings.ToLower(value))
		if m != EngineLocal && m != EngineHybrid && m != EngineAI {
			return fmt.Errorf("config: engine must be one of local|hybrid|ai")
		}
		c.Engine = m
	case "trigger":
		m := TriggerMode(strings.ToLower(value))
		if m != TriggerAuto && m != TriggerTab {
			return fmt.Errorf("config: trigger must be one of auto|tab")
		}
		c.Trigger = m
	case "ai.base_url":
		c.AI.BaseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	case "ai.api_key":
		if err := c.SetAPIKey(value); err != nil {
			return fmt.Errorf("config: cannot encrypt api key: %w", err)
		}
	case "ai.model":
		c.AI.Model = strings.TrimSpace(value)
	case "ai.temperature":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 2 {
			return fmt.Errorf("config: temperature must be a number in [0,2]")
		}
		c.AI.Temperature = f
	case "ai.max_tokens":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: max_tokens must be a positive integer")
		}
		c.AI.MaxTokens = n
	case "ai.timeout_ms":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: timeout_ms must be a positive integer")
		}
		c.AI.TimeoutMS = n
	case "debounce_ms":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("config: debounce_ms must be a non-negative integer")
		}
		c.DebounceMS = n
	case "ai_cache_ttl_minutes":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: ai_cache_ttl_minutes must be a positive integer")
		}
		c.AICacheTTLMinutes = n
	case "history_lines":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("config: history_lines must be a non-negative integer")
		}
		c.HistoryLines = n
	case "recommend_weighting":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: recommend_weighting must be true/false")
		}
		c.RecommendWeighting = b
	case "enable_prompt_line":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: enable_prompt_line must be true/false")
		}
		c.EnablePromptLine = b
	case "log_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("config: log_enabled must be true/false")
		}
		c.LogEnabled = b
	case "log_level":
		lv := strings.ToLower(value)
		if lv != "debug" && lv != "info" && lv != "warn" && lv != "error" {
			return fmt.Errorf("config: log_level must be debug|info|warn|error")
		}
		c.LogLevel = lv
	default:
		return fmt.Errorf("config: unknown path %q", path)
	}
	return c.Save()
}

// Effective returns the human-readable active mode description.
func (c *Config) Effective() string {
	switch c.Engine {
	case EngineLocal:
		return "local"
	case EngineAI:
		return "AI+local"
	default:
		return "hybrid(local+AI)"
	}
}
