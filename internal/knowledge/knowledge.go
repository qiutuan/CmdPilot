// Package knowledge provides CmdPilot's built-in command knowledge base.
//
// Storage decision (see docs/adr/0002-knowledge-storage.md): the built-in
// library is shipped as one gzip-compressed JSON blob embedded in the binary
// (immutable, versioned with the app, zero schema migration). User-provided
// data (favorites, overrides, statistics) lives in SQLite, keeping mutable and
// immutable data strictly separated.
package knowledge

import (
	"bytes"
	"compress/gzip"
	_ "embed" // required by //go:embed
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

//go:embed data/commands.json.gz
var embeddedGz []byte

// Shell identifies which terminal a command belongs to.
const (
	ShellCmd      = "cmd"      // CMD.exe builtin
	ShellPS       = "ps"       // PowerShell cmdlet
	ShellExternal = "external" // external tool (git/npm/docker/...)
)

// Command is one entry of the knowledge base.
type Command struct {
	Name     string   `json:"name"`     // command name, e.g. "git commit"
	Params   []string `json:"params"`   // common parameters/subcommands
	Desc     string   `json:"desc"`     // short Chinese description
	Examples []string `json:"examples"` // usage examples
	Shell    string   `json:"shell"`    // cmd | ps | external
	Tags     []string `json:"tags"`     // category labels
}

// DB is the parsed knowledge base.
type DB struct {
	Version  int       `json:"version"`
	Commands []Command `json:"commands"`
}

// Load decompresses and parses the embedded command database.
func Load() (*DB, error) {
	zr, err := gzip.NewReader(bytes.NewReader(embeddedGz))
	if err != nil {
		return nil, fmt.Errorf("knowledge: open gzip: %w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("knowledge: read gzip: %w", err)
	}
	if err := zr.Close(); err != nil {
		return nil, fmt.Errorf("knowledge: close gzip: %w", err)
	}
	var db DB
	if err := json.Unmarshal(raw, &db); err != nil {
		return nil, fmt.Errorf("knowledge: parse json: %w", err)
	}
	if db.Version <= 0 {
		return nil, fmt.Errorf("knowledge: invalid schema version %d", db.Version)
	}
	sort.Slice(db.Commands, func(i, j int) bool {
		if db.Commands[i].Shell != db.Commands[j].Shell {
			return db.Commands[i].Shell < db.Commands[j].Shell
		}
		return db.Commands[i].Name < db.Commands[j].Name
	})
	return &db, nil
}

// Count returns the total number of commands.
func (d *DB) Count() int { return len(d.Commands) }

// ByShell returns commands for a given shell ("cmd", "ps", "external").
func (d *DB) ByShell(shell string) []Command {
	var out []Command
	for _, c := range d.Commands {
		if c.Shell == shell {
			out = append(out, c)
		}
	}
	return out
}

// Find returns the first command with the given name, or nil.
func (d *DB) Find(name string) *Command {
	for i := range d.Commands {
		if d.Commands[i].Name == name {
			return &d.Commands[i]
		}
	}
	return nil
}

// All returns a copy of all commands.
func (d *DB) All() []Command {
	out := make([]Command, len(d.Commands))
	copy(out, d.Commands)
	return out
}
