package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/branchlog"
	"github.com/morphis/gummi/internal/diffannot"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// The log tab: the card's own commits, oldest first, and the one thing
// that can be done to them — reword a commit, or squash it into the one
// before. Both keep the branch's content exactly as it was, so the verify
// that ran on it and the comments on its diff stay true; reordering and
// dropping are not offered because either would change what was verified.
//
// It reads branchlog, the same fold as the web page's Log tab and
// `gummi log`, and refuses for branchlog.Refusal's reasons, so the two
// faces cannot disagree about whether a card's history is still the
// card's to change. Edits are a draft this surface holds; nothing moves
// until a is pressed and the confirm answered. gummi never pushes: a
// rewrite of pushed commits ends with the force push printed.

// logView is the mounted log tab.
type logView struct {
	f      domain.Feature
	log    branchlog.Log
	cursor int
	scroll int
	// the draft: commits folded into the one before, and new messages by
	// the first commit of their group
	squash map[string]bool
	msg    map[string]string
	// open holds the commits whose changes are drawn under them, and
	// patches what was read for each
	open    map[string]bool
	patches map[string][]string
}

type logLoadedMsg struct {
	f   domain.Feature
	log branchlog.Log
	err error
}

type logPatchMsg struct {
	id    domain.FeatureID
	sha   string
	lines []string
	err   error
}

// logRewrittenMsg reports a rewrite that ran (or was refused under the
// lock), and reloads the tab.
type logRewrittenMsg struct {
	f      domain.Feature
	notice noticeMsg
}

// logBusy is whether an agent has the card: a stage session, a freeform
// turn, or a pass the board itself is running on it.
func (m *Shell) logBusy(f domain.Feature) bool {
	if m.webCardBusy(f.ID) {
		return true
	}
	if r, ok := m.rowByID(f.ID); ok {
		return m.cardBusy(r)
	}
	return false
}

func (m *Shell) logEnv() branchlog.Env { return branchlog.Env{Store: m.store, Pool: m.wt} }

// openLog reads the card's log and mounts the tab.
func (m *Shell) openLog(f domain.Feature) tea.Cmd {
	env, busy := m.logEnv(), m.logBusy(f)
	return func() tea.Msg {
		if env.Store == nil || env.Pool == nil {
			return logLoadedMsg{f: f, err: errors.New("no workspace to read the log from")}
		}
		ctx := context.Background()
		if fresh, err := env.Store.GetFeature(ctx, f.ID); err == nil {
			f = fresh
		}
		l, err := env.Read(ctx, f, busy)
		return logLoadedMsg{f: f, log: l, err: err}
	}
}

