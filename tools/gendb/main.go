// Command gendb merges the three human-editable command JSON sources
// (cmd.json / ps.json / external.json) into the single gzip-compressed blob
// embedded in the binary, plus a readable merged JSON for review.
//
// Usage: go run ./tools/gendb
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// db mirrors internal/knowledge.DB for generation.
type db struct {
	Version  int       `json:"version"`
	Commands []command `json:"commands"`
}

type command struct {
	Name     string   `json:"name"`
	Params   []string `json:"params"`
	Desc     string   `json:"desc"`
	Examples []string `json:"examples"`
	Shell    string   `json:"shell"`
	Tags     []string `json:"tags"`
}

const schemaVersion = 1

func main() {
	dir := filepath.Join("internal", "knowledge", "data")
	sources := []string{"cmd.json", "ps.json", "external.json"}

	merged := db{Version: schemaVersion}
	seen := map[string]bool{}
	for _, src := range sources {
		raw, err := os.ReadFile(filepath.Join(dir, src))
		if err != nil {
			fatal("read %s: %v", src, err)
		}
		var part struct {
			Version  int       `json:"version"`
			Commands []command `json:"commands"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			fatal("parse %s: %v", src, err)
		}
		for _, c := range part.Commands {
			if c.Name == "" || c.Desc == "" {
				fatal("%s: entry with empty name/desc", src)
			}
			if seen[c.Name] {
				fatal("%s: duplicate command %q", src, c.Name)
			}
			seen[c.Name] = true
			merged.Commands = append(merged.Commands, c)
		}
	}
	sort.Slice(merged.Commands, func(i, j int) bool {
		if merged.Commands[i].Shell != merged.Commands[j].Shell {
			return merged.Commands[i].Shell < merged.Commands[j].Shell
		}
		return merged.Commands[i].Name < merged.Commands[j].Name
	})

	pretty, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	pretty = append(pretty, '\n')
	if err := os.WriteFile(filepath.Join(dir, "commands.json"), pretty, 0o644); err != nil {
		fatal("write commands.json: %v", err)
	}

	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write(pretty); err != nil {
		fatal("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		fatal("gzip close: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commands.json.gz"), gzBuf.Bytes(), 0o644); err != nil {
		fatal("write commands.json.gz: %v", err)
	}

	fmt.Printf("gendb: %d commands merged (%s) -> commands.json + commands.json.gz (%.1f KB)\n",
		len(merged.Commands), summarizeCounts(merged.Commands), float64(gzBuf.Len())/1024)
}

func summarizeCounts(cmds []command) string {
	var cmdN, psN, extN int
	for _, c := range cmds {
		switch c.Shell {
		case "cmd":
			cmdN++
		case "ps":
			psN++
		case "external":
			extN++
		}
	}
	return fmt.Sprintf("cmd=%d ps=%d external=%d", cmdN, psN, extN)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gendb: "+format+"\n", args...)
	os.Exit(1)
}
