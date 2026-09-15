package completion

import (
	"path/filepath"
	"strings"

	"github.com/qiutuan/CmdPilot/internal/knowledge"
	"github.com/qiutuan/CmdPilot/internal/match"
)

// localCandidates builds the full local candidate pool for the request.
func (e *Engine) localCandidates(req Request, ctx ContextInfo, ui *usageIndex) []cand {
	switch ctx.State {
	case "path":
		return e.pathCandidates(req)
	case "param":
		return e.paramCandidates(req, ui)
	case "subcommand":
		return e.subcommandCandidates(req, ui)
	default:
		return e.commandCandidates(req, ui)
	}
}

// mergedCommands returns knowledge commands with user overrides applied.
func (e *Engine) mergedCommands() map[string]knowledge.Command {
	m := map[string]knowledge.Command{}
	if e.KB != nil {
		for _, c := range e.KB.Commands {
			m[c.Name] = c
		}
	}
	if e.Store != nil {
		if ucs, err := e.Store.UserCommands(); err == nil {
			for _, uc := range ucs {
				m[uc.Name] = knowledge.Command{
					Name: uc.Name, Params: uc.Params, Desc: uc.Desc,
					Examples: uc.Examples, Shell: uc.Shell, Tags: uc.Tags,
				}
			}
		}
	}
	return m
}

// commandCandidates covers the "typing a command name" state.
func (e *Engine) commandCandidates(req Request, ui *usageIndex) []cand {
	var out []cand
	input := strings.TrimSpace(req.Input)
	cmds := e.mergedCommands()
	parent := firstToken(input)
	var matched []string
	for name := range cmds {
		if !shellRelevant(cmds[name], req.Shell) {
			continue
		}
		if match.Match(input, name).Tier != match.TierNone {
			matched = append(matched, name)
		}
	}
	weights := e.chainWeights(ui, parent, matched)
	for _, name := range matched {
		mr := match.Match(input, name)
		rs := e.rankScore(ui, name, req.CWD, false) * weights[name]
		out = append(out, e.cmdToCand(name, input, mr, rs, "local"))
	}
	// favorites: name and command content both participate.
	if e.Store != nil {
		if favs, err := e.Store.ListFavorites(); err == nil {
			for _, f := range favs {
				mr := match.Match(input, f.Name)
				if mr.Tier == match.TierNone {
					mr = match.Match(input, f.Command)
				}
				if mr.Tier == match.TierNone {
					continue
				}
				rs := e.rankScore(ui, f.Command, req.CWD, false)
				if rs < 2.0 {
					rs = 2.0 // favorites weigh above plain history
				}
				out = append(out, cand{
					text: suffixOrName(input, f.Name), kind: "favorite", source: "favorite",
					display: f.Command, isSuffix: isPrefixMatch(input, f.Name), mr: mr, rankScore: rs,
				})
			}
		}
	}
	// history lines as full-line candidates.
	out = append(out, e.historyCandidates(req, ui)...)
	return out
}

// subcommandCandidates covers "git <partial>" state.
func (e *Engine) subcommandCandidates(req Request, ui *usageIndex) []cand {
	fields := strings.Fields(req.Input)
	if len(fields) == 0 {
		return nil
	}
	parent := fields[0]
	current := ""
	if len(fields) > 1 {
		current = fields[len(fields)-1]
	}
	var out []cand
	cmds := e.mergedCommands()
	var matched []string
	for name, c := range cmds {
		if !strings.HasPrefix(name, parent+" ") {
			continue
		}
		if !shellRelevant(c, req.Shell) {
			continue
		}
		child := name[len(parent)+1:]
		if match.Match(current, child).Tier != match.TierNone {
			matched = append(matched, name)
		}
	}
	weights := e.chainWeights(ui, parent, matched)
	for _, name := range matched {
		child := name[len(parent)+1:]
		mr := match.Match(current, child)
		rs := e.rankScore(ui, name, req.CWD, false) * weights[name]
		out = append(out, cand{
			text: suffixOrName(current, child), kind: "subcommand", source: "local",
			display: name, isSuffix: isPrefixMatch(current, child), mr: mr, rankScore: rs,
		})
	}
	// favorite commands whose first token matches parent.
	if e.Store != nil {
		if favs, err := e.Store.ListFavorites(); err == nil {
			for _, f := range favs {
				ff := strings.Fields(f.Command)
				if len(ff) == 0 || ff[0] != parent {
					continue
				}
				child := strings.TrimSpace(strings.TrimPrefix(f.Command, parent))
				mr := match.Match(current, child)
				if mr.Tier == match.TierNone {
					continue
				}
				rs := e.rankScore(ui, f.Command, req.CWD, false)
				if rs < 2.0 {
					rs = 2.0
				}
				out = append(out, cand{
					text: suffixOrName(current, child), kind: "favorite", source: "favorite",
					display: f.Command, isSuffix: isPrefixMatch(current, child), mr: mr, rankScore: rs,
				})
			}
		}
	}
	out = append(out, e.historyCandidates(req, ui)...)
	return out
}

