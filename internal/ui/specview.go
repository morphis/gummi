package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

// specView is the spec surface state: one feature's design doc, shown as
// line-addressed source (DESIGN §6.1).
//
// There is one view, not a read mode and an annotate mode. Read mode was
// a glamour render, and glamour re-wraps text — so a cursor on a
// rendered row could never say which source line it was on, and comments
// are addressed by source line. The two modes were therefore two
// different documents, and the key that toggled between them was one of
// five things tab meant. The source is now styled in place instead
// (mdsource.go): headings, code and emphasis read as themselves without
// a character moving, so one view is both readable and addressable.
type specView struct {
	// workflowsOff is set when the session is not offered workflow runs: the
	// view leaves out the key that asks for one
	workflowsOff bool
	f            domain.Feature
	path         string
	content      string
	doc          spec.Doc
	cursor       int // 1-based source line
	// n is which of a session's specs is open, and specs the list of them
	// to switch between (DESIGN §25.3), each with its state, revision,
	// runs and done-when count; both zero on any other card's document.
	n     int
	specs []webapi.SpecEntry
}

// specLoadedMsg delivers a (re)loaded spec document.
type specLoadedMsg struct {
	f       domain.Feature
	path    string
	content string
	err     error
	// n and specs are a session's: the spec loaded, and its list.
	n     int
	specs []webapi.SpecEntry
}

// openSpec resolves the feature's spec file — a session's own spec file
// directly (no draft, no migration: a session's spec is started whole, by
// spec_start or + New spec), or for a retired workflow card, the workspace copy under
// .gummi/specs|bugs once the feature has a worktree, the draft under
// .gummi/state/drafts/ before then (created from the template on first
// open).
func (m *Shell) openSpec(f domain.Feature) tea.Cmd {
	if hasSessionSpec(f) {
		return m.openSessionSpec(f)
	}
	return func() tea.Msg {
		ctx := context.Background()
		// Once the worktree exists the artifact lives at its workspace
		// home: ensure it was promoted there (idempotent) and read that
		// copy. Before then, it is a draft under state/drafts/.
		var path string
		if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
			if err := m.migrateDraft(&f); err != nil {
				return specLoadedMsg{err: err}
			}
			path = filepath.Join(m.wt.Root(), f.ArtifactPath())
		} else {
			path = filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
			if err := spec.EnsureDraft(path, &f); err != nil {
				return specLoadedMsg{err: err}
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return specLoadedMsg{err: err}
		}
		return specLoadedMsg{f: f, path: path, content: string(raw)}
	}
}

// sessionSpecAt is which of f's specs lives at path, 1 when none is
// found: the view is mounted on one spec's file.
func (m *Shell) sessionSpecAt(f domain.Feature, path string) int {
	specs, _ := m.store.SessionSpecs(context.Background(), f.ID)
	for _, sp := range specs {
		if filepath.Join(m.wt.Root(), filepath.FromSlash(sp.Path)) == path {
			return sp.N
		}
	}
	return 1
}

// openSessionSpec mounts a session's own spec directly — no draft, no
// migrateDraft, no EnsureDraft: those exist to seed a retired workflow
// card's artifact; a session's specs are started by spec_start, the web
// page's + New spec, or + in this view (startNewSpec). It opens spec 1, or
// the spec already open on f.
func (m *Shell) openSessionSpec(f domain.Feature) tea.Cmd {
	n := 1
	if m.spec != nil && m.spec.f.ID == f.ID && m.spec.n > 0 {
		n = m.spec.n
	}
	return m.loadSessionSpec(f, n)
}

// loadSessionSpec reads f's spec n and the list of f's specs.
func (m *Shell) loadSessionSpec(f domain.Feature, n int) tea.Cmd {
	path, _ := m.store.SessionSpecFile(context.Background(), m.wt.Root(), f.ID, n)
	var docs *WebDocs
	if r, ok := m.rowByID(f.ID); ok {
		docs, _ = m.webDocsFor(r)
	}
	return func() tea.Msg {
		raw, err := os.ReadFile(path)
		if err != nil {
			return specLoadedMsg{err: err}
		}
		msg := specLoadedMsg{f: f, path: path, content: string(raw), n: n}
		if docs != nil {
			if list, err := docs.Specs(context.Background()); err == nil {
				msg.specs = list.Specs
			}
		}
		return msg
	}
}

// startNewSpec starts a new spec on the session the spec view is open on,
// titled from the + dialog. It is refused where the web page's + New spec
// is (newSpecRefusal), and recorded as the person at this board, the same
// actor the view's own edits are. The engine call runs off the update
// loop; specStartedMsg brings the new spec's number back to it, where the
// view is switched, since loading the view reads the board's rows.
func (m *Shell) startNewSpec(title string) tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	f := sv.f
	if err := newSpecRefusal(f); err != nil {
		return errNoticeCmd(err.Error())
	}
	person := m.humanActor()
	return func() tea.Msg {
		return m.withEngine(func(eng *engine.Engine) tea.Msg {
			sp, _, err := eng.StartSessionSpec(context.Background(), f, title, person)
			if err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return specStartedMsg{f: f, n: sp.N}
		})
	}
}

