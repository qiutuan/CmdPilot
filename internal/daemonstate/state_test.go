package daemonstate

import (
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	if err := Write(State{PID: 42, Port: 1234, Token: "abc", StartedAt: "now"}); err != nil {
		t.Fatal(err)
	}
	s, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.PID != 42 || s.Port != 1234 || s.Token != "abc" {
		t.Errorf("state = %+v", s)
	}
}

func TestReadMissing(t *testing.T) {
	Remove()
	s, err := Read()
	if err != nil || s != nil {
		t.Errorf("missing file: s=%v err=%v", s, err)
	}
}

func TestReadCorrupt(t *testing.T) {
	Remove()
	if err := Write(State{PID: 1, Port: 1, Token: "x"}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the file on disk.
	if err := writeRaw(`{not json`); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(); err == nil {
		t.Error("corrupt file should error")
	}
	Remove()
}

func writeRaw(s string) error {
	return writeFile(Path(), []byte(s))
}

func TestWaitHealthy(t *testing.T) {
	calls := 0
	check := func() bool { calls++; return calls >= 3 }
	if !WaitHealthy(check, 2*time.Second) {
		t.Error("should become healthy")
	}
	if !WaitHealthy(func() bool { return true }, time.Second) {
		t.Error("immediately healthy failed")
	}
	if WaitHealthy(func() bool { return false }, 250*time.Millisecond) {
		t.Error("never-healthy should time out")
	}
}
