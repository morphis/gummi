package engine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ProjectCommand is a prompt the repository carries for its agents as a
// file, invoked as "/name args" — Claude Code's .claude/commands,
// opencode's .opencode/command and Copilot's .github/prompts. A freeform
// session reads them from its card's own worktree, so what a command says
// is what the branch says, and expands them itself rather than trusting
// each backend to: the same line then means the same thing whichever
// agent the session runs.
type ProjectCommand struct {
	Name        string
	Description string
	// Source is the file it came from, relative to the worktree.
	Source string
	body   string
	// builtin marks a command gummi offers itself rather than a file's;
	// it goes to the backend as typed.
	builtin bool
}

// projectCommandDirs is where each backend keeps its command files, in
// the order a name clash is settled: the first directory to define a
// name owns it.
var projectCommandDirs = []struct {
	dir, suffix string
	nested      bool // subdirectories namespace a name as "dir:name"
}{
	{".claude/commands", ".md", true},
	{".opencode/command", ".md", false},
	{".opencode/commands", ".md", false},
	{".github/prompts", ".prompt.md", false},
}

// LoadProjectCommands reads the command files under workDir, sorted by
// name. A missing directory or an unreadable file is skipped: a command
// that cannot be read is one the session does not offer, not an error.
func LoadProjectCommands(workDir string) []ProjectCommand {
	if workDir == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []ProjectCommand
	for _, d := range projectCommandDirs {
		// rooted, so a symlink in the branch cannot lead the walk out of it
		root, err := os.OpenRoot(filepath.Join(workDir, filepath.FromSlash(d.dir)))
		if err != nil {
			continue
		}
		fsys := root.FS()
		_ = fs.WalkDir(fsys, ".", func(rel string, ent fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ent.IsDir() {
				if rel != "." && !d.nested {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(ent.Name(), d.suffix) {
				return nil
			}
			name := strings.ReplaceAll(strings.TrimSuffix(rel, d.suffix), "/", ":")
			if name == "" || strings.ContainsAny(name, " \t") || seen[strings.ToLower(name)] {
				return nil
			}
			raw, err := fs.ReadFile(fsys, rel)
			if err != nil {
				return nil
			}
			desc, body := splitCommandFrontmatter(string(raw))
			seen[strings.ToLower(name)] = true
			out = append(out, ProjectCommand{
				Name:        name,
				Description: desc,
				Source:      d.dir + "/" + rel,
				body:        body,
			})
			return nil
		})
		_ = root.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// splitCommandFrontmatter returns a command file's description and its
// body. The description is the frontmatter's, or failing that the body's
// first non-empty line, which is what all three backends show for a file
// that has none.
func splitCommandFrontmatter(raw string) (desc, body string) {
	body = raw
	if rest, ok := strings.CutPrefix(raw, "---\n"); ok {
		if front, after, ok := strings.Cut(rest, "\n---"); ok {
			body = strings.TrimPrefix(strings.TrimPrefix(after, "\r"), "\n")
			for _, line := range strings.Split(front, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
					desc = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
		}
	}
	body = strings.TrimSpace(body)
	if desc == "" {
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(strings.TrimLeft(line, "# ")); line != "" {
				desc = line
				break
			}
		}
	}
	return desc, body
}

// FindProjectCommand reports the command a line invokes: its leading
// "/name" matched, case-insensitively, against cmds.
func FindProjectCommand(cmds []ProjectCommand, line string) (ProjectCommand, string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "/")
	if !ok {
		return ProjectCommand{}, "", false
	}
	name, args, _ := strings.Cut(rest, " ")
	for _, c := range cmds {
		if strings.EqualFold(c.Name, name) {
			return c, strings.TrimSpace(args), true
		}
	}
	return ProjectCommand{}, "", false
}

// expand is the prompt "/name args" stands for: $ARGUMENTS takes the whole
// argument string and $1…$9 its words, as Claude Code and opencode both
// substitute them. A body that asks for neither still gets the arguments,
// after it, rather than having them silently dropped. File references and
// shell lines are then resolved against workDir (resolveCommandRefs).
func (c ProjectCommand) expand(workDir, args string) string {
	body := c.body
	words := strings.Fields(args)
	used := strings.Contains(body, "$ARGUMENTS")
	body = strings.ReplaceAll(body, "$ARGUMENTS", args)
	for i := 9; i >= 1; i-- {
		tok := "$" + strconv.Itoa(i)
		if !strings.Contains(body, tok) {
			continue
		}
		used = true
		val := ""
		if i <= len(words) {
			val = words[i-1]
		}
		body = strings.ReplaceAll(body, tok, val)
	}
	if !used && args != "" {
		body += "\n\nARGUMENTS: " + args
	}
	return resolveCommandRefs(workDir, body)
}

// commandShellRef is Claude Code's and opencode's "!`cmd`": a shell line
// whose output belongs in the prompt. commandFileRef is their "@path": a
// file whose contents do.
var (
	commandShellRef = regexp.MustCompile("!`([^`\n]+)`")
	commandFileRef  = regexp.MustCompile(`(^|\s)@([\w./-]*[\w/-])`)
)

// commandFileMax caps what one "@path" inlines; a larger file is left as
// its reference for the agent to read itself.
const commandFileMax = 64 << 10

// resolveCommandRefs is what gummi does with a command's "@path" and
// "!`cmd`". A file the worktree holds is inlined, read through a root so
// a reference cannot reach outside the branch. A shell line is never run
// here: gummi would be running the branch's code outside the agent's
// sandbox and permission mode, so the agent is asked to run it instead,
// under the same confinement as anything else it runs.
func resolveCommandRefs(workDir, body string) string {
	var cmds []string
	body = commandShellRef.ReplaceAllStringFunc(body, func(m string) string {
		cmd := commandShellRef.FindStringSubmatch(m)[1]
		cmds = append(cmds, cmd)
		return "the output of `" + cmd + "`"
	})
	var files []string
	if workDir != "" && strings.Contains(body, "@") {
		if root, err := os.OpenRoot(workDir); err == nil {
			seen := map[string]bool{}
			for _, m := range commandFileRef.FindAllStringSubmatch(body, -1) {
				rel := m[2]
				if seen[rel] {
					continue
				}
				seen[rel] = true
				raw, err := root.ReadFile(filepath.FromSlash(rel))
				if err != nil || len(raw) > commandFileMax || !utf8.Valid(raw) {
					continue
				}
				files = append(files, fmt.Sprintf("Contents of %s:\n```\n%s\n```", rel, strings.TrimRight(string(raw), "\n")))
			}
			_ = root.Close()
		}
	}
	if len(cmds) > 0 {
		var b strings.Builder
		b.WriteString("Before anything else, run these commands in the worktree and use their output where the prompt below refers to it:\n")
		for _, c := range cmds {
			b.WriteString("- `" + c + "`\n")
		}
		body = b.String() + "\n" + body
	}
	if len(files) > 0 {
		body += "\n\n" + strings.Join(files, "\n\n")
	}
	return body
}

// expandProjectCommands rewrites each paragraph of a turn that invokes a
// command into the command's prompt. Per paragraph because a drained
// queue joins its lines that way (drainQueue), and each was typed as a
// line of its own.
func expandProjectCommands(cmds []ProjectCommand, workDir, msg string) string {
	if len(cmds) == 0 || !strings.Contains(msg, "/") {
		return msg
	}
	parts := strings.Split(msg, "\n\n")
	for i, p := range parts {
		if c, args, ok := FindProjectCommand(cmds, p); ok && !c.builtin && !strings.Contains(strings.TrimSpace(p), "\n") {
			parts[i] = c.expand(workDir, args)
		}
	}
	return strings.Join(parts, "\n\n")
}

// compactCommand is the one command a freeform session offers that no
// file defines: "/compact" replaces the agent's conversation with a summary
// of it. It is not expanded — Claude Code reads the line as typed — and
// a backend whose session is an agent.Compactor is asked directly instead
// (FreeformSession.SendTurn). A project file named compact wins over it.
var compactCommand = ProjectCommand{
	Name:        "compact",
	Description: "summarize the conversation so far to free the agent's context",
	builtin:     true,
}

// isCompactLine reports whether msg is a lone "/compact" line that cmds
// resolve to the built-in rather than to a project file. Arguments are
// allowed and dropped: a Compactor takes no instructions.
func isCompactLine(cmds []ProjectCommand, msg string) bool {
	if strings.Contains(strings.TrimSpace(msg), "\n") {
		return false
	}
	c, _, ok := FindProjectCommand(cmds, msg)
	return ok && c.builtin && strings.EqualFold(c.Name, compactCommand.Name)
}
