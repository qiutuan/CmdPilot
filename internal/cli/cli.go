// Package cli implements CmdPilot's standalone command-line interface.
// The core logic layer must remain usable without any terminal (M 核心 CLI),
// so every command works directly against SQLite / knowledge / config, and
// only optional commands (ai test via daemon) need a running daemon.
package cli

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qiutuan/CmdPilot/internal/client"
	"github.com/qiutuan/CmdPilot/internal/completion"
	"github.com/qiutuan/CmdPilot/internal/config"
	"github.com/qiutuan/CmdPilot/internal/daemonstate"
	"github.com/qiutuan/CmdPilot/internal/db"
	"github.com/qiutuan/CmdPilot/internal/knowledge"
	"github.com/qiutuan/CmdPilot/internal/logx"
	"github.com/qiutuan/CmdPilot/internal/version"
)

// Stdout is the CLI output writer (swappable for tests).
var Stdout io.Writer = os.Stdout

// Stderr is the CLI error writer.
var Stderr io.Writer = os.Stderr

// Run dispatches the subcommand and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-v", "--version":
		fmt.Fprintln(Stdout, version.String())
		return 0
	case "help", "-h", "--help":
		printUsage()
		return 0
	case "config":
		return cmdConfig(rest)
	case "ai":
		return cmdAI(rest)
	case "favorite":
		return cmdFavorite(rest)
	case "stats":
		return cmdStats(rest)
	case "command":
		return cmdCommand(rest)
	case "complete":
		return cmdComplete(rest)
	case "recommend":
		return cmdRecommend(rest)
	case "daemon":
		return cmdDaemon(rest)
	case "self-check":
		return cmdSelfCheck()
	case "history":
		return cmdHistory(rest)
	case "privacy":
		return cmdPrivacy(rest)
	}
	fmt.Fprintf(Stderr, "cmdpilot: unknown command %q (see 'cmdpilot help')\n", cmd)
	return 2
}

func printUsage() {
	fmt.Fprintln(Stdout, `CmdPilot - Windows 命令行智能补全助手 (本地+AI 双引擎)

用法:
  cmdpilot config get <path> | set <path> <value> | show
  cmdpilot ai test                        # 测试 OpenAI 兼容端点
  cmdpilot favorite list|add|get|update|delete|search|export|import
  cmdpilot stats top [n]|trend|distribution|clear|location
  cmdpilot command add|list|delete        # 用户命令覆盖
  cmdpilot complete --input "git st" [--shell ps|cmd] [--cwd DIR] [--json]
  cmdpilot recommend [--cwd DIR]
  cmdpilot history recent [n]
  cmdpilot daemon start|stop|status|ensure
  cmdpilot self-check
  cmdpilot privacy                        # 统计文件位置与清除说明
  cmdpilot version`)
}

// --- wiring helpers ---

// loadStore loads config (corruption-tolerant) and opens the SQLite store.
func loadStore() (*config.Config, *db.DB, bool, error) {
	cfg, corrupted, err := config.Load()
	if err != nil {
		return nil, nil, false, err
	}
	store, err := db.Open(config.BaseDir())
	if err != nil {
		return nil, nil, false, err
	}
	return cfg, store, corrupted, nil
}

// loadEngine builds a standalone engine (CLI does not require a daemon).
func loadEngine(cfg *config.Config, store *db.DB) (*completion.Engine, error) {
	kb, err := knowledge.Load()
	if err != nil {
		return nil, err
	}
	return completion.New(kb, store, cfg, nil, nil), nil
}