func (m *Shell) logLoaded(msg logLoadedMsg) tea.Cmd { //nolint:unparam // a message handler: every handler returns the follow-up command
	if msg.err != nil {
		m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	lv := &logView{
		f: msg.f, log: msg.log, squash: map[string]bool{}, msg: map[string]string{},
		open: map[string]bool{}, patches: map[string][]string{},
	}
	if old := m.logv; old != nil && old.f.ID == msg.f.ID {
		// a reload keeps the place, and a draft still made of these very
		// commits; a draft of commits that are gone is dropped with them
		lv.cursor, lv.scroll = old.cursor, old.scroll
		if old.head() == lv.head() {
			lv.squash, lv.msg, lv.open, lv.patches = old.squash, old.msg, old.open, old.patches
		}
	}
	lv.cursor = min(lv.cursor, max(len(lv.log.Rows)-1, 0))
	m.logv = lv
	return nil
}

func (lv *logView) head() string {
	if n := len(lv.log.Rows); n > 0 {
		return lv.log.Rows[n-1].SHA
	}
	return ""
}

func (lv *logView) dirty() bool { return len(lv.squash) > 0 || len(lv.msg) > 0 }

func (lv *logView) rewritable() bool { return lv.log.Why == "" && len(lv.log.Rows) > 0 }

// lead is the index of the first commit of the group row i belongs to.
func (lv *logView) lead(i int) int {
	for i > 0 && lv.squash[lv.log.Rows[i].SHA] {
		i--
	}
	return i
}

// groupSize is how many commits the group led by row i holds.
func (lv *logView) groupSize(i int) int {
	n := 1
	for j := i + 1; j < len(lv.log.Rows) && lv.squash[lv.log.Rows[j].SHA]; j++ {
		n++
	}
	return n
}

func (lv *logView) plan() worktree.RewritePlan {
	return worktree.RewritePlan{Head: lv.head(), Groups: branchlog.PlanGroups(lv.log.Rows, lv.squash, lv.msg)}
}

func (lv *logView) bindings() []binding {
	bs := []binding{{key: "j/k", label: "move", help: "move between commits"}}
	bs = append(bs, binding{key: "enter", label: "changes", help: "show or hide what the commit changed", bar: true})
	if lv.rewritable() {
		bs = append(bs,
			binding{key: "e", label: "reword", help: "edit the message of the commit (or of the squashed group it leads)", bar: true},
			binding{key: "s", label: "squash", help: "fold the commit into the one before it, or unfold it", bar: true},
		)
		if lv.dirty() {
			bs = append(bs,
				binding{key: "a", label: "apply", help: "rewrite the branch to the draft — its content stays exactly the same", bar: true, sticky: true},
				binding{key: "u", label: "undo all", help: "drop the draft"},
			)
		}
	}
	return append(bs,
		binding{key: "r", label: "reload", help: "read the branch again"},
		binding{key: "?", label: "help", bar: true},
		binding{key: "esc", label: "back", help: "back to the thread", bar: true},
	)
}

func (m *Shell) handleLogKey(key string) tea.Cmd {
	lv := m.logv
	rows := lv.log.Rows
	switch key {
	case "esc", "q":
		m.logv = nil
	case "j", "down":
		if lv.cursor < len(rows)-1 {
			lv.cursor++
		}
	case "k", "up":
		if lv.cursor > 0 {
			lv.cursor--
		}
	case "g", "home":
		lv.cursor = 0
	case "G", "end":
		lv.cursor = max(len(rows)-1, 0)
	case "r":
		return m.openLog(lv.f)
	case "enter", "space":
		if len(rows) == 0 {
			return nil
		}
		sha := rows[lv.cursor].SHA
		if lv.open[sha] {
			delete(lv.open, sha)
			return nil
		}
		lv.open[sha] = true
		if _, ok := lv.patches[sha]; ok {
			return nil
		}
		return m.loadLogPatch(lv.f, sha)
	case "s":
		if !lv.rewritable() {
			return m.logRefused()
		}
		if lv.cursor == 0 {
			m.notice = noticeMsg{text: "the first commit has no commit before it to squash into"}
			return nil
		}
		sha := rows[lv.cursor].SHA
		if lv.squash[sha] {
			delete(lv.squash, sha)
		} else {
			lv.squash[sha] = true
			// a message written for the commit as its own group no longer
			// leads anything
			delete(lv.msg, sha)
		}
	case "e":
		if !lv.rewritable() {
			return m.logRefused()
		}
		if len(rows) == 0 {
			return nil
		}
		m.Overlay.Push(m.newRewordDialog(lv, lv.lead(lv.cursor)))
	case "u":
		lv.squash, lv.msg = map[string]bool{}, map[string]string{}
	case "a":
		if !lv.rewritable() {
			return m.logRefused()
		}
		if !lv.dirty() {
			m.notice = noticeMsg{text: "nothing to rewrite — reword a commit (e) or squash one (s) first"}
			return nil
		}
		return m.prepareRewrite(lv.f, lv.plan())
	}
	return nil
}

func (m *Shell) logRefused() tea.Cmd {
	why := m.logv.log.Why
	if why == "" {
		why = string(m.logv.f.ID) + " has no commits to rewrite"
	}
	m.notice = noticeMsg{text: why}
	return nil
}

func (m *Shell) loadLogPatch(f domain.Feature, sha string) tea.Cmd {
	env := m.logEnv()
	return func() tea.Msg {
		raw, err := env.CommitDiff(context.Background(), f, sha)
		return logPatchMsg{id: f.ID, sha: sha, lines: diffannot.Lines(raw), err: err}
	}
}

func (m *Shell) logPatchLoaded(msg logPatchMsg) tea.Cmd { //nolint:unparam // a message handler: every handler returns the follow-up command
	lv := m.logv
	if lv == nil || lv.f.ID != msg.id {
		return nil
	}
	if msg.err != nil {
		delete(lv.open, msg.sha)
		m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	lv.patches[msg.sha] = msg.lines
	return nil
}

// logPreparedMsg is a plan the dry run accepted, waiting for its yes.
type logPreparedMsg struct {
	f       domain.Feature
	plan    worktree.RewritePlan
	preview worktree.RewritePreview
	push    string
	err     error
}

// prepareRewrite dry-runs the draft off the loop, so the confirm can say
// what it will do and whether the remote will need a force push.
func (m *Shell) prepareRewrite(f domain.Feature, plan worktree.RewritePlan) tea.Cmd {
	env, busy := m.logEnv(), m.logBusy(f)
	push := m.logv.log.PushCommand
	return func() tea.Msg {
		prev, err := env.Plan(context.Background(), f, busy, plan)
		return logPreparedMsg{f: f, plan: plan, preview: prev, push: push, err: err}
	}
}

func (m *Shell) logPrepared(msg logPreparedMsg) tea.Cmd { //nolint:unparam // a message handler: every handler returns the follow-up command
	if msg.err != nil {
		m.notice = noticeMsg{text: string(msg.f.ID) + " rewrite refused: " + sanitize(msg.err.Error()), isErr: true}
		return nil
	}
	if msg.preview.Noop {
		m.notice = noticeMsg{text: string(msg.f.ID) + ": the draft leaves the branch as it is"}
		return nil
	}
	before := len(m.logv.log.Rows)
	detail := fmt.Sprintf("%d commit%s → %d · the branch's content stays exactly the same", before, plural(before), len(msg.preview.Entries))
	label := "Rewrite"
	if msg.preview.Pushed {
		detail += "\nthe remote already has commits this replaces — gummi will not push; afterwards run:\n  " + msg.push
		label = "Rewrite anyway"
	}
	f, plan, pushed := msg.f, msg.plan, msg.preview.Pushed
	m.Overlay.Push(&confirmDialog{
		card:         f.ID,
		id:           "confirm-rewrite",
		question:     "rewrite " + string(f.ID) + "'s history?",
		detail:       detail,
		confirmLabel: label,
		onConfirm:    func() tea.Cmd { return m.rewriteFeature(f, plan, pushed) },
	})
	return nil
}

// rewriteFeature applies plan under the card's lock, re-checking every
// refusal there: the dry run was off the lock, and an agent may have
// started since.
func (m *Shell) rewriteFeature(f domain.Feature, plan worktree.RewritePlan, acknowledgePushed bool) tea.Cmd {
	env := m.logEnv()
	busy := m.logBusy(f)
	return m.cardLocked(f.ID, func() tea.Msg {
		tip, push, err := env.Apply(context.Background(), f, busy, plan, acknowledgePushed)
		switch {
		case err != nil:
			return logRewrittenMsg{f: f, notice: noticeMsg{text: string(f.ID) + " rewrite failed: " + sanitize(err.Error()), isErr: true}}
		case tip == "":
			return logRewrittenMsg{f: f, notice: noticeMsg{text: string(f.ID) + ": nothing to rewrite"}}
		}
		text := string(f.ID) + " history rewritten to " + shortSHA(tip) + " — content unchanged"
		if push != "" {
			text += "\n  " + push
		}
		return logRewrittenMsg{f: f, notice: noticeMsg{text: text, reload: true}}
	})
}

func (m *Shell) logRewritten(msg logRewrittenMsg) tea.Cmd {
	m.notice = msg.notice
	if m.logv != nil && m.logv.f.ID == msg.f.ID && !msg.notice.isErr {
		m.logv.squash, m.logv.msg = map[string]bool{}, map[string]string{}
	}
	cmds := []tea.Cmd{m.loadRows}
	if m.logv != nil && m.logv.f.ID == msg.f.ID {
		cmds = append(cmds, m.openLog(msg.f))
	}
	return tea.Batch(cmds...)
}

// logViewRender draws the log, the window following the cursor.
func (m *Shell) logViewRender(w, h int) string {
	lines, at := m.logv.lines(m, w)
	lv := m.logv
	if at < lv.scroll {
		lv.scroll = at
	}
	if at >= lv.scroll+h {
		lv.scroll = at - h + 1
	}
	lv.scroll = max(0, min(lv.scroll, len(lines)-1))
	end := min(len(lines), lv.scroll+h)
	return strings.Join(lines[lv.scroll:end], "\n")
}

// lines renders the whole surface and reports the line the cursor is on.
func (lv *logView) lines(m *Shell, w int) ([]string, int) {
	s := m.styles
	var out []string
	add := func(line string) { out = append(out, ansi.Truncate(line, max(w, 10), "…")) }
	rows := lv.log.Rows
	head := fmt.Sprintf(" %d commit%s", len(rows), plural(len(rows)))
	add(head + s.Muted.Render("  oldest first"))
	if lv.log.Why != "" {
		add(" " + s.Muted.Render(lv.log.Why))
	}
	at := 0
	if len(rows) == 0 {
		return out, 0
	}
	add("")
	for i, r := range rows {
		folded := i > 0 && lv.squash[r.SHA]
		marker := "  "
		if i == lv.cursor {
			marker = s.Cursor.Render("▸") + " "
			at = len(out)
		}
		indent := ""
		if folded {
			indent = s.Faint.Render("└ ")
		}
		subject := r.Subject
		if msg, ok := lv.msg[r.SHA]; ok {
			subject, _, _ = strings.Cut(msg, "\n")
			subject += " " + s.Warning.Render("✎")
		} else if !folded && lv.groupSize(i) > 1 {
			subject += " " + s.Warning.Render(fmt.Sprintf("+%d", lv.groupSize(i)-1))
		}
		line := marker + indent + s.Faint.Render(r.Short) + "  " + subject
		var tags []string
		if r.Checkpoint {
			tags = append(tags, s.Faint.Render("checkpoint"))
		}
		if r.Pushed {
			tags = append(tags, s.Warning.Render("pushed"))
		}
		if r.Warning != "" {
			tags = append(tags, s.Error.Render("attribution"))
		}
		if len(tags) > 0 {
			line += "  " + strings.Join(tags, " ")
		}
		add(line)
		meta := fmt.Sprintf("%s · %s · %d file%s ", r.Author, r.At.Local().Format("Jan 2 15:04"), r.Files, plural(r.Files))
		stat := s.Success.Render(fmt.Sprintf("+%d", r.Add)) + " " + s.Error.Render(fmt.Sprintf("−%d", r.Del))
		pad := "    "
		if folded {
			pad = "      "
			meta = "squashed into " + rows[lv.lead(i)].Short + " · " + meta
		}
		add(pad + s.Muted.Render(meta) + stat)
		if lv.open[r.SHA] {
			patch, ok := lv.patches[r.SHA]
			switch {
			case !ok:
				add(pad + s.Faint.Render("reading…"))
			case len(patch) == 0:
				add(pad + s.Faint.Render("this commit changes no files"))
			}
			for _, pl := range patch {
				add(pad + diffLineStyle(m, sanitize(pl)))
			}
			add("")
		}
	}
	if lv.dirty() {
		add("")
		add(" " + s.Warning.Render("draft") + s.Muted.Render(" · a applies it · u drops it · the branch's content stays exactly the same"))
	}
	return out, at
}

// logHasBranch is whether the tab has a log to show: the same test the
// diff tab makes, and for the same reason — no branch, no commits.
func logHasBranch(r featureRow) bool { return cardHasDiff(r) && r.F.Kind != domain.KindGoal }

// ---------------------------------------------------------------- reword

// rewordDialog edits one commit message — or the message of a squashed
// group, which the commit leading it carries. Multi-line, so enter is a
// newline and ctrl+s saves, as the landing message's dialog does.
type rewordDialog struct {
	short   string
	group   int
	input   textarea.Model
	errText string
	save    func(string)
}

func (m *Shell) newRewordDialog(lv *logView, i int) *rewordDialog {
	r := lv.log.Rows[i]
	in := textarea.New()
	in.ShowLineNumbers = false
	in.CharLimit = 4000
	in.SetWidth(64)
	in.SetHeight(8)
	cur, ok := lv.msg[r.SHA]
	if !ok {
		cur = r.Subject
		if r.Body != "" {
			cur += "\n\n" + r.Body
		}
	}
	in.SetValue(cur)
	in.Focus()
	original := strings.TrimSpace(cur)
	return &rewordDialog{
		short: r.Short, group: lv.groupSize(i), input: in,
		save: func(v string) {
			// saving the commit's own message back unchanged is no edit
			if v == original && !ok && lv.groupSize(i) == 1 {
				delete(lv.msg, r.SHA)
				return
			}
			lv.msg[r.SHA] = v
		},
	}
}

func (d *rewordDialog) ID() string { return "reword" }

func (d *rewordDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "ctrl+s":
		v := strings.TrimSpace(d.input.Value())
		if v == "" {
			d.errText = "a commit needs a message"
			return false, nil
		}
		if hit := worktree.MatchesAttribution(v); hit != "" {
			d.errText = "the message carries agent attribution (" + hit + ") — a rewrite refuses it"
			return false, nil
		}
		d.save(v)
		return true, nil
	}
	d.input, _ = d.input.Update(key)
	d.errText = ""
	return false, nil
}

func (d *rewordDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	d.input, _ = d.input.Update(msg)
	d.errText = ""
	return nil
}

func (d *rewordDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	title := "reword " + d.short
	if d.group > 1 {
		title = fmt.Sprintf("message for %s and the %d commit%s squashed into it", d.short, d.group-1, plural(d.group-1))
	}
	b.WriteString(s.DialogTitle.Render(title) + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	b.WriteString("\n" + s.Faint.Render("ctrl+s keep · esc cancel · nothing is rewritten until a"))
	return s.DialogFrame.Render(b.String())
}
