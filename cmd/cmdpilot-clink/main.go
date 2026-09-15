// cmdpilot-clink is the tiny companion executable for the Clink (CMD) plugin.
// Clink's Lua has no HTTP client and no os.popen, so this binary connects to
// the local daemon over loopback and writes the completion response either to
// stdout or to a file. Three request/response encodings are supported so the
// Lua adapter never has to deal with cmd.exe quoting or JSON:
//
//  1. flag mode:  --input/--shell/--cwd/--history (simple shells)
//  2. JSON file:  --request file.json --output out.json (PowerShell module)
//  3. line mode:  --request file (cmdpilot-req-v1) --output file --plain (Clink)
//     --report "cmd" and --report-batch file (usage recording)
//
// The binary is designed to start fast (<10ms) to stay within the interactive
// budget; all failures are silent (exit != 0) so the shell never breaks.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/completion"
)

// requestFile is the JSON request layout written by the PowerShell module.
type requestFile struct {
	Input   string   `json:"input"`
	Shell   string   `json:"shell"`
	CWD     string   `json:"cwd"`
	History []string `json:"history"`
	Trigger string   `json:"trigger"`
}

// line-mode separators (Clink Lua has no JSON).
const (
	reqMagic   = "cmdpilot-req-v1"
	respMagic  = "cmdpilot-resp-v1"
	fieldSep   = "\x1f" // between fields on one line
	lineSep    = "\x1e" // escapes: \x1e -> \x1e\x1e, \x1f -> \x1e\x1f
	historySep = "\x1f" // between history entries inside the history value
)

func main() {
	input := flag.String("input", "", "current input (flag mode)")
	shell := flag.String("shell", "cmd", "cmd|ps")
	cwd := flag.String("cwd", "", "working directory")
	history := flag.String("history", "", "recent history joined by \\x1f")
	ensure := flag.Bool("ensure", true, "start daemon if missing")
	request := flag.String("request", "", "request file (JSON or cmdpilot-req-v1 line format)")
	output := flag.String("output", "", "output file")
	plain := flag.Bool("plain", false, "write cmdpilot-resp-v1 line format instead of JSON")
	report := flag.String("report", "", "record an executed command (--report \"full command\")")
	reportDir := flag.String("report-dir", "", "directory of the executed command")
	reportShell := flag.String("report-shell", "cmd", "shell of the executed command")
	reportBatch := flag.String("report-batch", "", "file with executed commands (line format)")
	timeout := flag.Duration("timeout", 2*time.Second, "complete timeout")
	flag.Parse()

	if *report != "" {
		os.Exit(reportUsage(*report, *reportDir, *reportShell, *ensure))
	}
	if *reportBatch != "" {
		os.Exit(reportBatchUsage(*reportBatch, *ensure))
	}

	var req client.CompleteReq
	if *request != "" {
		raw, err := os.ReadFile(*request)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read request: %v", err)
			os.Exit(1)
		}
		if strings.HasPrefix(string(raw), reqMagic+"\n") {
			req = parseLineRequest(string(raw))
		} else {
			var rf requestFile
			if err := json.Unmarshal(raw, &rf); err != nil {
				fmt.Fprintf(os.Stderr, "bad request: %v", err)
				os.Exit(1)
			}
			req = client.CompleteReq{Input: rf.Input, Shell: rf.Shell, CWD: rf.CWD, History: rf.History, Trigger: rf.Trigger}
		}
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
				emitError(*output, "daemon unavailable")
				os.Exit(code)
			}
			c = daemonClient()
		}
		if c == nil {
			emitError(*output, "daemon unavailable")
			os.Exit(1)
		}
	}
	cc := *c
	cc.HTTP.Timeout = *timeout
	resp, err := cc.Complete(req)
	if err != nil {
		emitError(*output, "daemon error: "+err.Error())
		os.Exit(1)
	}
	if *plain {
		emitPlain(resp, *output)
		return
	}
	emitJSON(resp, *output)
}

