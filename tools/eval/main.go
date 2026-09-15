// Command eval runs CmdPilot's automated evaluation & report suite:
//
//	go run ./tools/eval all        # everything, writes docs/reports/*
//	go run ./tools/eval eval       # engine Top-5 hit rate on the scenario set
//	go run ./tools/eval perf       # latency / memory / idle-CPU / debounce
//	go run ./tools/eval degrade    # AI failure degradation matrix
//	go run ./tools/eval sanitize   # secret-leak capture against a mock AI server
//	go run ./tools/eval stable     # kill -9 stability & DB integrity
//	go run ./tools/eval e2e        # critical-path end-to-end flows
//	go run ./tools/eval coverage   # unit-test coverage report
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const reportsDir = "docs/reports"

func main() {
	cmd := "all"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	start := time.Now()
	var failures []string
	run := func(name string, fn func() error) {
		t0 := time.Now()
		fmt.Printf("== %s ... ", name)
		if err := fn(); err != nil {
			fmt.Printf("FAIL (%v): %v\n", time.Since(t0).Round(time.Millisecond), err)
			failures = append(failures, name)
			return
		}
		fmt.Printf("ok (%v)\n", time.Since(t0).Round(time.Millisecond))
	}
	switch cmd {
	case "eval":
		run("engine-eval", runEval)
	case "perf":
		run("performance", runPerf)
	case "degrade":
		run("degradation", runDegrade)
	case "sanitize":
		run("sanitize-capture", runSanitize)
	case "stable":
		run("stability", runStable)
	case "e2e":
		run("e2e", runE2E)
	case "coverage":
		run("coverage", runCoverage)
	case "all":
		run("engine-eval", runEval)
		run("coverage", runCoverage)
		run("degradation", runDegrade)
		run("sanitize-capture", runSanitize)
		run("stability", runStable)
		run("e2e", runE2E)
		run("performance", runPerf)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		os.Exit(2)
	}
	fmt.Printf("\nreport dir: %s (%s)\n", filepath.Join(mustWD(), reportsDir), time.Since(start).Round(time.Millisecond))
	if len(failures) > 0 {
		fmt.Printf("FAILED steps: %s\n", strings.Join(failures, ", "))
		os.Exit(1)
	}
	fmt.Println("ALL STEPS GREEN")
}

func mustWD() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// writeReport writes both markdown and json artifacts for one report.
func writeReport(name string, md string, jsonData []byte) error {
	if err := os.WriteFile(filepath.Join(reportsDir, name+".md"), []byte(md), 0o644); err != nil {
		return err
	}
	if jsonData != nil {
		if err := os.WriteFile(filepath.Join(reportsDir, name+".json"), jsonData, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