// askForRun asks the session for a workflow run on the spec open: the
// /workflow line the composer would send, so the agent brings the spec up
// to date and proposes the run, and the person allows it (DESIGN §25.5).
func (m *Shell) askForRun() tea.Cmd {
	sv := m.spec
	if sv == nil || !hasSessionSpec(sv.f) || sv.n == 0 || sv.f.Stage == domain.StageDone {
		return nil
	}
	if m.engine == nil {
		m.notice = noticeMsg{text: m.noAgent(" (set a model/provider to enable agents)")}
		return nil
	}
	f, n, eng := sv.f, sv.n, m.engine
	if !eng.WorkflowsOn(context.Background(), f.ID) {
		m.notice = noticeMsg{text: "workflows are off for this session — turn them on from its card menu"}
		return nil
	}
	who := engine.WithActor(context.Background(), m.lineActor())
	return func() tea.Msg {
		ff, err := eng.OpenFreeform(context.Background(), f)
		if err == nil {
			err = ff.SendTurn(who, engine.WorkflowAskLine(n), nil)
		}
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("asked the session to propose a run on spec %d", n)}
	}
}

// specStartedMsg is a new spec started from the spec view: the view opens
// it.
type specStartedMsg struct {
	f domain.Feature
	n int
}

// switchSpec opens the session's spec delta places along its list.
func (m *Shell) switchSpec(delta int) tea.Cmd {
	sv := m.spec
	if sv == nil || len(sv.specs) < 2 {
		if sv != nil && sv.n > 0 {
			m.notice = noticeMsg{text: sv.f.Name() + " keeps one spec"}
		}
		return nil
	}
	at := 0
	for i, e := range sv.specs {
		if e.N == sv.n {
			at = i
		}
	}
	next := sv.specs[clamp(at+delta, 0, len(sv.specs)-1)].N
	if next == sv.n {
		return nil
	}
	return m.loadSessionSpec(sv.f, next)
}

// reloadSpec re-reads the currently open spec from disk.
func (m *Shell) reloadSpec() tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	if sv.n > 0 {
		return m.loadSessionSpec(sv.f, sv.n)
	}
	return func() tea.Msg {
		raw, err := os.ReadFile(sv.path)
		if err != nil {
			return specLoadedMsg{err: err}
		}
		return specLoadedMsg{f: sv.f, path: sv.path, content: string(raw)}
	}
}

// addSpecComment writes an annotation into the doc and reloads it.
func (m *Shell) addSpecComment(line int, text string) tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	reload := m.reloadSpec()
	f, path := sv.f, sv.path
	return func() tea.Msg {
		if err := m.sessionSpecClosed(f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := writeSpecNote(path, line, m.now().Format("2006-01-02"), text); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.noteSessionSpecRevision(f, path); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// noteSessionSpecRevision records a session's spec, just written to path
// by the caller, as a new numbered revision through the engine — the
// terminal's mirror of the web page's own writer (WebDocs.noteSessionRevision),
// so a comment, a resolution or an $EDITOR save made here reaches a
// open workflow run that froze it as a steer the same way a web edit does. A no-op for
// every card that is not such a session; a static board records the
// revision through withEngine's own short-lived engine.
func (m *Shell) noteSessionSpecRevision(f domain.Feature, path string) error {
	if !hasSessionSpec(f) {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ctx := context.Background()
	n := m.sessionSpecAt(f, path)
	fresh := f
	if got, ferr := m.store.GetFeature(ctx, f.ID); ferr == nil {
		fresh = got
	}
	var writeErr error
	m.withEngine(func(eng *engine.Engine) tea.Msg {
		_, writeErr = eng.WriteSessionSpec(ctx, fresh, n, string(raw), m.humanActor())
		return nil
	})
	return writeErr
}

// sessionSpecClosed refuses a write to a closed session's spec, read from
// the store rather than the view's load-time copy — the same refusal the
// web page's writers give (closedToReview).
func (m *Shell) sessionSpecClosed(f domain.Feature) error {
	if !hasSessionSpec(f) {
		return nil
	}
	if got, err := m.store.GetFeature(context.Background(), f.ID); err == nil {
		f = got
	}
	return closedToReview(f, "its spec takes no more edits")
}

// writeSpecNote writes one `@user` note under line of the document at
// path. stamp is the marker's parenthesis (spec.Stamp): the date, and the
// person when the note came from a named device. It is the one writer
// every surface that lets a person comment on a document goes through.
//
// It serializes against the engine's annotate/answer-capture writers and
// re-reads the current file (not a load-time copy) under the lock, so a
// concurrent marker isn't clobbered, and writes atomically.
func writeSpecNote(path string, line int, stamp, text string) error {
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := spec.AddComment(string(raw), line, "user", stamp, text)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(out), 0o600)
}

// resolveSpecComment writes a resolution for the thread at the given
// marker line and reloads the doc. Same writer discipline as
// addSpecComment: serialize under the per-file lock, re-read the current
// file, splice, and write atomically.
//
// reason is the one-line answer the user typed into the resolve dialog
// (empty when they left it blank); it is folded into the resolution
// marker itself (see resolveWithReason) rather than dropped, so a bare
// "resolved" is a choice the user made, not the only option gummi offered.
func (m *Shell) resolveSpecComment(line int, reason string) tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	reload := m.reloadSpec()
	f, path := sv.f, sv.path
	return func() tea.Msg {
		if err := m.sessionSpecClosed(f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := writeSpecResolution(path, line, m.now().Format("2006-01-02"), reason, nil); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := m.noteSessionSpecRevision(f, path); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// writeSpecResolution resolves the marker at line of the document at
// path as `@user`, with reason folded in (resolveWithReason), under the
// same writer discipline as writeSpecNote. check, when set, is shown the
// marker as it stands under the lock and may refuse: a reader who
// resolved a note they saw on a page that has since moved must not close
// whatever now sits on that line.
func writeSpecResolution(path string, line int, stamp, reason string, check func(spec.Marker) error) error {
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(markerAt(spec.Parse(string(raw)), line)); err != nil {
			return err
		}
	}
	out, err := resolveWithReason(string(raw), line, "user", stamp, reason)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(out), 0o600)
}

// markerAt is the marker on line, or a zero Marker when line holds none.
func markerAt(d spec.Doc, line int) spec.Marker {
	for _, mk := range d.Markers {
		if mk.Line == line {
			return mk
		}
	}
	return spec.Marker{}
}

// resolveWithReason closes the marker at line, same placement rule as
// spec.ResolveComment (immediately after line, so a resolution closes only
// the markers above it in the run — see spec.Doc.Threads) — but lets the
// caller record why, instead of always writing the bare word. An architect
// complained, in the drive this fix comes from, that the follow-up marker
// "just says resolved without recording the answer"; an empty reason still
// falls back to plain "resolved" so resolving stays a one-key action, it
// just no longer forces the terse form when the user has something to say.
//
// This duplicates spec.ResolveComment's splice locally rather than adding
// a reason parameter to it: that function is part of the shared marker
// grammar (internal/spec) that Doc.Parse and Doc.Threads read back, and a
// reason is only a UI convenience — it does not change what counts as a
// resolution (resolvedRe still matches "resolved" followed by an em dash),
// so it does not belong in that package's contract.
func resolveWithReason(content string, line int, author, date, reason string) (string, error) {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (1..%d)", line, len(lines))
	}
	if !spec.IsMarkerLine(lines[line-1]) {
		return "", fmt.Errorf("line %d is not a marker", line)
	}
	text := "resolved"
	if reason = strings.TrimSpace(reason); reason != "" {
		text = "resolved — " + reason
	}
	res := fmt.Sprintf("%%%% @%s(%s): %s", author, date, text)
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:line]...)
	out = append(out, res)
	out = append(out, lines[line:]...)
	return strings.Join(out, "\n"), nil
}