// printJSON writes machine-readable output.
func printJSON(v any) error {
	enc := json.NewEncoder(Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- config ---

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(Stderr, "用法: cmdpilot config get <path> | set <path> <value> | show")
		return 2
	}
	cfg, corrupted, err := config.Load()
	if err != nil {
		fmt.Fprintf(Stderr, "config: %v\n", err)
		return 1
	}
	if corrupted {
		fmt.Fprintln(Stderr, "警告: config.json 损坏，已备份并回退默认配置")
	}
	switch args[0] {
	case "show":
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Fprintln(Stdout, string(data))
		return 0
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(Stderr, "用法: cmdpilot config get <path>")
			return 2
		}
		v, err := cfg.Get(args[1])
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintln(Stdout, v)
		return 0
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(Stderr, "用法: cmdpilot config set <path> <value>")
			return 2
		}
		if err := cfg.Set(args[1], args[2]); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintln(Stdout, "已保存:", args[1])
		return 0
	}
	fmt.Fprintf(Stderr, "未知子命令 %q\n", args[0])
	return 2
}

// --- ai ---

func cmdAI(args []string) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(Stderr, "用法: cmdpilot ai test")
		return 2
	}
	cfg, corrupted, err := config.Load()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	if corrupted {
		fmt.Fprintln(Stderr, "警告: 配置损坏已回退默认")
	}
	if cfg.AI.BaseURL == "" || cfg.AI.Model == "" {
		fmt.Fprintln(Stderr, "AI 未配置: 请先设置 cmdpilot config set ai.base_url <url> / ai.model <model> / ai.api_key <key>")
		return 1
	}
	c := daemonClient()
	if c == nil {
		fmt.Fprintln(Stderr, "守护进程不可用，请先运行 cmdpilot daemon start")
		return 1
	}
	model, err := c.AITest()
	if err != nil {
		fmt.Fprintf(Stdout, "AI 连接失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(Stdout, "AI 连接成功, model: %s\n", model)
	return 0
}

// daemonClient builds a client from the state file, or nil.
func daemonClient() *client.Client {
	st, err := daemonstate.Read()
	if err != nil || st == nil || st.Port == 0 {
		return nil
	}
	return client.New(fmt.Sprintf("http://127.0.0.1:%d", st.Port), st.Token)
}

// --- favorites ---

// favoriteJSON is the shareable export format.
type favoriteJSON struct {
	Format    string        `json:"format"`
	Version   int           `json:"version"`
	Favorites []db.Favorite `json:"favorites"`
}

func cmdFavorite(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(Stderr, "用法: cmdpilot favorite list|add|get|update|delete|search|export|import")
		return 2
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	_ = cfg
	defer store.Close()
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		favs, err := store.ListFavorites()
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		for _, f := range favs {
			fmt.Fprintf(Stdout, "#%d  %s  [%s]  %s\n", f.ID, f.Name, f.Tags, f.Command)
		}
		fmt.Fprintf(Stdout, "共 %d 条收藏\n", len(favs))
		return 0
	case "add":
		// add --name X --command Y [--note N] [--tags T] [--shell both|cmd|ps]
		fs := flag.NewFlagSet("favorite add", flag.ContinueOnError)
		name := fs.String("name", "", "收藏名")
		command := fs.String("command", "", "命令内容（支持 {{param}} 占位符）")
		note := fs.String("note", "", "备注")
		tags := fs.String("tags", "", "标签（逗号分隔）")
		shell := fs.String("shell", "both", "适用终端 both|cmd|ps")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if *name == "" || *command == "" {
			fmt.Fprintln(Stderr, "需要 --name 与 --command")
			return 2
		}
		id, err := store.AddFavorite(db.Favorite{Name: *name, Command: *command, Note: *note, Tags: *tags, Shell: *shell, CreatedAt: time.Now()})
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已添加收藏 #%d\n", id)
		return 0
	case "get":
		if len(rest) < 1 {
			fmt.Fprintln(Stderr, "用法: cmdpilot favorite get <id>")
			return 2
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			fmt.Fprintf(Stderr, "无效 id: %v\n", err)
			return 1
		}
		f, err := store.GetFavorite(id)
		if err != nil || f == nil {
			fmt.Fprintf(Stderr, "未找到收藏 #%d\n", id)
			return 1
		}
		printJSON(f)
		return 0
	case "update":
		fs := flag.NewFlagSet("favorite update", flag.ContinueOnError)
		id := fs.Int64("id", 0, "收藏 id")
		name := fs.String("name", "", "新名称")
		command := fs.String("command", "", "新命令")
		note := fs.String("note", "", "新备注")
		tags := fs.String("tags", "", "新标签")
		shell := fs.String("shell", "", "新终端")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		f, err := store.GetFavorite(*id)
		if err != nil || f == nil {
			fmt.Fprintf(Stderr, "未找到收藏 #%d\n", *id)
			return 1
		}
		if *name != "" {
			f.Name = *name
		}
		if *command != "" {
			f.Command = *command
		}
		if *note != "" {
			f.Note = *note
		}
		if *tags != "" {
			f.Tags = *tags
		}
		if *shell != "" {
			f.Shell = *shell
		}
		if err := store.UpdateFavorite(*f); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已更新收藏 #%d\n", *id)
		return 0
	case "delete":
		if len(rest) < 1 {
			fmt.Fprintln(Stderr, "用法: cmdpilot favorite delete <id>")
			return 2
		}
		id, _ := strconv.ParseInt(rest[0], 10, 64)
		if err := store.DeleteFavorite(id); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已删除收藏 #%d\n", id)
		return 0
	case "search":
		q := strings.Join(rest, " ")
		if q == "" {
			fmt.Fprintln(Stderr, "用法: cmdpilot favorite search <关键词>")
			return 2
		}
		favs, err := store.SearchFavorites(q)
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		for _, f := range favs {
			fmt.Fprintf(Stdout, "#%d  %s  %s\n", f.ID, f.Name, f.Command)
		}
		return 0
	case "export":
		// export <file.json|file.csv> [--ids 1,2,3]
		if len(rest) < 1 {
			fmt.Fprintln(Stderr, "用法: cmdpilot favorite export <file.json|file.csv> [--ids 1,2]")
			return 2
		}
		file := rest[0]
		ids := map[int64]bool{}
		if len(rest) > 2 && rest[1] == "--ids" {
			for _, p := range strings.Split(rest[2], ",") {
				if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
					ids[n] = true
				}
			}
		}
		favs, err := store.ListFavorites()
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		var selected []db.Favorite
		for _, f := range favs {
			if len(ids) > 0 && !ids[f.ID] {
				continue
			}
			selected = append(selected, f)
		}
		if err := exportFavorites(file, selected); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已导出 %d 条收藏到 %s\n", len(selected), file)
		return 0
	case "import":
		// import <file.json|file.csv> [--strategy skip|overwrite|rename]
		if len(rest) < 1 {
			fmt.Fprintln(Stderr, "用法: cmdpilot favorite import <file> [--strategy skip|overwrite|rename]")
			return 2
		}
		strategy := "skip"
		if len(rest) > 2 && rest[1] == "--strategy" {
			strategy = rest[2]
		}
		imported, skipped, renamed, err := importFavorites(store, rest[0], strategy)
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "导入完成: 新增 %d, 跳过 %d, 重命名 %d\n", imported, skipped, renamed)
		return 0
	}
	fmt.Fprintf(Stderr, "未知子命令 %q\n", sub)
	return 2
}

