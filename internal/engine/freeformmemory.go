package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// Project memory for freeform cards — session memory and global memory,
// plain markdown under .gummi/memory/ that a freeform session reads at
// spawn and fills as it works: the offrun.dev idea translated to gummi's
// runtime. Where offrun hands every agent its conventions at session
// start and has each one write back as it goes, a freeform card's spawn
// inlines what memory holds (memoryCard) and offers two gummi tools to
// read all of it and fill the card's own. The tiers are named for their
// scope, not their content:
//
//   - Global memory (.gummi/memory/global.md) is every freeform session's
//     in this workspace — the durable facts. Read-only to a session: the
//     person fills and restructures it by hand, and what distills into
//     it is the later, separate feature; a session that learned
//     something the others should start with records it in its own
//     session memory.
//   - Session memory (.gummi/memory/<card>/plan.md and dead-ends.md) is
//     one card's own: the working plan, kept current, and the dead ends,
//     recorded. What they hold is what the next backend of the same
//     conversation starts from — whether it can resume its predecessor's
//     own transcript or not.
//
// Nothing distills one tier into the other: memory that outlives a card
// is a later, separate feature. And none of it is required — no section
// is demanded, no gate reads it, and no stage session is ever offered
// the tools: a stage's durable context carrier is the spec, and this is
// deliberately not that (DESIGN §19).
const (
	memoryReadToolName  = "memory_read"
	memoryWriteToolName = "memory_write"

	// The three files, as the tools' which argument names them: the
	// workspace's global memory, and a card's own session memory.
	memoryGlobal   = "global"
	memoryPlan     = "plan"
	memoryDeadEnds = "dead-ends"

	// maxMemoryFile caps one memory file, as written and as read back
	// whole. Memory rides every freeform spawn and is re-read whole by
	// its tools; an unbounded file would become exactly the context tax
	// this exists to keep small.
	maxMemoryFile = 64 << 10

	// What of each file rides the system prompt itself, capped per file
	// with the truncation noted; the rest is one memory_read away.
	maxGlobalInline        = 4 << 10
	maxSessionMemoryInline = 3 << 10
)

func memoryReadTool() agent.ToolDef {
	return agent.ToolDef{
		Name: memoryReadToolName,
		Description: "Read one project-memory file. \"global\" is the workspace's global " +
			"memory, shared by every freeform session here; \"plan\" and \"dead-ends\" are " +
			"this card's own session memory. Returns the file's content, or a note that " +
			"nothing has been written yet.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"which": map[string]any{
					"type":        "string",
					"description": "One of \"global\", \"plan\", \"dead-ends\".",
				},
			},
			"required": []any{"which"},
		},
	}
}

func memoryWriteTool() agent.ToolDef {
	return agent.ToolDef{
		Name: memoryWriteToolName,
		Description: "Fill one of this card's own project-memory files — the same " +
			"two memory_read reads that are yours: \"plan\" — your working plan for " +
			"this card, replace to rewrite it or append to add — and \"dead-ends\" — " +
			"what you tried that failed, appended so the attempts stay listed. " +
			"\"global\" is not writable through this tool: it is filled elsewhere " +
			"and read with memory_read.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"which": map[string]any{
					"type":        "string",
					"description": "One of \"plan\", \"dead-ends\".",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The file's new content (replacing) or the text to add (appending).",
				},
				"append": map[string]any{
					"type":        "boolean",
					"description": "Add to the end of the file instead of replacing it.",
				},
			},
			"required": []any{"which", "content"},
		},
	}
}

// freeformMemoryHint is added to the hints of a freeform session offered
// the memory tools; the "read at the start" half is memoryCard, which
// inlines what memory already holds.
const freeformMemoryHint = `You also have gummi's memory tools, memory_read and memory_write: this
card's project memory, plain files under .gummi/memory/ — not in your
worktree, so reach them only through the tools. Session memory —
"plan" and "dead-ends" — is this card's own and yours to write: keep plan
current as you work (the next backend of this conversation starts from it,
in place of turns it cannot replay), and record a dead end when you hit
one, so no later turn pays for it twice. Global memory is read-only to
you: the person fills it by hand and a later distillation pass folds
session memory into it, so if something you learned should move up,
record it in your own memory and say so in the thread. None of it is
required — no gate reads it and no section is demanded; this is not the
spec process the workflow cards run.`

// memoryPath maps a which argument to its file. ok is false for anything
// the tools do not serve; the refusal lists the three, which is the
// second time the tool's description taught them.
func memoryPath(w state.Workspace, id domain.FeatureID, which string) (path string, ok bool) {
	switch which {
	case memoryGlobal:
		return w.GlobalMemoryFile(), true
	case memoryPlan:
		return filepath.Join(w.SessionMemoryDir(id), "plan.md"), true
	case memoryDeadEnds:
		return filepath.Join(w.SessionMemoryDir(id), "dead-ends.md"), true
	default:
		return "", false
	}
}