// resolveDialog collects an optional one-line reason when resolving a
// comment thread ('x'). It is a distinct type from commentDialog rather
// than a shared one with an extra flag: an empty comment is a no-op
// cancel (there is nothing to write), but an empty resolve reason is
// still a resolve (that is the whole point — see resolveWithReason), so
// the two dialogs disagree about what enter-with-nothing-typed does and
// cannot share one submit rule. It also shows the anchor — the comment
// text being resolved — so the user can see what they are agreeing to
// close without holding the source pane's rendering of it in their head.
type resolveDialog struct {
	anchor   string
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	onSubmit func(reason string) tea.Cmd
}

func newResolveDialog(anchor string, onSubmit func(string) tea.Cmd) *resolveDialog {
	in := textinput.New()
	in.Placeholder = "reason (optional)"
	in.CharLimit = 200
	in.SetWidth(48)
	in.Focus()
	return &resolveDialog{
		anchor: anchor, input: in, onSubmit: onSubmit,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Resolve"}),
	}
}

// ID implements overlay.Dialog.
func (d *resolveDialog) ID() string { return "spec-resolve" }

// submit fires onSubmit unconditionally — unlike commentDialog, an empty
// value here is not a cancel, it is a resolve with no reason given, which
// resolveWithReason turns into the plain "resolved" wording.
func (d *resolveDialog) submit() (bool, tea.Cmd) {
	return true, d.onSubmit(strings.TrimSpace(d.input.Value()))
}

// HandleKey implements overlay.Dialog.
func (d *resolveDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		// only two stops, so tab and shift+tab are the same toggle
		d.setFocus((d.focus + 1) % 2)
		return false, nil
	}
	if d.focus == commentFieldButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
			return false, nil
		case "right", "l":
			d.buttons.Move(1)
			return false, nil
		case "enter":
			if d.buttons.Cursor() == 0 {
				return true, nil
			}
			return d.submit()
		}
		return false, nil
	}
	if key.String() == "enter" {
		return d.submit()
	}
	d.input, _ = d.input.Update(key)
	return false, nil
}