// exportFavorites writes JSON or CSV based on extension.
func exportFavorites(path string, favs []db.Favorite) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w := csv.NewWriter(f)
		_ = w.Write([]string{"name", "command", "note", "tags", "shell"})
		for _, fav := range favs {
			_ = w.Write([]string{fav.Name, fav.Command, fav.Note, fav.Tags, fav.Shell})
		}
		w.Flush()
		return w.Error()
	default:
		data, err := json.MarshalIndent(favoriteJSON{Format: "cmdpilot-favorites", Version: 1, Favorites: favs}, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)
	}
}

// importFavorites loads a JSON/CSV file and merges with the chosen conflict strategy.
func importFavorites(store *db.DB, path, strategy string) (imported, skipped, renamed int, err error) {
	var favs []db.Favorite
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		f, err := os.Open(path)
		if err != nil {
			return 0, 0, 0, err
		}
		defer f.Close()
		rows, err := csv.NewReader(f).ReadAll()
		if err != nil {
			return 0, 0, 0, err
		}
		for i, row := range rows {
			if i == 0 {
				continue // header
			}
			if len(row) < 2 {
				continue
			}
			shell := "both"
			if len(row) > 4 {
				shell = row[4]
			}
			favs = append(favs, db.Favorite{Name: row[0], Command: row[1], Note: val(row, 2), Tags: val(row, 3), Shell: shell})
		}
	default:
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, 0, 0, err
		}
		var doc favoriteJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return 0, 0, 0, fmt.Errorf("无效的收藏文件: %w", err)
		}
		favs = doc.Favorites
	}
	for _, f := range favs {
		if f.Name == "" || f.Command == "" {
			continue
		}
		existing, err := store.SearchFavorites(f.Name)
		if err != nil {
			return imported, skipped, renamed, err
		}
		conflict := false
		for _, e := range existing {
			if e.Name == f.Name {
				conflict = true
				break
			}
		}
		if conflict {
			switch strategy {
			case "overwrite":
				for _, e := range existing {
					if e.Name == f.Name {
						f.ID = e.ID
						if err := store.UpdateFavorite(f); err != nil {
							return imported, skipped, renamed, err
						}
						imported++
					}
				}
			case "rename":
				f.Name = f.Name + "-import"
				if _, err := store.AddFavorite(f); err != nil {
					return imported, skipped, renamed, err
				}
				renamed++
			default: // skip
				skipped++
			}
			continue
		}
		if _, err := store.AddFavorite(f); err != nil {
			return imported, skipped, renamed, err
		}
		imported++
	}
	return imported, skipped, renamed, nil
}

