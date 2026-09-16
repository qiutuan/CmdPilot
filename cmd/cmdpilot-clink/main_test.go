package main

import (
	"strings"
	"testing"

	"github.com/qiutuan/CmdPilot/internal/client"
)

func TestStuffRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"git checkout main",
		"a\x1eb",
		"a\x1fb",
		"\x1e\x1f\x1e",
		"含中文与\n换行\x1e混合",
	}
	for _, c := range cases {
		if got := unstuff(stuff(c)); got != c {
			t.Fatalf("round-trip mismatch: input=%q got=%q", c, got)
		}
	}
}

func TestUnstuffTrailingSeparator(t *testing.T) {
	// 末尾孤立 \x1e（无后续字节）应原样保留，不 panic。
	if got := unstuff("abc\x1e"); got != "abc\x1e" {
		t.Fatalf("trailing sep: got %q", got)
	}
}

func TestParseLineRequest(t *testing.T) {
	raw := strings.Join([]string{
		"cmdpilot-req-v1",
		"input=" + stuff("git st"),
		"shell=cmd",
		"cwd=" + stuff("C:\\work\\repo"),
		"trigger=auto",
		"history=" + stuff("git add .") + historySep + stuff("git status"),
		"unknown=ignored",
		"",
	}, "\n")
	req := parseLineRequest(raw)
	if req.Input != "git st" {
		t.Fatalf("input: got %q", req.Input)
	}
	if req.Shell != "cmd" || req.Trigger != "auto" || req.CWD != "C:\\work\\repo" {
		t.Fatalf("fields mismatch: %+v", req)
	}
	if len(req.History) != 2 || req.History[0] != "git add ." || req.History[1] != "git status" {
		t.Fatalf("history: got %#v", req.History)
	}
}

func TestParseLineRequestDefaults(t *testing.T) {
	req := parseLineRequest("cmdpilot-req-v1\ninput=dir\n")
	if req.Shell != "cmd" || req.Trigger != "auto" {
		t.Fatalf("defaults: %+v", req)
	}
	// CRLF 与缺失字段均需容忍
	req = parseLineRequest("cmdpilot-req-v1\r\ninput=x\r\n")
	if req.Input != "x" {
		t.Fatalf("crlf input: got %q", req.Input)
	}
}

func TestIsLineRequest(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"lf", "cmdpilot-req-v1\ninput=x\n", true},
		{"crlf", "cmdpilot-req-v1\r\ninput=x\r\n", true},
		{"magic-only", "cmdpilot-req-v1", true},
		{"json", "{\"input\":\"x\"}", false},
		{"json-pretty", "{\r\n  \"input\": \"x\"\r\n}", false},
		{"magic-with-suffix", "cmdpilot-req-v1x\ninput=x\n", false},
	}
	for _, c := range cases {
		if got := isLineRequest(c.raw); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
		if c.want {
			// 判定为行模式后，解析必须真的拿到字段（CRLF 时曾因入口判定失败而走不到这里）
			if req := parseLineRequest(c.raw); strings.HasPrefix(c.raw, reqMagic) && req.Input != "x" && c.name != "magic-only" {
				t.Fatalf("%s: parse lost input: %+v", c.name, req)
			}
		}
	}
}

func TestParseLineRequestHistoryStuffedSep(t *testing.T) {
	// history 值内本身含 \x1f（经 stuffing）不应被误拆。
	h := stuff("echo a\x1fb")
	raw := "cmdpilot-req-v1\nhistory=" + h + "\n"
	req := parseLineRequest(raw)
	if len(req.History) != 1 || req.History[0] != "echo a\x1fb" {
		t.Fatalf("stuffed history: %#v", req.History)
	}
}

var _ = client.CompleteReq{} // 确保依赖导入（防止误删包引用）