// setFocus moves focus between the input and the button row, keeping the
// textinput's own focus/blur in sync with which one is drawn active.
func (d *resolveDialog) setFocus(f int) {
	d.focus = f
	if f == commentFieldInput {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// HandlePaste implements overlay.Paster.
func (d *resolveDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == commentFieldInput {
		d.input, _ = d.input.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *resolveDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("resolve") + "\n\n")
	if d.anchor != "" {
		b.WriteString(s.Subtle.Render(ansi.Truncate(d.anchor, 60, "…")) + "\n\n")
	}
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(d.buttons.View(s, d.focus == commentFieldButtons) + "\n\n")
	b.WriteString(s.Faint.Render("enter resolve · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// editSpec suspends the TUI and opens the doc in $EDITOR at the cursor
// line (best effort — plain `$EDITOR file` when unknown).
func (m *Shell) editSpec() tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return func() tea.Msg {
			return noticeMsg{text: "$EDITOR is not set", isErr: true}
		}
	}
	if hasSessionSpec(sv.f) {
		if err := closedToReview(sv.f, "its spec takes no more edits"); err != nil {
			return func() tea.Msg { return noticeMsg{text: err.Error(), isErr: true} }
		}
	}
	reload := m.reloadSpec()
	f, path, before := sv.f, sv.path, sv.content
	cmd := exec.CommandContext(context.Background(), editor, path) //nolint:gosec // $EDITOR is the user's own trusted setting
	return tea.ExecProcess(cmd, m.specEdited(f, path, before, reload))
}

// specEdited is what follows the editor's exit: a session's spec saved
// with a change becomes a revision, then the view reloads.
func (m *Shell) specEdited(f domain.Feature, path, before string, reload tea.Cmd) func(error) tea.Msg {
	return func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: fmt.Sprintf("editor: %v", err), isErr: true}
		}
		// only a save that actually changed the file is worth a revision —
		// an editor opened and closed untouched would otherwise spend a
		// revision (and a run's steer) on nothing.
		if raw, rerr := os.ReadFile(path); rerr == nil && string(raw) != before {
			if err := m.noteSessionSpecRevision(f, path); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		return reload()
	}
}

// threadAtCursor returns the thread whose markers include the cursor
// line, or nil when the cursor is not on a marker line — the target for
// the resolve key.
func (sv *specView) threadAtCursor() *spec.Thread {
	for _, t := range sv.doc.Threads() {
		for _, mk := range t.Markers {
			if mk.Line == sv.cursor {
				return &t
			}
		}
	}
	return nil
}

// bindings is the spec surface's key table (see keymap.go): the surface
// has one mode, so every verb it lists is live whenever the surface is.
func (sv *specView) bindings() []binding {
	// Bar order is shedding order: the status bar drops hints from the
	// second-to-last backwards and never the last, so the two rows that
	// answer "what do I do about this document" lead, and the way out
	// goes last where it outlives everything else (threadInputBindings
	// keeps esc last for the same reason). Reading and annotating are
	// what the surface is obviously for; leaving is the row a reader
	// would otherwise have to go looking for.
	bs := []binding{
		{key: "j/k ↓↑", label: "line", help: "move the line cursor"},
		{key: "pgup/pgdn", label: "page", help: "move the line cursor by a page"},
		{key: "c", label: "comment", help: "comment on the cursor line", bar: true},
		{key: "x", label: "resolve", help: "resolve the comment thread at the cursor", bar: true},
		{key: "n/p", label: "comments", help: "jump between comments", bar: true},
		{key: "e", label: "editor", help: "open in $EDITOR at the cursor line"},
	}
	if len(sv.specs) > 1 {
		bs = append(bs, binding{key: "[ ]", label: "specs", help: "the session's previous / next spec", bar: true})
	}
	if hasSessionSpec(sv.f) {
		bs = append(bs, binding{key: "+", label: "new spec", help: "start a new spec on this session", bar: true})
		if sv.n > 0 && sv.f.Stage != domain.StageDone && !sv.workflowsOff {
			bs = append(bs, binding{key: "w", label: "propose a run", help: "ask the session to propose a workflow run on this spec: it brings the spec up to date and proposes, and you allow it", bar: true})
		}
	}
	if len(sv.specRuns()) > 0 {
		bs = append(bs, binding{key: "1-9", label: "jump to run", help: "jump to the run the list's row names, in the thread, unfolded whole"})
	}
	bs = append(bs, []binding{
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
	}...)
	return bs
}

// handleSpecKey processes keys while the spec surface is open.
func (m *Shell) handleSpecKey(key string) tea.Cmd {
	sv := m.spec
	switch key {
	case "esc", "q":
		m.spec = nil
	case "e":
		return m.editSpec()
	case "[":
		return m.switchSpec(-1)
	case "]":
		return m.switchSpec(1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		runs := sv.specRuns()
		i, _ := strconv.Atoi(key)
		if i > len(runs) {
			return nil
		}
		return m.jumpToRun(runs[i-1].N)
	case "w":
		return m.askForRun()
	case "+":
		if !hasSessionSpec(sv.f) {
			return nil
		}
		m.Overlay.Push(newSpecTitleDialog(func(title string) tea.Cmd {
			return m.startNewSpec(title)
		}))
	case "j", "down":
		sv.setCursor(sv.cursor + 1)
	case "k", "up":
		sv.setCursor(sv.cursor - 1)
	case "pgdown":
		sv.setCursor(sv.cursor + m.mainPage())
	case "pgup":
		sv.setCursor(sv.cursor - m.mainPage())
	case "n":
		sv.jumpMarker(1)
	case "p":
		sv.jumpMarker(-1)
	case "c":
		line := sv.cursor
		m.Overlay.Push(newCommentDialog(sv.lineText(line), func(text string) tea.Cmd {
			return m.addSpecComment(line, text)
		}))
	case "x":
		t := sv.threadAtCursor()
		if t == nil {
			m.notice = noticeMsg{text: "no marker on this line"}
			return nil
		}
		if t.Resolved {
			m.notice = noticeMsg{text: "already resolved"}
			return nil
		}
		// resolving used to write a bare "%% @user(date): resolved" with no
		// way to say why — the same contentless marker an architect
		// complained about when an agent did it. The dialog accepts empty
		// (enter with nothing typed still resolves, falling back to the old
		// bare wording) so resolving stays a one-key action; it just is no
		// longer the only shape resolving can take.
		line := sv.cursor
		anchor := markerTextAt(sv.doc, line)
		m.Overlay.Push(newResolveDialog(anchor, func(reason string) tea.Cmd {
			return m.resolveSpecComment(line, reason)
		}))
	}
	return nil
}

// markerTextAt returns the marker text at the given source line, or ""
// when the line carries no marker — the resolve dialog's anchor, so the
// user can see which comment they are about to close without having to
// keep the source pane's own rendering of it in their head.
func markerTextAt(doc spec.Doc, line int) string {
	for _, mk := range doc.Markers {
		if mk.Line == line {
			return mk.Text
		}
	}
	return ""
}

func (sv *specView) setCursor(n int) {
	sv.cursor = min(max(n, 1), len(sv.doc.Lines))
}

// jumpMarker moves the cursor to the next/previous marker line.
func (sv *specView) jumpMarker(dir int) {
	lines := sv.doc.MarkerLines()
	if len(lines) == 0 {
		return
	}
	if dir > 0 {
		for _, l := range lines {
			if l > sv.cursor {
				sv.cursor = l
				return
			}
		}
		sv.cursor = lines[0] // wrap
		return
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] < sv.cursor {
			sv.cursor = lines[i]
			return
		}
	}
	sv.cursor = lines[len(lines)-1] // wrap
}

// specViewRender renders the spec surface into the main pane: the
// status header (live dependencies and the open-thread checklist) over
// the scrolling source.
func (m *Shell) specViewRender(w, h int) string {
	sv := m.spec
	s := m.styles
	if sv == nil {
		return ""
	}
	var b strings.Builder
	// the same noun the card page used to send the reader here: this view
	// opens over a bug's report as often as over a feature's spec, and
	// calling both "spec" renamed the document between the line that
	// pointed at it and the header of the thing it opened.
	noun := artifactNoun(sv.f.Kind)
	if sv.n > 0 {
		// a session's spec, one of its list
		noun = fmt.Sprintf("spec S%d", sv.n)
	}
	head := s.Title.Render(string(sv.f.ID)) + " " + s.Base.Render("· "+noun)
	if open := sv.needsAttentionCount(); open > 0 {
		head += " " + s.Warning.Render(fmt.Sprintf("✎ %d open", open))
	}
	b.WriteString("\n" + head + "\n")
	// Mirrors diffViewRender's own loop-prevention line (diffrender.go):
	// once every open blocking comment already carries the agent's own
	// answer, R's own advice — send the open comments to the agent — would
	// spend a full rework round re-sending threads that are already
	// settled. Doc.Threads only lets a @user resolution close a @user
	// marker, so the open count alone cannot tell the reader the agent has
	// already been here; said here, at the surface holding both keys, not
	// at todo where there is no gate this could re-block (see the
	// blockingLabel swap in renderStatus).
	if sv.f.Stage != domain.StageTodo && sv.blockingAnswered() {
		b.WriteString(s.Warning.Render("  every open comment has already been answered — x resolves one that the answer addressed; R sends them back again") + "\n")
	}
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")
	if lines := specListLines(m, sv, w); len(lines) > 0 {
		b.WriteString(strings.Join(lines, "\n") + "\n")
		b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")
	}

	// the status header used to belong to read mode alone, which meant
	// the mode you could actually act in was the one that never told you
	// what was blocking the gate. It is fixed above the body now, so it
	// is true of the surface rather than of a mode.
	b.WriteString(sv.renderStatus(m, w))

	// keys live in the status bar (keymap.go), so the body gets whatever
	// the header left of the pane (plus one line of slack).
	used := strings.Count(b.String(), "\n")
	b.WriteString(sv.renderSource(m, w, max(h-used-1, 3)))
	return b.String()
}

// specListLines is a session's specs, one row each, the open one marked:
// its title, its state, its newest revision (and the one an open run
// froze), its runs and its done-when count (DESIGN §25.3).
func specListLines(m *Shell, sv *specView, w int) []string {
	if len(sv.specs) == 0 {
		return nil
	}
	s := m.styles
	out := make([]string, 0, len(sv.specs))
	for _, e := range sv.specs {
		lead := "  "
		title := s.Subtle
		if e.N == sv.n {
			lead, title = s.Cursor.Render("▸ "), s.Title
		}
		state := s.Faint
		switch e.State {
		case "met":
			state = s.Success
		case "running":
			state = s.Info
		case "partial":
			state = s.Warning
		}
		facts := []string{fmt.Sprintf("rev %d", e.Rev)}
		if e.Frozen > 0 {
			facts[0] += fmt.Sprintf(" (run froze rev %d)", e.Frozen)
		}
		switch len(e.Runs) {
		case 0:
		case 1:
			facts = append(facts, fmt.Sprintf("%s · run %d", e.Runs[0].Recipe, e.Runs[0].N))
		default:
			facts = append(facts, fmt.Sprintf("%d runs", len(e.Runs)))
		}
		if e.DoneWhen > 0 {
			facts = append(facts, fmt.Sprintf("DW %d/%d", e.Met, e.DoneWhen))
		} else {
			facts = append(facts, "no done-when yet")
		}
		row := lead + s.Faint.Render(fmt.Sprintf("S%d ", e.N)) + title.Render(padTo(sanitize(e.Title), 34)) + " " +
			state.Render(padTo(e.State, 8)) + " " + s.Faint.Render(strings.Join(facts, " · "))
		out = append(out, ansi.Truncate(row, max(w, 8), "…"))
	}
	if len(sv.specs) > 1 {
		out = append(out, s.Faint.Render("  [ ] switch spec"))
	}
	return append(out, specRunLines(m, sv, w)...)
}

// specRunsShown is how many of a spec's runs its view lists, newest
// first: one digit key each.
const specRunsShown = 9

// specRuns are the open spec's runs, newest first, as many as the view
// lists.
func (sv *specView) specRuns() []webapi.SpecRun {
	var out []webapi.SpecRun
	for _, e := range sv.specs {
		if e.N != sv.n {
			continue
		}
		for i := len(e.Runs) - 1; i >= 0 && len(out) < specRunsShown; i-- {
			out = append(out, e.Runs[i])
		}
	}
	return out
}

// specRunLines lists the open spec's runs, newest first (§25.9): the
// digit that jumps to its block in the thread, its glyph, recipe and run,
// its state, its checked items met, its spend and when it ended.
func specRunLines(m *Shell, sv *specView, w int) []string {
	runs := sv.specRuns()
	if len(runs) == 0 {
		return nil
	}
	s := m.styles
	out := []string{s.Subtle.Render(fmt.Sprintf("  runs of S%d", sv.n)) + s.Faint.Render(" · a digit jumps to one in the thread")}
	for i, r := range runs {
		mark := r.Mark
		if mark == "" {
			mark = "·"
		}
		facts := []string{fmt.Sprintf("%s · run %d", r.Recipe, r.N), r.State}
		if r.Checked > 0 {
			facts = append(facts, fmt.Sprintf("met %d/%d", r.Met, r.Checked))
		}
		facts = append(facts, domain.FormatDollars(r.Spent))
		if !r.Ended.IsZero() {
			facts = append(facts, "ended "+endedWhen(r.Ended, m.now()))
		}
		row := "  " + s.KeyHint.Render(strconv.Itoa(i+1)) + " " + specRunStyle(s, r.State).Render(mark) + " " + s.Subtle.Render(sanitize(strings.Join(facts, " · ")))
		out = append(out, ansi.Truncate(row, max(w, 8), "…"))
	}
	return out
}

// specRunStyle colours a listed run's glyph by its state.
func specRunStyle(s *theme.Styles, state string) lipgloss.Style {
	switch domain.RunState(state) {
	case domain.RunDone:
		return s.Success
	case domain.RunPartial:
		return s.Warning
	case domain.RunRunning, domain.RunPaused:
		return s.Info
	}
	return s.Faint
}

// endedWhen is when a run ended, as a list says it: the time today, else
// the day and the time.
func endedWhen(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if y, mo, d := t.Date(); y == now.Year() && mo == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// jumpToRun leaves the spec for the thread and jumps to run n's block, as
// alt+w does, unfolded whole: the run a spec's list row names.
func (m *Shell) jumpToRun(n int) tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	var cmd tea.Cmd
	m.spec, m.diff, m.stats, m.logv = nil, nil, nil, nil
	if !m.cardOpen {
		cmd = m.openCard()
	}
	if m.wf.folds == nil {
		m.wf.folds = map[string]bool{}
	}
	m.wf.folds[runFoldKey(r.F.ID, n)] = false
	m.wf.focus, m.wf.cursor, m.wf.jump = true, runTarget{n: n}, true
	m.threadScroll = 0
	return cmd
}

// reviewerMarker returns the last unresolved `@reviewer` marker in a
// thread, or nil. It mirrors userMarker (spec.UnresolvedUserMarker) for
// the one other role that must never be lumped in with plain agent
// scaffolding: a critique finding does not gate on its own, but calling
// it "informational" — the word that tells a reader they may skip
// something — let a card approve past two rounds of escalated blocking
// findings with the gate none the wiser (see renderStatus).
func reviewerMarker(t spec.Thread) *spec.Marker {
	var found *spec.Marker
	for i := range t.Markers {
		if t.Markers[i].Author == "reviewer" && !t.Markers[i].Resolved {
			found = &t.Markers[i]
		}
	}
	return found
}

// agentAnsweredOpenUser reports whether an agent wrote its own resolution
// marker after a thread's still-open @user comment. Only a @user
// resolution can close a @user marker (Doc.Threads) — an agent resolving
// its own reviewer thread on a shared anchor used to silently close a
// human's untouched comment above it too, which is the hole that fix
// closed. The correct fix leaves the human's comment open until the human
// answers it, but it also means an agent that DID address the comment (it
// just cannot say "resolved" and have that count) leaves no visible trace
// that anything happened — the open marker looks identical whether the
// agent read it and replied, or never saw it. Scanning for the agent's
// own resolution after the human's marker in document order recovers
// that difference so the surface can say which one this is.
func agentAnsweredOpenUser(t spec.Thread) bool {
	um := userMarker(t)
	if um == nil {
		return false
	}
	after := false
	for _, mk := range t.Markers {
		if after && mk.Author != "user" && mk.Resolved {
			return true
		}
		if mk.Line == um.Line {
			after = true
		}
	}
	return false
}

// blockingAnswered reports whether every currently open, gate-blocking
// @user thread already has an agent's answer behind it (agentAnsweredOpenUser)
// — the state that makes R's own advice wrong: sending the same comments
// to the agent again would spend a full rework round on threads it has
// already addressed, because nothing but the human's own x can close
// them now. False on a card with no open blocking threads at all — there
// is nothing to have answered.
func (sv *specView) blockingAnswered() bool {
	blocking := sv.doc.UserOpenThreads()
	if len(blocking) == 0 {
		return false
	}
	for _, t := range blocking {
		if !agentAnsweredOpenUser(t) {
			return false
		}
	}
	return true
}

// needsAttentionCount is the headline's open-thread count: threads that
// actually need a person — a human's own comment, or a reviewer finding
// that has to be weighed before approving. spec.Doc.OpenQuestions also
// counts the template's own `%% @gummi: …` scaffolding prompts (every
// blank section of a fresh draft carries one), which are real open
// threads by the file's grammar but not a problem anyone caused: on a
// card where nothing has run yet, those prompts are the whole document,
// and folding them into the headline made a brand-new card read as
// already having several open issues. They still render, under "agent
// notes (non-blocking)" in renderStatus — this only keeps them out of the
// number a reader takes as "things I or a gate are waiting on".
func (sv *specView) needsAttentionCount() int {
	n := 0
	for _, t := range sv.doc.OpenQuestions() {
		if userMarker(t) != nil || reviewerMarker(t) != nil {
			n++
		}
	}
	return n
}

// renderStatus renders the fixed header: live dependency status, then
// the open threads split into three groups by who authored them. From the
// plan stage on, an unresolved @user comment blocks the approval gate
// (DESIGN §6.1) — the gate math counts only those threads, so a reader
// must be able to tell which group that is. At todo the same group is
// authored by the user but does NOT block anything: engine.Advance
// itself does not gate the todo→plan edge on comments (leaving todo is
// not a gate — nothing has run yet for a comment to object to), so the
// group's heading has to say something true at that stage instead of
// "blocks approval" with no approval pending. The other two groups are
// both agent-authored and neither ever gates, but they are not the same
// thing: a @reviewer finding is the critique's own verdict on the
// artifact and needs weighing before approving, while an
// @architect/@gummi thread (template prompts, notes) is ordinary
// scaffolding. Grouping by the marker's role rather than by scanning
// finding text for words like "blocking" is what keeps this honest — free
// text a reviewer writes is not a contract gummi can pattern-match
// without eventually mislabeling a finding that happens not to start with
// the expected word.
func (sv *specView) renderStatus(m *Shell, w int) string {
	s := m.styles
	var b strings.Builder

	var blocking, reviewed, agentNotes []spec.Thread
	for _, t := range sv.doc.OpenQuestions() {
		switch {
		case userMarker(t) != nil:
			blocking = append(blocking, t)
		case reviewerMarker(t) != nil:
			reviewed = append(reviewed, t)
		default:
			agentNotes = append(agentNotes, t)
		}
	}
	renderThreadGroup := func(label string, threads []spec.Thread, marker func(spec.Thread) *spec.Marker) {
		if len(threads) == 0 {
			return
		}
		b.WriteString(s.Subtitle.Render(label) + "\n")
		for _, t := range threads {
			mk := t.Markers[0]
			if marker != nil {
				if found := marker(t); found != nil {
					mk = *found
				}
			}
			// The line reference is reserved out of the width BEFORE the
			// text is truncated into it. It used to be appended after a
			// truncate to w-8, making the row w+2 wide, and the pane then
			// clipped the tail — which is the one part of the row with no
			// ellipsis to admit it had been cut, so "L105" rendered as
			// "L10" and sent the reader ninety-five lines wrong (round 3
			// §3.2; the same row read correctly at 160 columns, which is
			// what made it look like a content problem).
			ref := "  L" + strconv.Itoa(mk.Line)
			textW := max(w-8-ansi.StringWidth(ref), 4)
			b.WriteString(s.Warning.Render("  ☐ ") + s.Subtle.Render(ansi.Truncate(mk.Text, textW, "…")) +
				s.Faint.Render(ref) + "\n")
		}
		b.WriteString("\n")
	}
	// "blocks approval" is only true from plan on — see renderStatus's own
	// doc comment. At todo it is read by the next run instead, so the
	// label has to swap with the stage rather than being pinned to the one
	// wording that happens to be right most of the time.
	blockingLabel := "blocks approval (you)"
	if sv.f.Stage == domain.StageTodo {
		blockingLabel = "read by the next run (you)"
	}
	renderThreadGroup(blockingLabel, blocking, userMarker)
	renderThreadGroup("reviewer findings — weigh before approving", reviewed, reviewerMarker)
	// "prompts", not "agent notes (non-blocking)". On a fresh card this
	// group is the TEMPLATE's own unanswered questions — "exact steps to
	// reproduce", "what should happen, vs what actually happens?" — and
	// heading them "notes" over a column of empty checkboxes made a
	// brand-new bug report open looking like seven unfinished tasks
	// somebody else had left behind (round 3 §5.6). "non-blocking" is the
	// gate's vocabulary; what the reader needs to know is that nothing is
	// waiting on them.
	renderThreadGroup("prompts the stages will answer — nothing waiting on you", agentNotes, nil)
	return b.String()
}

// renderSource renders the document: line numbers, gutter markers on
// annotated lines, tinted %% marker lines, in-place markdown styling for
// everything else (mdsource.go), and the line cursor.
func (sv *specView) renderSource(m *Shell, w, h int) string {
	s := m.styles
	anchored := map[int]bool{}
	for _, mk := range sv.doc.Markers {
		if mk.Anchor > 0 {
			anchored[mk.Anchor] = true
		}
	}
	resolvedLine := map[int]bool{}
	for _, t := range sv.doc.Threads() {
		for _, mk := range t.Markers {
			resolvedLine[mk.Line] = t.Resolved
		}
	}

	total := len(sv.doc.Lines)
	numW := len(strconv.Itoa(total))
	textW := max(w-numW-3, 4)

	// Lines wider than the pane wrap into continuation rows (line number
	// and gutter only on the first), so the window and cursor centering
	// work in display rows rather than source lines.
	type row struct {
		n       int    // 1-based source line
		content string // styled segment
		first   bool   // first row of its source line
	}
	var rows []row
	cursorRow := 0
	// one styler for the whole document: fenced-block state is carried
	// line to line, so a ``` block has to be walked in order.
	var md mdSource
	for i, raw := range sv.doc.Lines {
		n := i + 1
		var segs []string
		if spec.IsMarkerLine(raw) {
			// a %% thread is gummi's own annotation, not the document's
			// prose: it keeps its status color and its indent, and never
			// goes through the markdown styler.
			style := s.Warning
			if resolvedLine[n] {
				style = s.Success
			}
			segs = strings.Split(ansi.Wrap(strings.TrimSpace(raw), max(textW-2, 4), ""), "\n")
			for j := range segs {
				segs[j] = "  " + style.Render(segs[j])
			}
			// the styler still has to see the line, or a %% marker inside
			// a fenced block would be read as prose on the way past.
			md.line(s, raw)
		} else {
			segs = wrapStyled(md.line(s, raw), textW)
		}
		if n == sv.cursor {
			cursorRow = len(rows)
		}
		for j, seg := range segs {
			rows = append(rows, row{n: n, content: seg, first: j == 0})
		}
	}

	visible := max(h, 3)
	// the window is derived purely from the cursor (render must not
	// mutate state): keep the cursor centered where possible
	off := min(max(cursorRow-(visible-1)/2, 0), max(len(rows)-visible, 0))

	var b strings.Builder
	end := min(off+visible, len(rows))
	for i := off; i < end; i++ {
		r := rows[i]
		num := strings.Repeat(" ", numW)
		gutter := " "
		if r.first {
			num = fmt.Sprintf("%*d", numW, r.n)
			if anchored[r.n] {
				gutter = s.Warning.Render("▍")
			}
		}
		lineStr := s.Faint.Render(num) + gutter + " " + r.content
		if r.n == sv.cursor && r.first {
			lineStr = s.Selection.Render(num) + gutter + " " + r.content
			lineStr = s.Cursor.Render("▸") + lineStr
		} else {
			lineStr = " " + lineStr
		}
		b.WriteString(lineStr)
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// firstBlockingComment reports the line of the first open @user comment in
// sv's artifact — the group renderStatus heads "blocks approval (you)" —
// so the surface can open where the blocker is rather than where the stage
// is. Absent before plan, where an open user comment blocks nothing
// (renderStatus's own rule, and the reason its heading swaps there): at
// todo the comment is a note for the next run, not something to clear.
//
// It reads the threads in document order, which is the order the panel
// lists them in, so "the first one" means the same thing in both places.
func firstBlockingComment(sv *specView) (int, bool) {
	if sv == nil || sv.f.Stage == domain.StageTodo {
		return 0, false
	}
	for _, t := range sv.doc.OpenQuestions() {
		if mk := userMarker(t); mk != nil {
			return mk.Line, true
		}
	}
	return 0, false
}

// lineText is the artifact's 1-based line n, trimmed, or "" when n is out
// of range — what the comment dialog shows as the anchor it will attach
// to. Trimmed because the dialog has one short row for it and a wrapped
// table row's leading whitespace would spend most of it.
func (sv *specView) lineText(n int) string {
	if sv == nil || n < 1 || n > len(sv.doc.Lines) {
		return ""
	}
	return strings.TrimSpace(sv.doc.Lines[n-1])
}