func val(row []string, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

// --- stats ---

func cmdStats(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(Stderr, "用法: cmdpilot stats top [n] | trend | distribution | clear | location")
		return 2
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	_ = cfg
	defer store.Close()
	switch args[0] {
	case "top":
		n := 10
		if len(args) > 1 {
			if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
				n = v
			}
		}
		rows, err := store.TopN(n, time.Time{})
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		for i, r := range rows {
			fmt.Fprintf(Stdout, "%2d. %-40s %4d 次  最近 %s  目录: %s\n", i+1, r.Command, r.Count, r.LastUsed.Format("01-02 15:04"), r.Dirs)
		}
		return 0
	case "trend":
		days := 14
		if len(args) > 1 {
			if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
				days = v
			}
		}
		points, err := store.UsageTrend(days)
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		for _, p := range points {
			fmt.Fprintf(Stdout, "%s  %d 次\n", p.Day, p.Count)
		}
		return 0
	case "distribution":
		byDir, byShell, total, err := store.StatsDistribution()
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "总计 %d 次执行\n按目录:\n", total)
		for k, v := range byDir {
			fmt.Fprintf(Stdout, "  %-40s %d\n", k, v)
		}
		fmt.Fprintln(Stdout, "按终端:")
		for k, v := range byShell {
			fmt.Fprintf(Stdout, "  %-8s %d\n", k, v)
		}
		return 0
	case "clear":
		if err := store.ClearStats(); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintln(Stdout, "已清空统计（收藏保留）")
		return 0
	case "location":
		fmt.Fprintf(Stdout, "统计文件: %s\n", db.DBPath(config.BaseDir()))
		fmt.Fprintln(Stdout, "隐私说明: 所有统计完全本地存储，不联网；清除: cmdpilot stats clear")
		return 0
	}
	fmt.Fprintf(Stderr, "未知子命令 %q\n", args[0])
	return 2
}