// parseLineRequest decodes the Clink line format.
func parseLineRequest(raw string) client.CompleteReq {
	req := client.CompleteReq{Shell: "cmd", Trigger: "auto"}
	var hist []string
	for i, line := range strings.Split(raw, "\n") {
		if i == 0 {
			continue // magic line
		}
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key, val := line[:eq], line[eq+1:]
		switch key {
		case "input":
			req.Input = unstuff(val)
		case "shell":
			req.Shell = unstuff(val)
		case "cwd":
			req.CWD = unstuff(val)
		case "trigger":
			req.Trigger = unstuff(val)
		case "history":
			// history 值保持 stuffed 原样交给 unstuffSplit（分隔符 \x1f 与
			// 内容转义 \x1e\x1f 必须区分对待，不能先整体 unstuff）。
			for _, h := range unstuffSplit(val) {
				if h != "" {
					hist = append(hist, h)
				}
			}
		}
	}
	req.History = hist
	return req
}

// unstuffSplit 解码并用分隔符拆分 history 值。
// 编码规则（与 adapters/clink/cmdpilot.lua 一致）：\x1e 后的字节为转义内容；
// 裸 \x1f 是条目分隔符；条目内容内的 \x1f 会被 stuff 成 \x1e\x1f（成对出现）。
// 因此必须"扫描式"解码：\x1e 吞掉下一字节，裸 \x1f 切分——先 split 再 unstuff 会把
// 内容中的 \x1f 误切（\x1e\x1f 内含裸 \x1f）。
func unstuffSplit(s string) []string {
	var items []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\x1e' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		if c == '\x1f' {
			items = append(items, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	items = append(items, b.String())
	return items
}

// stuff encodes a value: \x1e -> \x1e\x1e, \x1f -> \x1e\x1f.
func stuff(s string) string {
	s = strings.ReplaceAll(s, "\x1e", "\x1e\x1e")
	return strings.ReplaceAll(s, "\x1f", "\x1e\x1f")
}

// unstuff decodes a value produced by stuff.
func unstuff(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1e' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
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

// emitPlain writes the Clink line format.
func emitPlain(resp *completion.Response, output string) {
	var b strings.Builder
	b.WriteString(respMagic)
	b.WriteByte('\n')
	if resp.Top != nil {
		b.WriteString(stuff(resp.Top.Full))
		b.WriteString(fieldSep)
		b.WriteString(stuff(resp.Top.Text))
		b.WriteString(fieldSep)
		b.WriteString(stuff(resp.Top.Source))
		b.WriteString(fieldSep)
		b.WriteString(stuff(resp.Top.Kind))
	}
	b.WriteByte('\n')
	for _, s := range resp.List {
		b.WriteString(stuff(s.Full))
		b.WriteString(fieldSep)
		b.WriteString(stuff(s.Kind))
		b.WriteString(fieldSep)
		b.WriteString(stuff(s.Source))
		b.WriteByte('\n')
	}
	if output == "" {
		fmt.Fprint(os.Stdout, b.String())
		return
	}
	_ = os.WriteFile(output, []byte(b.String()), 0o644)
}

// emitError writes an error payload (file or stdout).
func emitError(output, msg string) {
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

// reportBatchUsage records many executed commands (one per line) at once.
func reportBatchUsage(path string, ensure bool) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
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
	cc.HTTP.Timeout = 3 * time.Second
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 3)
		cmd, dir, sh := parts[0], "", "cmd"
		if len(parts) > 1 {
			dir = unstuff(parts[1])
		}
		if len(parts) > 2 {
			sh = unstuff(parts[2])
		}
		_ = cc.ReportUsage(cmd, dir, sh)
	}
	return 0
}

// split splits on the unit separator used by the Lua adapter.
func split(s string) []string {
	return strings.Split(s, "\x1f")
}
