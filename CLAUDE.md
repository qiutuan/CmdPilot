# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

CmdPilot — a Windows command-line completion assistant: a PSReadLine predictor for PowerShell plus a Clink plugin for CMD. Dual-engine design: local knowledge base + usage-frequency ranking, optionally enhanced by an OpenAI-compatible AI. Pure-Go, no CGO (modernc.org/sqlite). Repo documentation, CLI output, and commit messages are in Chinese — keep that convention when writing new docs/commits.

## Commands

```powershell
# Build (both binaries). Installer uses: CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"
go build ./cmd/cmdpilot ./cmd/cmdpilot-clink

# Full unit tests — always use -count=1 (README/eval rely on no test cache)
go test -count=1 ./...
# Single test / package
go test -count=1 -run TestComplete ./internal/completion
# Benchmarks (match/rank are pure functions with benchmarks)
go test -bench=. -run=^$ ./internal/match

# Seven-part eval suite → regenerates docs/reports/*.{md,json} (committed artifacts)
go run ./tools/eval all   # subcommands: eval perf degrade sanitize stable e2e coverage

# Regenerate the embedded knowledge base after editing source JSON
go run ./tools/gendb

# One-click install / uninstall
powershell -ExecutionPolicy Bypass -File installer\install.ps1

# CLI smoke test without a terminal
go run ./cmd/cmdpilot complete --input "git st" --shell cmd --json
```

Cross-compilation is a real workflow (the daemon also targets Linux/macOS), e.g. `GOOS=linux GOARCH=amd64 go build ./cmd/cmdpilot`. Windows-only code is split by `//go:build windows` / `//go:build !windows` files (e.g. `internal/secrets`, `internal/cli/detach_*`); keep both variants building when touching those areas.

## Architecture

Three cooperating processes; the core is terminal-agnostic:

```
PowerShell PSReadLine predictor          CMD + Clink Lua generator
   (adapters/powershell)                    (adapters/clink/cmdpilot.lua)
        │ JSON request/output file               │ line protocol (plain text)
        ▼                                         ▼
   cmd/cmdpilot-clink  (companion → daemon bridge; must start <10ms;
                        every failure is silent = exit != 0 so the shell never breaks)
        │ HTTP loopback 127.0.0.1:<random port>   Bearer token (32B hex)
        ▼
   cmd/cmdpilot daemon (internal/server)  ← the single process holding state
        ├── SQLite (modernc.org/sqlite, WAL): usage_stats / recent_cmds /
        │    favorites / user_commands — %LOCALAPPDATA%\CmdPilot\cmdpilot.db
        └── embedded knowledge base (492 commands, gzip JSON)
```

- **Adapters** (`adapters/`) talk to core only via HTTP/JSON or the plain line protocol — no other coupling.
- **Companion** (`cmd/cmdpilot-clink`) supports three encodings: flag mode (`--input/--shell/...`), JSON request/output file (PowerShell module), and plain line mode. The line protocol uses `cmdpilot-req-v1`/`cmdpilot-resp-v1` with `\x1e`/`\x1f` stuffing that is defined **twice**: in `adapters/clink/cmdpilot.lua` and `cmd/cmdpilot-clink/main.go` (escaping rules documented in main.go) — keep them in sync.
- **Daemon** (`internal/server`): loopback-only HTTP JSON API; bearer-token auth on every route except `/health`. Routes: `/complete /report /recommend /stats /ai/test /shutdown`. Discovery via `internal/daemonstate`, which writes `daemon.json` (pid+port+token) into the data dir.
- **CLI** (`internal/cli`): standalone `cmdpilot <command>`; works without a daemon (reads SQLite/knowledge/config directly) except `ai test`.
- **Core** (`internal/...`) imports no terminal APIs. `completion.Engine` orchestrates: context detection (command/subcommand/param/path/git-repo) → local candidates (knowledge + favorites + user_commands + history + chain recommendations) → `match` (4 tiers: exact/prefix/fuzzy/edit ≤2) + `rank` scoring (`log(1+count) × 0.5^(age/30d) × context bonus 1.5`) → optional AI enhancement.

### Completion data flow (`internal/completion/engine.go`)

`Complete()` always returns the local result synchronously. If engine mode ≠ `local` and AI is configured, it kicks off a background fetch: 300ms debounce, single in-flight (`aiInFlight`), per-prefix 5-min cache. `WaitAIMS > 0` (Tab trigger) blocks briefly waiting for an AI result. On any AI failure it silently keeps the local result — **AI must never block terminal input**.

### AI request

POST `{base_url}/v1/chat/completions`. The user message = sanitized history (last `history_lines`) + local top-5 candidates + input + cwd. History passes through `sanitize.Sanitize` first — lines containing key/token/password/secret/Bearer are dropped before anything leaves the machine. AI logs record metadata only (never the key, prompt, or history content).

### Storage

- SQLite (pure-Go driver): WAL + synchronous=NORMAL + `user_version` migration (current v1).
- `config.json` in the data dir; a corrupted file is backed up to `config.json.bak-<ts>` and replaced with defaults — the tool never fails to start. Precedence: built-in defaults < config.json < `CMDPILOT_*` env vars.
- AI key is DPAPI-encrypted into `ai.api_key_encrypted` (`config.SetAPIKey`/`APIKey`), decrypted only in memory; `CMDPILOT_AI_API_KEY` stays in memory only and is never persisted. On non-Windows, secrets are a `dev:`-prefix placeholder.

## Knowledge base (embedded)

Human-editable sources: `internal/knowledge/data/{cmd,ps,external}.json`. Run `go run ./tools/gendb` to merge and validate (rejects empty name/desc, duplicate command names) into `commands.json` (readable) + `commands.json.gz` (embedded via `//go:embed` in `internal/knowledge`). Any source change requires regenerating the blob and rebuilding the binary.

## Tests & eval

- Unit tests are plain `go test`, always `-count=1`. Config tests redirect the data dir via `config.BaseDirOverrideForTest`; eval isolates test data via `XDG_DATA_HOME`/`LOCALAPPDATA` env vars.
- `tools/eval` is the acceptance gate: `go run ./tools/eval all` runs engine hit-rate (231 real scenarios), coverage (core ≥85%, AI parse 100% — enforced), AI-failure degradation matrix, secret-leak capture (against an in-test mock AI server), kill -9 stability, E2E against a real built daemon, and perf. Reports under `docs/reports/` are committed but generated — regenerate, don't hand-edit.
- `tools/demo/mock-ai` is a tiny OpenAI-compatible mock server (`go build ./tools/demo/mock-ai`) for demos/tests without real API spend; a prebuilt `mock-ai` binary lives at the repo root (committed, not gitignored).

## Commit conventions

Conventional commits with a Chinese subject, e.g. `fix(build): ...`, `test(eval): ...`, `docs(demo): ...` (see git history). Docs under `docs/` are written in Chinese.
