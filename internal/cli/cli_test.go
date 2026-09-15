package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/db"
)

// setBaseDir redirects the CLI data directory for the duration of a test.
func setBaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := config.BaseDirOverrideForTest(dir)
	t.Cleanup(func() { config.BaseDirOverrideForTest(old) })
	return dir
}

// run captures CLI stdout+stderr for a command.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldErr := Stdout, Stderr
	Stdout, Stderr = &buf, &buf
	defer func() { Stdout, Stderr = oldOut, oldErr }()
	code := Run(args)
	return buf.String(), code
}

func TestConfigSetGetShow(t *testing.T) {
	setBaseDir(t)
	if out, code := run(t, "config", "set", "engine", "local"); code != 0 {
		t.Fatalf("set: code=%d out=%s", code, out)
	}
	if out, code := run(t, "config", "get", "engine"); code != 0 || strings.TrimSpace(out) != "local" {
		t.Fatalf("get: code=%d out=%q", code, out)
	}
	if out, code := run(t, "config", "get", "ai.timeout_ms"); code != 0 || strings.TrimSpace(out) != "5000" {
		t.Fatalf("get timeout: code=%d out=%q", code, out)
	}
	if out, _ := run(t, "config", "show"); !strings.Contains(out, `"engine": "local"`) {
		t.Errorf("show missing engine: %s", out)
	}
	// invalid value rejected
	if _, code := run(t, "config", "set", "engine", "nonsense"); code == 0 {
		t.Error("invalid engine accepted")
	}
}

func TestFavoriteLifecycleAndExportImport(t *testing.T) {
	setBaseDir(t)
	// add
	if out, code := run(t, "favorite", "add", "--name", "deploy", "--command", "kubectl rollout restart deploy {{app}}", "--tags", "k8s", "--shell", "ps"); code != 0 {
		t.Fatalf("add: %s", out)
	}
	if out, _ := run(t, "favorite", "list"); !strings.Contains(out, "deploy") {
		t.Fatalf("list missing favorite: %s", out)
	}
	// search
	if out, code := run(t, "favorite", "search", "rollout"); code != 0 || !strings.Contains(out, "deploy") {
		t.Fatalf("search: %s", out)
	}
	// export
	exp := filepath.Join(t.TempDir(), "favs.json")
	if out, code := run(t, "favorite", "export", exp); code != 0 {
		t.Fatalf("export: %s", out)
	}
	raw, err := os.ReadFile(exp)
	if err != nil {
		t.Fatal(err)
	}
	var doc favoriteJSON
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Favorites) != 1 || doc.Favorites[0].Name != "deploy" {
		t.Fatalf("export content: %v %s", doc, err)
	}
	// import into a second (empty) store with rename conflict strategy
	dir2 := t.TempDir()
	store2, err := db.Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	imported, skipped, renamed, err := importFavorites(store2, exp, "skip")
	if err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("import1: i=%d s=%d r=%d err=%v", imported, skipped, renamed, err)
	}
	// conflict → skip
	imported, skipped, renamed, err = importFavorites(store2, exp, "skip")
	if err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("import2: i=%d s=%d r=%d err=%v", imported, skipped, renamed, err)
	}
	// conflict → rename
	imported, skipped, renamed, err = importFavorites(store2, exp, "rename")
	if err != nil || imported != 0 || renamed != 1 {
		t.Fatalf("import3: i=%d s=%d r=%d err=%v", imported, skipped, renamed, err)
	}
}