// memoryPathChecked resolves a which argument against this card's
// workspace, refusing a which the tools do not serve and an engine
// without a workspace — a memory that lives nowhere serves nobody.
func (ff *FreeformSession) memoryPathChecked(which string) (string, error) {
	w := ff.engine.cfg.Workspace
	if w.Root == "" {
		return "", errors.New("no workspace: this session's engine has no .gummi")
	}
	path, ok := memoryPath(w, ff.id, which)
	if !ok {
		return "", fmt.Errorf("which is one of %q, %q or %q — %q is none of them",
			memoryGlobal, memoryPlan, memoryDeadEnds, which)
	}
	return path, nil
}

// handleMemoryTool answers memory_read and memory_write for a freeform
// session; a stage session has neither — its durable context carrier is
// the spec, and a second, ungated record beside it is what the freeform
// kind exists to be for the cards that have no spec. Every answer here
// resolves at once (file reads and writes, no human in the loop), so the
// tool is dispatched the way resolve_annotation is.
func (e *Engine) handleMemoryTool(s *Session, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	ff := e.Freeform(s.Feature.ID)
	if ff == nil || s.Feature.Stage != domain.StageOpen {
		e.resolveNow(s, tc.ID, tc.Name+" is only available in a freeform session")
		return
	}
	var a struct {
		Which   string `json:"which"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	_ = json.Unmarshal(tc.Args, &a)
	switch tc.Name {
	case memoryReadToolName:
		out, err := ff.readMemory(a.Which)
		if err != nil {
			e.resolveNow(s, tc.ID, err.Error())
			return
		}
		e.resolveNow(s, tc.ID, out)
	case memoryWriteToolName:
		out, err := ff.writeMemory(a.Which, a.Content, a.Append)
		if err != nil {
			e.resolveNow(s, tc.ID, err.Error())
			return
		}
		e.resolveNow(s, tc.ID, out)
	default:
		e.resolveNow(s, tc.ID, fmt.Sprintf("unknown memory tool %q", tc.Name))
	}
}

// readMemory returns a memory file's content. A file nothing has written
// yet is a normal state, not an error — the tools are how the first write
// happens — so it reads back as a note.
func (ff *FreeformSession) readMemory(which string) (string, error) {
	path, err := ff.memoryPathChecked(which)
	if err != nil {
		return "", err
	}
	s, cut, ok, err := readMemoryFile(path, maxMemoryFile)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "(empty — nothing written yet)", nil
	case cut:
		return fmt.Sprintf("%s\n\n(truncated at %d bytes — the file has outgrown its cap and should be rewritten tighter)",
			s, maxMemoryFile), nil
	default:
		return s, nil
	}
}

// writeMemory fills one memory file. plan and dead-ends are the card's
// own and take either form; global is not a session's to write at all —
// the person fills it by hand and a later distillation pass owns what
// goes into it, so the tool refuses it before anything touches a file.
// A replace goes through atomicfile: memory has no git backstop, and a
// crash mid-write must not leave a torn half-file behind.
func (ff *FreeformSession) writeMemory(which, content string, addTo bool) (string, error) {
	path, err := ff.memoryPathChecked(which)
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errors.New("an empty write writes nothing")
	}
	// checked before anything touches the file: global is not a
	// session's to write, in either form
	if which == memoryGlobal {
		return "", errors.New("global memory is not written by sessions: it is filled by hand " +
			"and distilled from session memory later — record what you learned in " +
			"your own \"plan\" or \"dead-ends\" and say in the thread if it should " +
			"move up to global")
	}
	// the cap is checked before the append/replace split: an append onto
	// a file nothing wrote yet would otherwise grow it past the cap on
	// its first write — appendMemory's own check only guards the growth
	// of a file that already has content
	if len(content)+1 > maxMemoryFile {
		return "", fmt.Errorf("a memory file is capped at %d bytes — rewrite it tighter", maxMemoryFile)
	}
	// one writer at a time, per card: an MCP backend may issue two
	// memory_write calls in parallel (mcpsock dispatches each in its own
	// goroutine), and a rename-based replace racing an append would drop
	// the append onto the replaced inode. Writes serialize; reads stay
	// free to race them.
	ff.writeMu.Lock()
	defer ff.writeMu.Unlock()
	if err := ensureMemoryDir(path); err != nil {
		return "", err
	}
	if addTo {
		return ff.appendMemory(path, which, content)
	}
	if err := atomicfile.Write(path, []byte(content+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing %s memory: %w", which, err)
	}
	return "wrote " + which, nil
}

// appendMemory adds one entry with a single O_APPEND write — atomic
// against a concurrent append to the same file. A symlink already
// sitting at the path is refused rather than followed, the same
// anti-symlink-smuggle rule the workspace's directories are created
// under. The checks are defense in depth, not a boundary: the files
// live in the operator's own .gummi, and reads follow symlinks
// unchecked.
func (ff *FreeformSession) appendMemory(path, which, content string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	size := int64(0)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symlink; refusing to write memory through it", path)
		}
		size = fi.Size()
	}
	sep := ""
	if size > 0 {
		sep = "\n\n"
	}
	// the write's exact size — separator, entry, and the newline every
	// entry ends with — against the whole file's cap
	if size+int64(len(sep)+len(content)+1) > maxMemoryFile {
		return "", fmt.Errorf("this memory file is at its %d-byte cap — replace it (append=false) with tighter content", maxMemoryFile)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(sep + content + "\n"); err != nil {
		return "", err
	}
	return "appended to " + which, nil
}

// ensureMemoryDir creates a memory file's card directory and the memory
// directory above it, each through state.MkdirChecked: a repo that ships
// .gummi committed could carry a symlink pointing outside the checkout,
// and a bare MkdirAll would follow it.
func ensureMemoryDir(path string) error {
	cardDir := filepath.Dir(path)
	if err := state.MkdirChecked(filepath.Dir(cardDir), 0o750); err != nil {
		return err
	}
	return state.MkdirChecked(cardDir, 0o750)
}

// memoryCard is the read half at spawn: what global and this card's
// session memory hold, inlined into the system hints so a backend starts
// oriented without paying a read — the "read at the start" half of the
// deal, and the reason a respawn or a restart of the board starts where
// the conversation left off even when the backend cannot replay its own
// turns. Nothing is inlined for files that are empty or absent, and
// nothing at all while memory is empty: the tools' hint already teaches
// the layout, and an empty card is one less paragraph in every prompt.
// Contents are capped per file with the cap noted, because the card
// rides every system prompt this session spawns.
func (e *Engine) memoryCard(id domain.FeatureID) string {
	w := e.cfg.Workspace
	if w.Root == "" {
		return ""
	}
	global, gCut := readMemoryInline(w.GlobalMemoryFile(), maxGlobalInline)
	plan, pCut := readMemoryInline(filepath.Join(w.SessionMemoryDir(id), "plan.md"), maxSessionMemoryInline)
	dead, dCut := readMemoryInline(filepath.Join(w.SessionMemoryDir(id), "dead-ends.md"), maxSessionMemoryInline)
	if global == "" && plan == "" && dead == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Project memory: plain files under .gummi/memory/ that you read through\n" +
		"your memory tools and fill where a file is yours — none of it required, no\n" +
		"gate reads it.\n")
	if global != "" {
		fmt.Fprintf(&b, "\nGlobal memory, shared by every freeform session in this workspace:\n\n%s", global)
		if gCut {
			b.WriteString("\n\n(truncated — read the rest with memory_read)")
		}
		b.WriteString("\n")
	}
	if plan != "" || dead != "" {
		fmt.Fprintf(&b, "\nSession memory, this card's own (%s):\n", id)
		if plan != "" {
			fmt.Fprintf(&b, "\nPlan:\n\n%s", plan)
			if pCut {
				b.WriteString("\n\n(truncated — read the rest with memory_read)")
			}
			b.WriteString("\n")
		}
		if dead != "" {
			fmt.Fprintf(&b, "\nDead ends — tried, failed, recorded:\n\n%s", dead)
			if dCut {
				b.WriteString("\n\n(truncated — read the rest with memory_read)")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// readMemoryInline reads a memory file for the spawn hint, dropping empty
// ones and capping what rides the prompt.
func readMemoryInline(path string, max int) (content string, cut bool) {
	s, cut, ok, err := readMemoryFile(path, max)
	if err != nil || !ok {
		return "", false
	}
	s = strings.TrimRight(s, " \t\n")
	if s == "" {
		return "", false
	}
	return s, cut
}

// readMemoryFile reads one memory file without ever loading more of it
// than max bytes — a file that outgrew its cap must not become a
// per-spawn tax — and backs a cut tail up to a rune boundary, since a
// split UTF-8 rune reads worse than a slightly shorter cut. ok is false
// when the file does not exist (a normal state, not an error); cut
// reports that the file held more than max.
func readMemoryFile(path string, max int) (content string, cut, ok bool, err error) {
	f, oerr := os.Open(path)
	if errors.Is(oerr, fs.ErrNotExist) {
		return "", false, false, nil
	}
	if oerr != nil {
		return "", false, false, oerr
	}
	defer f.Close()
	b, rerr := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if rerr != nil {
		return "", false, false, rerr
	}
	if len(b) > max {
		b, cut = b[:max], true
		for len(b) > 0 {
			r, size := utf8.DecodeLastRune(b)
			if r != utf8.RuneError || size != 1 {
				break
			}
			b = b[:len(b)-1]
		}
	}
	return string(b), cut, true, nil
}