// --- user command overrides ---

func cmdCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(Stderr, "用法: cmdpilot command add|list|delete")
		return 2
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	_ = cfg
	defer store.Close()
	switch args[0] {
	case "list":
		cmds, err := store.UserCommands()
		if err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		for _, c := range cmds {
			fmt.Fprintf(Stdout, "%-40s [%s] %s\n", c.Name, c.Shell, c.Desc)
		}
		return 0
	case "add":
		fs := flag.NewFlagSet("command add", flag.ContinueOnError)
		name := fs.String("name", "", "命令名（覆盖内置库同名条目）")
		desc := fs.String("desc", "", "描述")
		shell := fs.String("shell", "external", "cmd|ps|external")
		params := fs.String("params", "", "参数，逗号分隔")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *name == "" {
			fmt.Fprintln(Stderr, "需要 --name")
			return 2
		}
		var p []string
		for _, s := range strings.Split(*params, ",") {
			if t := strings.TrimSpace(s); t != "" {
				p = append(p, t)
			}
		}
		if err := store.SetUserCommand(db.UserCommand{Name: *name, Desc: *desc, Shell: *shell, Params: p}); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已保存用户命令 %q（覆盖内置库）\n", *name)
		return 0
	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(Stderr, "用法: cmdpilot command delete <name>")
			return 2
		}
		if err := store.DeleteUserCommand(args[1]); err != nil {
			fmt.Fprintf(Stderr, "%v\n", err)
			return 1
		}
		fmt.Fprintf(Stdout, "已删除用户命令 %q（恢复内置库条目）\n", args[1])
		return 0
	}
	fmt.Fprintf(Stderr, "未知子命令 %q\n", args[0])
	return 2
}

// --- complete / recommend (standalone, no daemon needed) ---