func TestCompleteStandaloneJSON(t *testing.T) {
	setBaseDir(t)
	out, code := run(t, "complete", "--input", "git st", "--shell", "ps", "--json")
	if code != 0 {
		t.Fatalf("complete: code=%d out=%s", code, out)
	}
	var resp struct {
		Top *struct {
			Full string `json:"full"`
		} `json:"top"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if resp.Top == nil || !strings.HasPrefix(resp.Top.Full, "git ") {
		t.Errorf("top = %+v", resp.Top)
	}
}

func TestStatsClearKeepsFavorites(t *testing.T) {
	setBaseDir(t)
	run(t, "favorite", "add", "--name", "keep", "--command", "git status")
	// record a usage directly via store
	store, err := db.Open(config.BaseDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsage(db.UsageRecord{Command: "git status", Dir: "/x", Shell: "ps"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if out, _ := run(t, "stats", "top", "5"); !strings.Contains(out, "git status") {
		t.Fatalf("stats top before clear: %s", out)
	}
	if out, code := run(t, "stats", "clear"); code != 0 {
		t.Fatalf("clear: %s", out)
	}
	if out, _ := run(t, "stats", "top", "5"); strings.Contains(out, "git status") {
		t.Errorf("stats not cleared: %s", out)
	}
	if out, _ := run(t, "favorite", "list"); !strings.Contains(out, "keep") {
		t.Errorf("favorite lost after clear: %s", out)
	}
	if out, code := run(t, "stats", "location"); code != 0 || !strings.Contains(out, "cmdpilot.db") {
		t.Errorf("location: %s", out)
	}
}

func TestCommandOverride(t *testing.T) {
	setBaseDir(t)
	if out, code := run(t, "command", "add", "--name", "mycmd", "--desc", "自定义命令", "--shell", "cmd"); code != 0 {
		t.Fatalf("add: %s", out)
	}
	if out, _ := run(t, "command", "list"); !strings.Contains(out, "mycmd") {
		t.Fatalf("list: %s", out)
	}
	if out, code := run(t, "command", "delete", "mycmd"); code != 0 {
		t.Fatalf("delete: %s", out)
	}
	if out, _ := run(t, "command", "list"); strings.Contains(out, "mycmd") {
		t.Errorf("still present: %s", out)
	}
}

func TestPrivacyAndSelfCheck(t *testing.T) {
	setBaseDir(t)
	if out, code := run(t, "privacy"); code != 0 || !strings.Contains(out, "本地") {
		t.Errorf("privacy: %s", out)
	}
	if out, code := run(t, "self-check"); code != 0 || !strings.Contains(out, "网络") {
		t.Errorf("self-check: code=%d %s", code, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	setBaseDir(t)
	if _, code := run(t, "nonsense-cmd"); code == 0 {
		t.Error("unknown command should fail")
	}
}

// --- 覆盖率补充测试 ---

func TestAITestNotConfiguredAndUsage(t *testing.T) {
	setBaseDir(t)
	if out, code := run(t, "ai", "test"); code != 1 || !strings.Contains(out, "未配置") {
		t.Errorf("ai test (no cfg): code=%d out=%q", code, out)
	}
	if _, code := run(t, "ai"); code != 2 {
		t.Error("ai without subcommand should return 2")
	}
	// 配了 base_url/model 但没有 key 且无守护进程 → 守护进程不可用
	run(t, "config", "set", "ai.base_url", "http://127.0.0.1:1")
	run(t, "config", "set", "ai.model", "mock")
	if _, code := run(t, "ai", "test"); code == 0 {
		t.Error("ai test should fail without daemon")
	}
}

func TestFavoriteGetUpdateDeleteAndErrors(t *testing.T) {
	setBaseDir(t)
	if _, code := run(t, "favorite"); code != 2 {
		t.Error("favorite bare should return 2")
	}
	if _, code := run(t, "favorite", "add"); code != 2 {
		t.Error("favorite add without flags should return 2")
	}
	run(t, "favorite", "add", "--name", "alias", "--command", "git push", "--note", "n", "--tags", "git,ci")
	if out, code := run(t, "favorite", "list"); code != 0 || !strings.Contains(out, "alias") {
		t.Fatalf("add+list: code=%d out=%q", code, out)
	}
	if out, code := run(t, "favorite", "get", "1"); code != 0 || !strings.Contains(out, "git push") {
		t.Errorf("get: code=%d out=%q", code, out)
	}
	if _, code := run(t, "favorite", "get", "99"); code != 1 {
		t.Error("get missing should return 1")
	}
	if _, code := run(t, "favorite", "get", "abc"); code != 1 {
		t.Error("get invalid id should return 1")
	}
	if out, code := run(t, "favorite", "update", "--id", "1", "--command", "git push --force"); code != 0 {
		t.Errorf("update: code=%d out=%q", code, out)
	}
	if out, code := run(t, "favorite", "list"); code != 0 || !strings.Contains(out, "git push --force") {
		t.Errorf("list after update: code=%d out=%q", code, out)
	}
	if out, code := run(t, "favorite", "delete", "1"); code != 0 {
		t.Errorf("delete: code=%d out=%q", code, out)
	}
	if out, _ := run(t, "favorite", "list"); strings.Contains(out, "alias") {
		t.Errorf("delete failed: %s", out)
	}
	// CSV 导出
	exp := filepath.Join(t.TempDir(), "favs.csv")
	run(t, "favorite", "add", "--name", "c1", "--command", "dir /s")
	if out, code := run(t, "favorite", "export", exp); code != 0 || !strings.Contains(out, "已导出 1 条") {
		t.Errorf("csv export: code=%d out=%q", code, out)
	}
}

func TestStatsTrendDistributionAndErrors(t *testing.T) {
	setBaseDir(t)
	store, err := db.Open(config.BaseDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := store.RecordUsage(db.UsageRecord{Command: "git log", Dir: "/repo", Shell: "cmd", At: time.Now().Add(-time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	if out, code := run(t, "stats", "trend", "7"); code != 0 || !strings.Contains(out, "5 次") {
		t.Errorf("trend: code=%d out=%q", code, out)
	}
	if out, code := run(t, "stats", "distribution"); code != 0 || !strings.Contains(out, "总计 5 次") || !strings.Contains(out, "cmd") {
		t.Errorf("distribution: code=%d out=%q", code, out)
	}
	if _, code := run(t, "stats", "bogus"); code != 2 {
		t.Error("unknown stats subcommand should return 2")
	}
	if _, code := run(t, "stats"); code != 2 {
		t.Error("bare stats should return 2")
	}
}

func TestCompleteWithHistoryAndFlagErrors(t *testing.T) {
	setBaseDir(t)
	out, code := run(t, "complete", "--input", "git st", "--shell", "ps", "--history", "git add .|git status", "--json")
	if code != 0 || !strings.Contains(out, `"top"`) {
		t.Errorf("complete with history: code=%d out=%s", code, out)
	}
	if _, code := run(t, "complete", "--badflag"); code != 2 {
		t.Error("bad flag should return 2")
	}
	if out, code := run(t, "recommend", "--json"); code != 0 {
		t.Errorf("recommend: code=%d out=%s", code, out)
	}
	if _, code := run(t, "recommend", "--badflag"); code != 2 {
		t.Error("bad flag should return 2")
	}
}

func TestVersionHelpAndHistoryCmd(t *testing.T) {
	setBaseDir(t)
	if out, code := run(t, "version"); code != 0 || !strings.Contains(out, "CmdPilot") {
		t.Errorf("version: code=%d out=%q", code, out)
	}
	if _, code := run(t, "help"); code != 0 {
		t.Error("help should return 0")
	}
	if out, code := run(t, "history", "5"); code != 0 {
		t.Errorf("history: code=%d out=%q", code, out)
	}
	if out, code := run(t, "history", "bad", "x"); code != 0 {
		t.Errorf("history badarg: code=%d out=%q", code, out)
	}
}

func TestConfigCorruptedRecovery(t *testing.T) {
	dir := setBaseDir(t)
	cfgDir := dir
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("{broken json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, "config", "show"); code != 0 {
		t.Errorf("corrupted config should fall back: code=%d out=%s", code, out)
	}
	// 备份文件应存在
	matches, _ := filepath.Glob(filepath.Join(cfgDir, "config.json.bak-*"))
	if len(matches) == 0 {
		t.Error("backup file not created")
	}
}

func TestCommandOverrideErrors(t *testing.T) {
	setBaseDir(t)
	if _, code := run(t, "command"); code != 2 {
		t.Error("bare command should return 2")
	}
	if _, code := run(t, "command", "add"); code != 2 {
		t.Error("command add without flags should return 2")
	}
}