// paramCandidates covers "-<flag>" and argument position after a known command.
func (e *Engine) paramCandidates(req Request, ui *usageIndex) []cand {
	fields := strings.Fields(req.Input)
	if len(fields) == 0 {
		return nil
	}
	current := fields[len(fields)-1]
	// Locate the command entry: first token, or first+subcommand.
	cmdName := fields[0]
	if len(fields) >= 2 {
		if c := e.mergedCommands()[fields[0]+" "+fields[1]]; c.Name != "" {
			cmdName = c.Name
		}
	}
	var out []cand
	c := e.mergedCommands()[cmdName]
	if c.Name != "" {
		for _, p := range c.Params {
			if !strings.HasPrefix(p, "-") && !strings.HasPrefix(p, "/") {
				continue
			}
			mr := match.Match(current, p)
			if mr.Tier == match.TierNone {
				continue
			}
			full := replaceLastToken(req.Input, p)
			out = append(out, cand{
				text: suffixOrName(current, p), kind: "param", source: "local",
				display: full, isSuffix: isPrefixMatch(current, p), mr: mr,
			})
		}
	}
	// Path completion for non-flag tokens is always useful.
	if !strings.HasPrefix(current, "-") {
		out = append(out, e.pathCandidates(req)...)
	}
	return out
}

// pathCandidates completes a path-like token against the filesystem.
func (e *Engine) pathCandidates(req Request) []cand {
	if e.FS == nil {
		return nil
	}
	fields := strings.Fields(req.Input)
	current := ""
	if len(fields) > 0 {
		current = fields[len(fields)-1]
	}
	dir, prefix := splitPath(req.CWD, current)
	entries, err := e.FS.List(dir)
	if err != nil {
		return nil
	}
	// dirPart is the "src/" part of the current token, used to rebuild the line.
	dirPart := ""
	if strings.HasSuffix(current, string(filepath.Separator)) || strings.HasSuffix(current, "/") || strings.HasSuffix(current, `\`) {
		dirPart = current
	} else if i := lastSepIndex(current); i >= 0 {
		dirPart = current[:i+1]
	}
	var out []cand
	for _, ent := range entries {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(ent), strings.ToLower(prefix)) {
			continue
		}
		fullPath := filepath.Join(dir, strings.TrimSuffix(ent, string(filepath.Separator)))
		if e.FS.IsDir(fullPath) && !strings.HasSuffix(ent, string(filepath.Separator)) {
			ent += string(filepath.Separator)
		}
		mr := match.Match(prefix, strings.TrimSuffix(ent, string(filepath.Separator)))
		if mr.Tier == match.TierNone {
			mr = match.Match(strings.ToLower(prefix), strings.ToLower(ent))
		}
		out = append(out, cand{
			text: suffixOrName(prefix, ent), kind: "path", source: "local",
			display: replaceLastToken(req.Input, dirPart+ent), isSuffix: true, mr: mr,
		})
	}
	return out
}

// lastSepIndex returns the index of the last path separator in s, or -1.
func lastSepIndex(s string) int {
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return i
		}
	}
	return idx
}

// historyCandidates adds distinct recent executed lines as candidates.
func (e *Engine) historyCandidates(req Request, ui *usageIndex) []cand {
	var out []cand
	seen := map[string]bool{}
	input := strings.TrimSpace(req.Input)
	limit := MaxHistory
	for _, line := range ui.recent {
		if limit <= 0 {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		limit--
		mr := match.Match(input, line)
		if mr.Tier == match.TierNone {
			continue
		}
		// Usage is keyed by the full executed command line.
		rs := e.rankScore(ui, line, req.CWD, false)
		out = append(out, cand{
			text: suffixOrName(input, line), kind: "history", source: "history",
			display: line, isSuffix: isPrefixMatch(input, line), mr: mr, rankScore: rs,
		})
	}
	return out
}

// chainMatch is superseded by chainWeights; kept as a thin wrapper only for
// compatibility with older call sites (none remain). It always returns false.
func (e *Engine) chainMatch(ui *usageIndex, cmd string, req Request) bool { return false }

// cmdToCand converts a knowledge command into a candidate.
func (e *Engine) cmdToCand(name, input string, mr match.Result, rs float64, source string) cand {
	// Exact match: offer a trailing space so the user can keep typing args.
	if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(input)) {
		return cand{text: " ", kind: "command", source: source, display: name + " ", isSuffix: true, mr: mr, rankScore: rs}
	}
	return cand{
		text: suffixOrName(input, name), kind: "command", source: source,
		display: name, isSuffix: isPrefixMatch(input, name), mr: mr, rankScore: rs,
	}
}

// helpers ---------------------------------------------------------------

// isPrefixMatch reports whether input is a (case-insensitive) prefix of name.
func isPrefixMatch(input, name string) bool {
	return strings.HasPrefix(strings.ToLower(name), strings.ToLower(strings.TrimSpace(input)))
}

// suffixOrName returns the suffix when input prefixes name, else the name.
func suffixOrName(input, name string) string {
	if isPrefixMatch(input, name) {
		return name[len(strings.TrimSpace(input)):]
	}
	return name
}

// replaceLastToken swaps the last whitespace-delimited token of line with repl.
func replaceLastToken(line, repl string) string {
	idx := strings.LastIndex(line, " ")
	if idx < 0 {
		return repl
	}
	return line[:idx+1] + repl
}

// splitPath resolves current + dir part of a token into (dir, prefix).
func splitPath(cwd, current string) (string, string) {
	if current == "" || !strings.ContainsAny(current, `/\`) {
		return cwd, current
	}
	if strings.HasSuffix(current, "/") || strings.HasSuffix(current, `\`) {
		// Already inside a directory: complete its children.
		dir := current
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		return filepath.Clean(dir), ""
	}
	dir := filepath.Dir(current)
	prefix := filepath.Base(current)
	if dir == "." {
		dir = cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir), prefix
}

// firstToken returns the first whitespace-delimited token.
func firstToken(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}