func cmdComplete(args []string) int {
	fs := flag.NewFlagSet("complete", flag.ContinueOnError)
	input := fs.String("input", "", "当前输入")
	shell := fs.String("shell", "ps", "ps|cmd")
	cwd := fs.String("cwd", "", "工作目录")
	history := fs.String("history", "", "最近历史，用 | 分隔")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	defer store.Close()
	eng, err := loadEngine(cfg, store)
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	var hist []string
	if *history != "" {
		hist = strings.Split(*history, "|")
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	resp, err := eng.Complete(completion.Request{Input: *input, Shell: *shell, CWD: *cwd, History: hist})
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	if *asJSON {
		return writeJSONOrFail(resp)
	}
	if resp.Top != nil {
		fmt.Fprintf(Stdout, "最佳: %s (来源: %s)\n", resp.Top.Full, resp.Top.Source)
	}
	for i, s := range resp.List {
		fmt.Fprintf(Stdout, "  %d. %-40s [%s/%s]\n", i+1, s.Full, s.Kind, s.Source)
	}
	return 0
}

func cmdRecommend(args []string) int {
	fs := flag.NewFlagSet("recommend", flag.ContinueOnError)
	cwd := fs.String("cwd", "", "工作目录")
	shell := fs.String("shell", "ps", "ps|cmd")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	defer store.Close()
	eng, err := loadEngine(cfg, store)
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	resp, err := eng.Complete(completion.Request{Input: "", Shell: *shell, CWD: *cwd})
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	if *asJSON {
		return writeJSONOrFail(resp.Recommendations)
	}
	for i, s := range resp.Recommendations {
		fmt.Fprintf(Stdout, "%d. %-40s %s\n", i+1, s.Full, s.Source)
	}
	return 0
}

func writeJSONOrFail(v any) int {
	if err := printJSON(v); err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	return 0
}

// --- daemon ---

func cmdDaemon(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(Stderr, "用法: cmdpilot daemon start|stop|status|ensure")
		return 2
	}
	switch args[0] {
	case "start":
		return daemonStart()
	case "stop":
		c := daemonClient()
		if c == nil {
			fmt.Fprintln(Stdout, "守护进程未在运行")
			daemonstate.Remove() // clean up any stale state file
			return 0
		}
		_ = c.Shutdown()
		// fall back to PID kill if graceful shutdown fails silently
		// （跨平台：Windows 走 TerminateProcess，类 Unix 走 SIGKILL）
		st, _ := daemonstate.Read()
		if st != nil && st.PID > 0 {
			if proc, err := os.FindProcess(st.PID); err == nil {
				_ = proc.Kill() //nolint:errcheck // best-effort
			}
		}
		daemonstate.Remove()
		fmt.Fprintln(Stdout, "守护进程已停止")
		return 0
	case "status":
		c := daemonClient()
		if c == nil {
			fmt.Fprintln(Stdout, "守护进程: 未运行")
			return 0
		}
		ok, ver := c.Health()
		st, _ := daemonstate.Read()
		if ok {
			fmt.Fprintf(Stdout, "守护进程: 运行中 pid=%d port=%d %s\n", st.PID, st.Port, ver)
			return 0
		}
		fmt.Fprintln(Stdout, "守护进程: 状态文件存在但进程无响应（可能已崩溃，运行 cmdpilot daemon ensure 恢复）")
		return 0
	case "ensure":
		c := daemonClient()
		if c != nil {
			if ok, _ := c.Health(); ok {
				fmt.Fprintln(Stdout, "守护进程已在运行")
				return 0
			}
			fmt.Fprintln(Stdout, "检测到失效状态文件，重新拉起...")
			daemonstate.Remove()
		}
		if err := startDaemonDetached(); err != nil {
			fmt.Fprintf(Stderr, "启动失败: %v\n", err)
			return 1
		}
		ok := daemonstate.WaitHealthy(func() bool {
			c := daemonClient()
			if c == nil {
				return false
			}
			ok, _ := c.Health()
			return ok
		}, 8*time.Second)
		if !ok {
			fmt.Fprintln(Stderr, "守护进程启动超时")
			return 1
		}
		fmt.Fprintln(Stdout, "守护进程已启动")
		return 0
	}
	fmt.Fprintf(Stderr, "未知子命令 %q\n", args[0])
	return 2
}

// daemonStart runs the daemon in the foreground (used by the detached child).
func daemonStart() int {
	cfg, corrupted, err := config.Load()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	if corrupted {
		fmt.Fprintln(Stderr, "警告: 配置损坏已回退默认")
	}
	kb, err := knowledge.Load()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	store, err := db.Open(config.BaseDir())
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	defer store.Close()
	var log *logx.Logger
	if cfg.LogEnabled {
		log, err = logx.New(filepath.Join(config.BaseDir(), "logs"), "cmdpilot")
		if err == nil {
			log.SetLevel(logx.ParseLevel(cfg.LogLevel))
			defer log.Close()
		}
	}
	srv := NewServer(store, kb, cfg, log)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	// Block until shutdown.
	select {}
}

// startDaemonDetached spawns `cmdpilot daemon start` without a window.
func startDaemonDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := startDetached(exe, "daemon", "start")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("无法启动守护进程: %w", err)
	}
	// Detach: release handles.
	_ = cmd.Process.Release()
	return nil
}

// --- self-check ---

