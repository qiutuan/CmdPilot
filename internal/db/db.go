// Package db provides the SQLite storage layer for CmdPilot.
//
// Guarantees (quality bar #2):
//   - WAL journal mode + synchronous=NORMAL + busy_timeout
//   - schema versioning via PRAGMA user_version with explicit migrations
//   - all writes go through transactions
//   - one connection, opened lazily, safe for concurrent use
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// schemaVersion is the current schema version (migrated from 0).
const schemaVersion = 1

// DB wraps the SQLite handle.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database in dir and migrates it.
// The directory is created when missing.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("db: create data dir: %w", err)
	}
	path := filepath.Join(dir, "cmdpilot.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // single writer, avoids SQLITE_BUSY in WAL mode
	d := &DB{sql: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error {
	return d.sql.Close()
}

// migrate applies schema migrations based on PRAGMA user_version.
func (d *DB) migrate() error {
	var v int
	if err := d.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("db: read user_version: %w", err)
	}
	if v > schemaVersion {
		return fmt.Errorf("db: database schema version %d is newer than supported %d", v, schemaVersion)
	}
	if v < 1 {
		if err := d.migrateV1(); err != nil {
			return err
		}
	}
	return nil
}

// migrateV1 creates the initial schema.
func (d *DB) migrateV1() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usage_stats (
			command  TEXT NOT NULL,
			dir      TEXT NOT NULL DEFAULT '',
			shell    TEXT NOT NULL DEFAULT '',
			count    INTEGER NOT NULL DEFAULT 1,
			last_used INTEGER NOT NULL,
			PRIMARY KEY (command, dir, shell)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_last ON usage_stats(last_used)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_count ON usage_stats(count)`,
		`CREATE TABLE IF NOT EXISTS recent_commands (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			command TEXT NOT NULL,
			dir     TEXT NOT NULL DEFAULT '',
			shell   TEXT NOT NULL DEFAULT '',
			at      INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_recent_at ON recent_commands(at)`,
		`CREATE TABLE IF NOT EXISTS favorites (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL UNIQUE,
			command    TEXT NOT NULL,
			note       TEXT NOT NULL DEFAULT '',
			tags       TEXT NOT NULL DEFAULT '',
			shell      TEXT NOT NULL DEFAULT 'both',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_commands (
			name     TEXT PRIMARY KEY,
			params   TEXT NOT NULL DEFAULT '',
			desc     TEXT NOT NULL DEFAULT '',
			examples TEXT NOT NULL DEFAULT '',
			shell    TEXT NOT NULL DEFAULT 'external',
			tags     TEXT NOT NULL DEFAULT ''
		)`,
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("db: begin migrate v1: %w", err)
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			tx.Rollback()
			return fmt.Errorf("db: migrate v1: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", 1)); err != nil {
		tx.Rollback()
		return fmt.Errorf("db: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit migrate v1: %w", err)
	}
	return nil
}

// Integrity runs PRAGMA integrity_check (used by stability tests and self-check).
func (d *DB) Integrity() (string, error) {
	var out string
	if err := d.sql.QueryRow("PRAGMA integrity_check").Scan(&out); err != nil {
		return "", fmt.Errorf("db: integrity check: %w", err)
	}
	return out, nil
}

// DBPath returns the on-disk database path for a data directory (self-check / privacy docs).
func DBPath(dir string) string { return filepath.Join(dir, "cmdpilot.db") }

// --- usage statistics (M5) ---

// UsageRecord describes one executed command observation.
type UsageRecord struct {
	Command string
	Dir     string
	Shell   string
	At      time.Time
}

// RecordUsage upserts a usage counter and appends to the recent-command log.
func (d *DB) RecordUsage(r UsageRecord) error {
	if r.Command == "" {
		return nil
	}
	ts := r.At.Unix()
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("db: begin record: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(
		`INSERT INTO usage_stats (command, dir, shell, count, last_used) VALUES (?, ?, ?, 1, ?)
		 ON CONFLICT(command, dir, shell) DO UPDATE SET
		   count = count + 1, last_used = excluded.last_used`,
		r.Command, r.Dir, r.Shell, ts); err != nil {
		return fmt.Errorf("db: record usage: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO recent_commands (command, dir, shell, at) VALUES (?, ?, ?, ?)`,
		r.Command, r.Dir, r.Shell, ts); err != nil {
		return fmt.Errorf("db: record recent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit record: %w", err)
	}
	return nil
}

// UsageRow aggregates one command's statistics.
type UsageRow struct {
	Command  string
	Count    int
	LastUsed time.Time
	Dirs     string // comma-joined distinct dirs
	Shells   string // comma-joined distinct shells
}

// TopN returns the top-N most-used commands overall (optionally since a time).
func (d *DB) TopN(n int, since time.Time) ([]UsageRow, error) {
	q := `SELECT command, SUM(count), MAX(last_used),
	             GROUP_CONCAT(DISTINCT dir), GROUP_CONCAT(DISTINCT shell)
	      FROM usage_stats`
	var args []any
	if !since.IsZero() {
		q += ` WHERE last_used >= ?`
		args = append(args, since.Unix())
	}
	q += ` GROUP BY command ORDER BY SUM(count) DESC LIMIT ?`
	args = append(args, n)
	return d.queryUsage(q, args...)
}

// TopNByDir returns the top-N commands for a specific directory.
func (d *DB) TopNByDir(dir string, n int) ([]UsageRow, error) {
	return d.queryUsage(
		`SELECT command, SUM(count), MAX(last_used),
		        GROUP_CONCAT(DISTINCT dir), GROUP_CONCAT(DISTINCT shell)
		 FROM usage_stats WHERE dir = ?
		 GROUP BY command ORDER BY SUM(count) DESC LIMIT ?`, dir, n)
}

func (d *DB) queryUsage(q string, args ...any) ([]UsageRow, error) {
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query usage: %w", err)
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var last int64
		if err := rows.Scan(&r.Command, &r.Count, &last, &r.Dirs, &r.Shells); err != nil {
			return nil, fmt.Errorf("db: scan usage: %w", err)
		}
		r.LastUsed = time.Unix(last, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentCommands returns the most recent distinct executed commands (for chain
// recommendation), optionally filtered by directory.
func (d *DB) RecentCommands(n int, dir string) ([]string, error) {
	q := `SELECT command FROM recent_commands`
	var args []any
	if dir != "" {
		q += ` WHERE dir = ?`
		args = append(args, dir)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, n*4) // fetch extra, dedupe below
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query recent: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}
	return out, rows.Err()
}

// StatsDistribution returns per-directory and per-shell usage distribution.
func (d *DB) StatsDistribution() (byDir map[string]int, byShell map[string]int, total int, err error) {
	byDir = map[string]int{}
	byShell = map[string]int{}
	rows, err := d.sql.Query(`SELECT dir, shell, count FROM usage_stats`)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("db: query distribution: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dir, shell string
		var c int
		if err := rows.Scan(&dir, &shell, &c); err != nil {
			return nil, nil, 0, err
		}
		byDir[dir] += c
		byShell[shell] += c
		total += c
	}
	return byDir, byShell, total, rows.Err()
}

// TrendPoint is one day's command-execution count.
type TrendPoint struct {
	Day   string // YYYY-MM-DD (local time)
	Count int
}

// UsageTrend returns per-day execution counts for the last `days` days.
func (d *DB) UsageTrend(days int) ([]TrendPoint, error) {
	if days <= 0 {
		days = 14
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	rows, err := d.sql.Query(
		`SELECT date(at, 'unixepoch', 'localtime') AS day, COUNT(*) FROM recent_commands
		 WHERE at >= ? GROUP BY day ORDER BY day`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("db: query trend: %w", err)
	}
	defer rows.Close()
	var out []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Day, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClearStats wipes usage statistics but preserves favorites and user commands.
func (d *DB) ClearStats() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM usage_stats`); err != nil {
		return fmt.Errorf("db: clear usage_stats: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM recent_commands`); err != nil {
		return fmt.Errorf("db: clear recent_commands: %w", err)
	}
	return tx.Commit()
}

// --- favorites (M4) ---

// Favorite is a user-saved command snippet.
type Favorite struct {
	ID        int64
	Name      string
	Command   string
	Note      string
	Tags      string
	Shell     string
	CreatedAt time.Time
}

// AddFavorite inserts a favorite (name must be unique) and returns its ID.
func (d *DB) AddFavorite(f Favorite) (int64, error) {
	res, err := d.sql.Exec(
		`INSERT INTO favorites (name, command, note, tags, shell, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		f.Name, f.Command, f.Note, f.Tags, f.Shell, f.CreatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("db: add favorite: %w", err)
	}
	return res.LastInsertId()
}

// ListFavorites returns all favorites ordered by name.
func (d *DB) ListFavorites() ([]Favorite, error) {
	rows, err := d.sql.Query(`SELECT id, name, command, note, tags, shell, created_at FROM favorites ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list favorites: %w", err)
	}
	defer rows.Close()
	return scanFavorites(rows)
}

// GetFavorite returns one favorite by id.
func (d *DB) GetFavorite(id int64) (*Favorite, error) {
	row := d.sql.QueryRow(`SELECT id, name, command, note, tags, shell, created_at FROM favorites WHERE id = ?`, id)
	f, err := scanFavorite(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// UpdateFavorite overwrites a favorite's fields (name stays unique).
func (d *DB) UpdateFavorite(f Favorite) error {
	_, err := d.sql.Exec(
		`UPDATE favorites SET name=?, command=?, note=?, tags=?, shell=? WHERE id=?`,
		f.Name, f.Command, f.Note, f.Tags, f.Shell, f.ID)
	if err != nil {
		return fmt.Errorf("db: update favorite: %w", err)
	}
	return nil
}

// DeleteFavorite removes a favorite by id.
func (d *DB) DeleteFavorite(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM favorites WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete favorite: %w", err)
	}
	return nil
}

// SearchFavorites returns favorites whose name/command/note/tags contain q.
func (d *DB) SearchFavorites(q string) ([]Favorite, error) {
	like := "%" + q + "%"
	rows, err := d.sql.Query(
		`SELECT id, name, command, note, tags, shell, created_at FROM favorites
		 WHERE name LIKE ? OR command LIKE ? OR note LIKE ? OR tags LIKE ? ORDER BY name`,
		like, like, like, like)
	if err != nil {
		return nil, fmt.Errorf("db: search favorites: %w", err)
	}
	defer rows.Close()
	return scanFavorites(rows)
}

func scanFavorites(rows *sql.Rows) ([]Favorite, error) {
	var out []Favorite
	for rows.Next() {
		var f Favorite
		var created int64
		if err := rows.Scan(&f.ID, &f.Name, &f.Command, &f.Note, &f.Tags, &f.Shell, &created); err != nil {
			return nil, fmt.Errorf("db: scan favorite: %w", err)
		}
		f.CreatedAt = time.Unix(created, 0)
		out = append(out, f)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanFavorite(s rowScanner) (Favorite, error) {
	var f Favorite
	var created int64
	err := s.Scan(&f.ID, &f.Name, &f.Command, &f.Note, &f.Tags, &f.Shell, &created)
	f.CreatedAt = time.Unix(created, 0)
	return f, err
}

// --- user command overrides (M1: 用户可覆盖内置库) ---

// UserCommand mirrors a knowledge.Command entry stored in the user table.
type UserCommand struct {
	Name     string
	Params   []string
	Desc     string
	Examples []string
	Shell    string
	Tags     []string
}

// SetUserCommand inserts or replaces a user override.
func (d *DB) SetUserCommand(c UserCommand) error {
	_, err := d.sql.Exec(
		`INSERT INTO user_commands (name, params, desc, examples, shell, tags) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET params=excluded.params, desc=excluded.desc,
		   examples=excluded.examples, shell=excluded.shell, tags=excluded.tags`,
		c.Name, joinList(c.Params), c.Desc, joinList(c.Examples), c.Shell, joinList(c.Tags))
	if err != nil {
		return fmt.Errorf("db: set user command: %w", err)
	}
	return nil
}

// UserCommands returns all user overrides.
func (d *DB) UserCommands() ([]UserCommand, error) {
	rows, err := d.sql.Query(`SELECT name, params, desc, examples, shell, tags FROM user_commands ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list user commands: %w", err)
	}
	defer rows.Close()
	var out []UserCommand
	for rows.Next() {
		var c UserCommand
		var params, examples, tags string
		if err := rows.Scan(&c.Name, &params, &c.Desc, &examples, &c.Shell, &tags); err != nil {
			return nil, err
		}
		c.Params = splitList(params)
		c.Examples = splitList(examples)
		c.Tags = splitList(tags)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteUserCommand removes a user override.
func (d *DB) DeleteUserCommand(name string) error {
	_, err := d.sql.Exec(`DELETE FROM user_commands WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("db: delete user command: %w", err)
	}
	return nil
}

// joinList encodes a string slice with an unlikely separator (vertical bar).
func joinList(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += "|"
		}
		out += it
	}
	return out
}

// splitList decodes joinList output.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// ensureExists is a helper used by tests to confirm the file materializes.
func ensureExists(dir string) error {
	_, err := os.Stat(filepath.Join(dir, "cmdpilot.db"))
	return err
}
