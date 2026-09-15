// cmdpilot-clink is the tiny companion executable for the Clink (CMD) plugin.
// Clink's Lua has no HTTP client and no os.popen, so this binary connects to
// the local daemon over loopback and writes the JSON completion response
// either to stdout or to a file (--request/--output keep complex inputs out of
// cmd.exe quoting). It is designed to start fast (<10ms) to stay within the
// interactive budget.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
)

// requestFile is the JSON request layout written by the Lua adapter.
type requestFile struct {
	Input   string   `json:"input"`
	Shell   string   `json:"shell"`
	CWD     string   `json:"cwd"`
	History []string `json:"history"`
	Trigger string   `json:"trigger"`
}

func main() {
	input := flag.String("input", "", "current input (flag mode)")
	shell := flag.String("shell", "cmd", "cmd|ps")
	cwd := flag.String("cwd", "", "working directory")
	history := flag.String("history", "", "recent history joined by \\x1f")
	ensure := flag.Bool("ensure", true, "start daemon if missing")
	request := flag.String("request", "", "JSON request file (file mode)")
	output := flag.String("output", "", "JSON output file (file mode)")
	report := flag.String("report", "", "record an executed command (--report \"full command\")")
	reportDir := flag.String("report-dir", "", "directory of the executed command")
	reportShell := flag.String("report-shell", "cmd", "shell of the executed command")
	timeout := flag.Duration("timeout", 2*time.Second, "complete timeout")
	flag.Parse()

	if *report != "" {
		os.Exit(reportUsage(*report, *reportDir, *reportShell, *ensure))
	}

	var req client.CompleteReq
	if *request != "" {
		raw, err := os.ReadFile(*request)
		if err != nil {
			fmt.Fprintf(os.Stderr, `{"error":%q}`, err.Error())
			os.Exit(1)
		}
		var rf requestFile
		if err := json.Unmarshal(raw, &rf); err != nil {
			fmt.Fprintf(os.Stderr, `{"error":%q}`, err.Error())
			os.Exit(1)
		}
		req = client.CompleteReq{Input: rf.Input, Shell: rf.Shell, CWD: rf.CWD, History: rf.History, Trigger: rf.Trigger}
		if req.Shell == "" {
			req.Shell = "cmd"
		}
	} else {
		var hist []string
		if *history != "" {
			for _, h := range split(*history) {
				if h != "" {
					hist = append(hist, h)
				}
			}
		}
		req = client.CompleteReq{Input: *input, Shell: *shell, CWD: *cwd, History: hist, Trigger: "auto"}
	}

	c := daemonClient()
	if c == nil {
		if *ensure {
			if code := ensureDaemon(); code != 0 {
				emitError(nil, *output, `{"error":"daemon unavailable"}`)
				os.Exit(code)
			}
			c = daemonClient()
		}
		if c == nil {
			emitError(nil, *output, `{"error":"daemon unavailable"}`)
			os.Exit(1)
		}
	}
	cc := *c
	cc.HTTP.Timeout = *timeout
	resp, err := cc.Complete(req)
	if err != nil {
		emitError(&cc, *output, fmt.Sprintf(`{"error":%q}`, err.Error()))
		os.Exit(1)
	}
	emitJSON(resp, *output)
}

// emitJSON writes the payload to the output file when given, else stdout.
func emitJSON(v any, output string) {
	if output == "" {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(v)
		return
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(output, raw, 0o644)
}

// emitError writes an error payload (file or stdout).
func emitError(c *client.Client, output, msg string) {
	if output != "" {
		_ = os.WriteFile(output, []byte(msg), 0o644)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// reportUsage records an executed command; exit code 0 even on failure so the
// shell pipeline is never disturbed.
func reportUsage(cmd, dir, shell string, ensure bool) int {
	c := daemonClient()
	if c == nil {
		if ensure {
			if code := ensureDaemon(); code != 0 {
				return 0
			}
			c = daemonClient()
		}
		if c == nil {
			return 0
		}
	}
	cc := *c
	cc.HTTP.Timeout = 1 * time.Second
	_ = cc.ReportUsage(cmd, dir, shell)
	return 0
}

// split splits on the unit separator used by the Lua adapter.
func split(s string) []string {
	return strings.Split(s, "\x1f")
}