func cmdSelfCheck() int {
	fmt.Fprintf(Stdout, "CmdPilot 自检报告\n")
	fmt.Fprintln(Stdout, "=================")
	fmt.Fprintf(Stdout, "版本: %s\n", version.String())
	fmt.Fprintf(Stdout, "平台: %s/%s\n", runtime.GOOS, runtime.GOARCH)

	cfg, corrupted, err := config.Load()
	if err != nil {
		fmt.Fprintf(Stdout, "配置: 读取失败 %v\n", err)
		return 1
	}
	if corrupted {
		fmt.Fprintln(Stdout, "配置: 已从损坏备份恢复默认")
	} else {
		fmt.Fprintln(Stdout, "配置: 正常")
	}
	fmt.Fprintf(Stdout, "引擎模式: %s / 触发模式: %s\n", cfg.Effective(), cfg.Trigger)
	if cfg.AI.BaseURL != "" {
		fmt.Fprintf(Stdout, "AI 端点: %s (model=%s)\n", cfg.AI.BaseURL, cfg.AI.Model)
	} else {
		fmt.Fprintln(Stdout, "AI 端点: 未配置（本地模式可用）")
	}

	kb, err := knowledge.Load()
	if err != nil {
		fmt.Fprintf(Stdout, "知识库: 加载失败 %v\n", err)
		return 1
	}
	fmt.Fprintf(Stdout, "知识库: %d 条命令 (cmd=%d ps=%d external=%d)\n", kb.Count(),
		len(kb.ByShell(knowledge.ShellCmd)), len(kb.ByShell(knowledge.ShellPS)), len(kb.ByShell(knowledge.ShellExternal)))

	store, err := db.Open(config.BaseDir())
	if err != nil {
		fmt.Fprintf(Stdout, "数据库: 打开失败 %v\n", err)
		return 1
	}
	defer store.Close()
	integrity, err := store.Integrity()
	if err != nil {
		fmt.Fprintf(Stdout, "数据库: 完整性检查失败 %v\n", err)
		return 1
	}
	fmt.Fprintf(Stdout, "数据库: 完整性 %s, 位置 %s\n", integrity, db.DBPath(config.BaseDir()))

	c := daemonClient()
	if c != nil {
		if ok, ver := c.Health(); ok {
			fmt.Fprintf(Stdout, "守护进程: 运行中 %s\n", ver)
		} else {
			fmt.Fprintln(Stdout, "守护进程: 未响应（可用 cmdpilot daemon ensure 恢复）")
		}
	} else {
		fmt.Fprintln(Stdout, "守护进程: 未运行")
	}

	fmt.Fprintln(Stdout, "网络: 本工具发起的所有网络请求仅为用户配置的 AI base_url: "+displayURL(cfg.AI.BaseURL))
	return 0
}

func displayURL(u string) string {
	if u == "" {
		return "(未配置，无任何网络请求)"
	}
	return u
}

// --- history ---

func cmdHistory(args []string) int {
	n := 10
	if len(args) > 1 && args[0] == "recent" {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			n = v
		}
	}
	cfg, store, _, err := loadStore()
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	_ = cfg
	defer store.Close()
	rec, err := store.RecentCommands(n, "")
	if err != nil {
		fmt.Fprintf(Stderr, "%v\n", err)
		return 1
	}
	for i, r := range rec {
		fmt.Fprintf(Stdout, "%2d. %s\n", i+1, r)
	}
	return 0
}

// --- privacy ---

func cmdPrivacy(args []string) int {
	fmt.Fprintf(Stdout, "CmdPilot 隐私说明\n")
	fmt.Fprintln(Stdout, "==================")
	fmt.Fprintf(Stdout, "统计数据库: %s\n", db.DBPath(config.BaseDir()))
	fmt.Fprintf(Stdout, "配置文件:   %s\n", config.Path())
	fmt.Fprintf(Stdout, "日志目录:   %s\n", filepath.Join(config.BaseDir(), "logs"))
	fmt.Fprintln(Stdout, "所有统计/历史完全本地存储，工具本身无任何网络遥测。")
	fmt.Fprintln(Stdout, "唯一可能的网络请求目标为用户配置的 AI base_url（用于 AI 补全）。")
	fmt.Fprintln(Stdout, "一键清除统计: cmdpilot stats clear（收藏不受影响）")
	fmt.Fprintln(Stdout, "完全清除所有数据: 删除上述目录后重新安装。")
	return 0
}

// sortStrings is a tiny helper used for stable listing.
func sortStrings(s []string) {
	sort.Strings(s)
}
