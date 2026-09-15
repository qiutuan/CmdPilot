// cmdpilot-clink is the tiny companion executable for the Clink (CMD) plugin.
// Clink's Lua has no HTTP client, so this binary connects to the local daemon
// over loopback, prints the JSON completion response to stdout, and exits.
// It is designed to start fast (<10ms) to stay within interactive budget.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/daemonstate"
)

func main() {
	input := flag.String("input", "", "current input")
	shell := flag.String("shell", "cmd", "cmd|ps")
	cwd := flag.String("cwd", "", "working directory")
	history := flag.String("history", "", "recent history joined by \\x1f")
	ensure := flag.Bool("ensure", true, "start daemon if missing")
	flag.Parse()

	c := daemonClient()
	if c == nil {
		if *ensure {
			// Fall back to spawning the daemon via the main binary.
			if code := ensureDaemon(); code != 0 {
				fmt.Fprintln(os.Stderr, `{"error":"daemon unavailable"}`)
				os.Exit(code)
			}
			c = daemonClient()
		}
		if c == nil {
			fmt.Fprintln(os.Stderr, `{"error":"daemon unavailable"}`)
			os.Exit(1)
		}
	}
	var hist []string
	if *history != "" {
		for _, h := range split(*history) {
			if h != "" {
				hist = append(hist, h)
			}
		}
	}
	resp, err := c.Complete(client.CompleteReq{Input: *input, Shell: *shell, CWD: *cwd, History: hist, Trigger: "auto"})
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error":%q}`, err.Error())
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(resp)
}

// split splits on the unit separator used by the Lua adapter.
func split(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == 0x1f {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

// ensureDaemon runs `cmdpilot daemon ensure` (blocking) and returns its code.
func ensureDaemon() int {
	c := daemonClient()
	if c != nil {
		if ok, _ := c.Health(); ok {
			return 0
		}
		daemonstate.Remove()
	}
	return runEnsure()
}
