package db

import (
	"path/filepath"
	"testing"
	"time"
)

// openTemp creates a DB in a temp dir.
func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestOpenMigrate verifies a fresh DB is created with WAL and version 1.
func TestOpenMigrate(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	// WAL sidecar files appear once a connection writes; verify mode pragma.
	var mode string
	if err := d.sql.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var v int
	if err := d.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("user_version = %d, want 1", v)
	}
	if err := ensureExists(dir); err != nil {
		t.Errorf("db file not created: %v", err)
	}
}

// TestReopenKeepsData verifies data survives close/reopen (stability baseline).
func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFavorite(Favorite{Name: "dep", Command: "docker compose down", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	favs, err := d2.ListFavorites()
	if err != nil || len(favs) != 1 || favs[0].Name != "dep" {
		t.Fatalf("favorite lost after reopen: %v %v", favs, err)
	}
}

// TestRecordUsageAndTopN verifies counters increment and top-N works.
func TestRecordUsageAndTopN(t *testing.T) {
	d := openTemp(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = d.RecordUsage(UsageRecord{Command: "git commit", Dir: "/proj", Shell: "ps", At: now})
	}
	_ = d.RecordUsage(UsageRecord{Command: "git status", Dir: "/proj", Shell: "ps", At: now})
	_ = d.RecordUsage(UsageRecord{Command: "git status", Dir: "/other", Shell: "cmd", At: now})

	top, err := d.TopN(5, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) < 2 {
		t.Fatalf("TopN too small: %d", len(top))
	}
	if top[0].Command != "git commit" || top[0].Count != 5 {
		t.Errorf("top[0] = %+v", top[0])
	}
	byDir, err := d.TopNByDir("/proj", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(byDir) != 2 {
		t.Errorf("ByDir returned %d rows, want 2", len(byDir))
	}
}

// TestRecentCommandsDedupe verifies chain-recommendation history is distinct + newest-first.
func TestRecentCommandsDedupe(t *testing.T) {
	d := openTemp(t)
	now := time.Now()
	_ = d.RecordUsage(UsageRecord{Command: "git add", Dir: "/p", Shell: "ps", At: now})
	_ = d.RecordUsage(UsageRecord{Command: "git commit", Dir: "/p", Shell: "ps", At: now.Add(time.Second)})
	_ = d.RecordUsage(UsageRecord{Command: "git add", Dir: "/p", Shell: "ps", At: now.Add(2 * time.Second)})
	_ = d.RecordUsage(UsageRecord{Command: "git push", Dir: "/q", Shell: "ps", At: now.Add(3 * time.Second)})

	rec, err := d.RecentCommands(5, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 2 || rec[0] != "git add" || rec[1] != "git commit" {
		t.Errorf("recent(/p) = %v", rec)
	}
	recAll, _ := d.RecentCommands(5, "")
	if len(recAll) != 3 {
		t.Errorf("recent(all) = %v", recAll)
	}
}

// TestFavoritesCRUD covers add/list/get/update/delete/search.
func TestFavoritesCRUD(t *testing.T) {
	d := openTemp(t)
	id, err := d.AddFavorite(Favorite{Name: "deploy", Command: "git push origin main", Note: "发布", Tags: "git", Shell: "both", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// duplicate name must fail
	if _, err := d.AddFavorite(Favorite{Name: "deploy", Command: "x"}); err == nil {
		t.Error("duplicate favorite name should fail")
	}
	f, err := d.GetFavorite(id)
	if err != nil || f == nil || f.Command != "git push origin main" {
		t.Fatalf("GetFavorite: %v %v", f, err)
	}
	f.Command = "git push --force-with-lease origin main"
	if err := d.UpdateFavorite(*f); err != nil {
		t.Fatal(err)
	}
	found, _ := d.SearchFavorites("force")
	if len(found) != 1 {
		t.Errorf("search 'force' = %d", len(found))
	}
	none, _ := d.SearchFavorites("zzz")
	if len(none) != 0 {
		t.Errorf("search 'zzz' = %d", len(none))
	}
	if err := d.DeleteFavorite(id); err != nil {
		t.Fatal(err)
	}
	gone, _ := d.GetFavorite(id)
	if gone != nil {
		t.Error("favorite still exists after delete")
	}
}

// TestUserCommandOverride verifies upsert/list/delete of overrides.
func TestUserCommandOverride(t *testing.T) {
	d := openTemp(t)
	c := UserCommand{Name: "mydeploy", Params: []string{"--force"}, Desc: "自定义部署", Examples: []string{"mydeploy --force"}, Shell: "external", Tags: []string{"custom"}}
	if err := d.SetUserCommand(c); err != nil {
		t.Fatal(err)
	}
	c.Desc = "更新后的描述"
	if err := d.SetUserCommand(c); err != nil { // upsert
		t.Fatal(err)
	}
	all, err := d.UserCommands()
	if err != nil || len(all) != 1 {
		t.Fatalf("UserCommands: %v %v", all, err)
	}
	if all[0].Desc != "更新后的描述" || len(all[0].Params) != 1 {
		t.Errorf("override not updated: %+v", all[0])
	}
	if err := d.DeleteUserCommand("mydeploy"); err != nil {
		t.Fatal(err)
	}
	all, _ = d.UserCommands()
	if len(all) != 0 {
		t.Errorf("override not deleted: %v", all)
	}
}

// TestClearStatsKeepsFavorites verifies clearing stats preserves user data.
func TestClearStatsKeepsFavorites(t *testing.T) {
	d := openTemp(t)
	_ = d.RecordUsage(UsageRecord{Command: "ls", Dir: "/", Shell: "cmd", At: time.Now()})
	_, _ = d.AddFavorite(Favorite{Name: "k", Command: "kubectl get pods", CreatedAt: time.Now()})
	if err := d.ClearStats(); err != nil {
		t.Fatal(err)
	}
	top, _ := d.TopN(10, time.Time{})
	if len(top) != 0 {
		t.Errorf("stats not cleared: %v", top)
	}
	favs, _ := d.ListFavorites()
	if len(favs) != 1 {
		t.Errorf("favorites lost on ClearStats: %v", favs)
	}
	byDir, byShell, total, err := d.StatsDistribution()
	if err != nil || total != 0 {
		t.Errorf("distribution: %v %v %d %v", byDir, byShell, total, err)
	}
}

// TestIntegrity verifies integrity_check returns ok.
func TestIntegrity(t *testing.T) {
	d := openTemp(t)
	out, err := d.Integrity()
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Errorf("integrity_check = %q", out)
	}
}

// TestDBPath verifies the DB file path helper.
func TestDBPath(t *testing.T) {
	p := DBPath("/tmp/x")
	if filepath.Base(p) != "cmdpilot.db" {
		t.Errorf("DBPath = %q", p)
	}
}
