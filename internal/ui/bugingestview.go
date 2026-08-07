package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// bugIngest form fields, in tab order.
const (
	bugIngestFieldRepo = iota
	bugIngestFieldLabel
	bugIngestFieldProfile
	bugIngestFieldComments
	bugIngestFieldCount
)

// bugIngestForm collects the GitHub target for a bug import: the repo
// (blank = this repo's origin remote), a label filter, the profile the
// new bugs adopt, and whether to fetch each issue's comments. State
// defaults to open (the CLI exposes the rest).
type bugIngestForm struct {
	repo     textinput.Model
	label    textinput.Model
	profiles []string
	profile  int
	comments bool
	focus    int

	onSubmit func(repo, label, profile string, comments bool) tea.Cmd
}

func newBugIngestForm(profiles []string, onSubmit func(repo, label, profile string, comments bool) tea.Cmd) *bugIngestForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	repo := textinput.New()
	repo.Placeholder = "owner/repo (blank = origin)"
	repo.CharLimit = 140
	repo.SetWidth(46)
	repo.Focus()
	label := textinput.New()
	label.Placeholder = "label filter"
	label.CharLimit = 60
	label.SetWidth(46)
	label.SetValue("bug")
	return &bugIngestForm{repo: repo, label: label, profiles: profiles, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *bugIngestForm) ID() string { return "ingest-bugs" }

// HandleKey implements overlay.Dialog.
func (d *bugIngestForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		return true, d.onSubmit(strings.TrimSpace(d.repo.Value()), strings.TrimSpace(d.label.Value()), d.profiles[d.profile], d.comments)
	case "tab", "down":
		d.setFocus((d.focus + 1) % bugIngestFieldCount)
		return false, nil
	case "shift+tab", "up":
		d.setFocus((d.focus + bugIngestFieldCount - 1) % bugIngestFieldCount)
		return false, nil
	}
	switch d.focus {
	case bugIngestFieldProfile:
		switch key.String() {
		case "left", "h":
			d.profile = (d.profile + len(d.profiles) - 1) % len(d.profiles)
		case "right", "l", "space":
			d.profile = (d.profile + 1) % len(d.profiles)
		}
	case bugIngestFieldComments:
		if key.String() == "c" || key.String() == "space" {
			d.comments = !d.comments
		}
	case bugIngestFieldRepo:
		d.repo, _ = d.repo.Update(key)
	case bugIngestFieldLabel:
		d.label, _ = d.label.Update(key)
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text goes into the
// focused field (repo or label).
func (d *bugIngestForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	switch d.focus {
	case bugIngestFieldRepo:
		d.repo, _ = d.repo.Update(msg)
	case bugIngestFieldLabel:
		d.label, _ = d.label.Update(msg)
	}
	return nil
}

func (d *bugIngestForm) setFocus(f int) {
	d.focus = f
	d.repo.Blur()
	d.label.Blur()
	switch f {
	case bugIngestFieldRepo:
		d.repo.Focus()
	case bugIngestFieldLabel:
		d.label.Focus()
	}
}

// View implements overlay.Dialog.
func (d *bugIngestForm) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("import github bugs") + "\n\n")
	b.WriteString(d.repo.View() + "\n")
	b.WriteString(d.label.View() + "\n\n")

	marker := "  "
	profile := s.Faint.Render(d.profiles[d.profile])
	if d.focus == bugIngestFieldProfile {
		marker = s.Cursor.Render("▸ ")
		profile = s.Subtle.Render(d.profiles[d.profile])
	}
	b.WriteString(marker + profile + "\n")

	box := "[ ]"
	if d.comments {
		box = "[x]"
	}
	cmMarker := "  "
	comments := s.Faint.Render(box + " Fetch comments")
	if d.focus == bugIngestFieldComments {
		cmMarker = s.Cursor.Render("▸ ")
		comments = s.Subtle.Render(box + " Fetch comments")
	}
	b.WriteString(cmMarker + comments + "\n")

	hint := "enter import · tab next · esc cancel"
	switch d.focus {
	case bugIngestFieldProfile:
		hint = "←/→ profile · enter import · esc cancel"
	case bugIngestFieldComments:
		hint = "c toggle comments · enter import · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(hint))
	return s.DialogFrame.Render(b.String())
}

// bugIngestView is the bug-import review surface (DESIGN §11.4 phase B,
// bug variant): the GitHub issues fetched as proposals, reviewed and
// approved before any are minted. Unlike feature ingest there is no
// coverage map and no merge — issues are discrete — so the gate only
// coarsens (drop) and edits (rename/one-liner).
type bugIngestView struct {
	source   string
	props    []bugIngestProposal
	skipped  []domain.FeatureID
	cursor   int // index into the filtered (visible) list
	profile  string
	envelope int

	filter    textinput.Model // live substring filter over the fetched issues
	filtering bool            // the filter input is focused (typing)
}

type bugIngestProposal struct {
	p       domain.BugProposal
	dropped bool
}

func newBugIngestView(res engine.BugIngestResult, profile string, envelope int) *bugIngestView {
	props := make([]bugIngestProposal, len(res.Proposals))
	for i, p := range res.Proposals {
		props[i] = bugIngestProposal{p: p}
	}
	filter := textinput.New()
	filter.Placeholder = "filter by title / label / text…"
	filter.CharLimit = 80
	filter.SetWidth(40)
	skipped := make([]domain.FeatureID, len(res.Skipped))
	for i, s := range res.Skipped {
		skipped[i] = s.LocalID
	}
	return &bugIngestView{source: res.Source, props: props, skipped: skipped, profile: profile, envelope: envelope, filter: filter}
}

// bugMatches reports whether a proposal matches the (already lowercased)
// filter query — an empty query matches everything. Matches on title,
// one-liner, external ref, and body so any of them can narrow the set.
func bugMatches(p domain.BugProposal, q string) bool {
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(p.Title), q) ||
		strings.Contains(strings.ToLower(p.OneLiner), q) ||
		strings.Contains(strings.ToLower(p.ExternalRef), q) ||
		strings.Contains(strings.ToLower(p.Report.Description), q)
}

// visible returns the indices (into props) of proposals matching the
// current filter, in list order. The cursor and every action index
// through this projection, so filtering both narrows the view and, at
// approval, the set that materializes.
func (bv *bugIngestView) visible() []int {
	q := strings.ToLower(strings.TrimSpace(bv.filter.Value()))
	var out []int
	for i, ip := range bv.props {
		if bugMatches(ip.p, q) {
			out = append(out, i)
		}
	}
	return out
}

// selected returns the props index under the cursor, or -1 when the
// filtered view is empty.
func (bv *bugIngestView) selected() int {
	vis := bv.visible()
	if len(vis) == 0 {
		return -1
	}
	bv.cursor = min(max(bv.cursor, 0), len(vis)-1)
	return vis[bv.cursor]
}

// active reports whether a filter query is applied (even when not
// currently editing it).
func (bv *bugIngestView) active() bool { return strings.TrimSpace(bv.filter.Value()) != "" }

// keptCount / kept count only proposals that are visible under the
// current filter AND not dropped: what you see (and haven't dropped) is
// what materializes, so the filter drives the import set.
func (bv *bugIngestView) keptCount() int {
	n := 0
	for _, i := range bv.visible() {
		if !bv.props[i].dropped {
			n++
		}
	}
	return n
}

func (bv *bugIngestView) kept() []domain.BugProposal {
	var out []domain.BugProposal
	for _, i := range bv.visible() {
		if !bv.props[i].dropped {
			out = append(out, bv.props[i].p)
		}
	}
	return out
}

func (bv *bugIngestView) setCursor(n int) {
	if vis := len(bv.visible()); vis > 0 {
		bv.cursor = min(max(n, 0), vis-1)
	} else {
		bv.cursor = 0
	}
}

// bindings is the bug-import surface's key table (see keymap.go),
// split by filter focus like handleBugIngestKey routes.
func (bv *bugIngestView) bindings() []binding {
	if bv.filtering {
		return []binding{
			{key: "type", label: "filter", help: "type to filter the list", bar: true},
			{key: "enter", label: "apply", help: "lock the filter in and return to the list", bar: true},
			{key: "esc", label: "clear", help: "clear the filter and return to the full list", bar: true},
		}
	}
	return []binding{
		{key: "j/k", label: "select", help: "move over the bugs"},
		{key: "pgup/pgdn", label: "page", help: "move by a page over the bugs"},
		{key: "/", label: "filter", bar: true},
		{key: "r", label: "rename", help: "rename the bug (also c)", bar: true},
		{key: "o", label: "one-liner", help: "edit the one-line summary", bar: true},
		{key: "x", label: "drop", help: "drop/undrop the bug", bar: true},
		{key: "A", label: "approve", help: "materialize the kept bugs into todo", bar: true},
		{key: "esc", label: "discard", help: "discard the import — nothing created (also q)", bar: true},
		{key: "?", label: "help", bar: true},
	}
}

// handleBugIngestKey routes keys while the bug-import review surface is
// open. While the filter is focused, keys type into it; otherwise they
// navigate and act on the filtered list.
func (m *Shell) handleBugIngestKey(msg tea.KeyPressMsg) tea.Cmd {
	bv := m.bugIngest
	key := msg.String()
	if bv.filtering {
		switch key {
		case "enter":
			// lock the filter in and return to navigation, keeping the query
			bv.filtering = false
			bv.filter.Blur()
		case "esc":
			// clear the filter and return to the full list
			bv.filter.SetValue("")
			bv.filtering = false
			bv.filter.Blur()
			bv.cursor = 0
		default:
			bv.filter, _ = bv.filter.Update(msg)
			bv.setCursor(bv.cursor) // reclamp: the visible set may have shrunk
		}
		return nil
	}
	switch key {
	case "esc", "q":
		m.bugIngest = nil
		m.notice = noticeMsg{text: "import discarded — nothing created"}
		return nil
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "/":
		bv.filtering = true
		bv.filter.Focus()
	case "j", "down":
		bv.setCursor(bv.cursor + 1)
	case "k", "up":
		bv.setCursor(bv.cursor - 1)
	case "pgdown":
		bv.setCursor(bv.cursor + m.mainPage())
	case "pgup":
		bv.setCursor(bv.cursor - m.mainPage())
	case "x":
		if i := bv.selected(); i >= 0 {
			bv.props[i].dropped = !bv.props[i].dropped
		}
	case "r", "c":
		bv.promptTitle(m)
	case "o":
		bv.promptOneLiner(m)
	case "A":
		return m.approveBugIngest()
	}
	return nil
}

func (bv *bugIngestView) promptTitle(m *Shell) {
	i := bv.selected()
	if i < 0 {
		return
	}
	m.Overlay.Push(newTextPrompt("rename bug", bv.props[i].p.Title, "bug title",
		func(s string) error { _, err := domain.Slugify(s); return err },
		func(s string) tea.Cmd { bv.props[i].p.Title = s; return nil }))
}

func (bv *bugIngestView) promptOneLiner(m *Shell) {
	i := bv.selected()
	if i < 0 {
		return
	}
	m.Overlay.Push(newTextPrompt("edit one-liner", bv.props[i].p.OneLiner, "one-line summary", nil,
		func(s string) tea.Cmd { bv.props[i].p.OneLiner = s; return nil }))
}

// approveBugIngest confirms, then materializes the kept proposals.
func (m *Shell) approveBugIngest() tea.Cmd {
	bv := m.bugIngest
	if bv == nil {
		return nil
	}
	n := bv.keptCount()
	if n == 0 {
		m.notice = noticeMsg{text: "every bug is dropped — nothing to create", isErr: true}
		return nil
	}
	m.Overlay.Push(&confirmDialog{
		id:        "confirm-bug-ingest",
		question:  fmt.Sprintf("materialize %d bug(s) into todo?", n),
		detail:    "from " + bv.source,
		onConfirm: m.materializeBugIngest,
	})
	return nil
}

func (m *Shell) materializeBugIngest() tea.Cmd {
	bv := m.bugIngest
	if bv == nil || m.engine == nil {
		return nil
	}
	eng := m.engine
	props := bv.kept()
	opts := engine.MaterializeOpts{Profile: bv.profile, Envelope: bv.envelope}
	m.bugIngest = nil
	return func() tea.Msg {
		created, err := eng.MaterializeBugs(context.Background(), props, opts)
		if err != nil {
			return noticeMsg{text: "import: " + sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("imported %d bug(s) into todo", len(created))}
	}
}

// startBugIngest fetches issues from the GitHub target and opens the
// review surface. The fetch shells out to gh, so it runs off the main loop.
func (m *Shell) startBugIngest(repo, label, profile string, comments bool) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — bug import needs the engine", isErr: true}
		return nil
	}
	if m.bugIngesting {
		m.notice = noticeMsg{text: "an import is already running — wait for it", isErr: true}
		return nil
	}
	eng, envelope, root := m.engine, m.envelope, m.wt.Root()
	m.bugIngesting = true
	m.notice = noticeMsg{text: "importing GitHub issues…"}
	return func() tea.Msg {
		src := engine.GitHubSource{Repo: repo, Label: label, Dir: root, FetchComments: comments}
		res, err := eng.IngestBugs(context.Background(), src)
		if err != nil {
			return bugIngestLoadedMsg{err: err}
		}
		return bugIngestLoadedMsg{res: res, profile: profile, envelope: envelope}
	}
}

// bugIngestLoadedMsg delivers the result of a bug import to the shell.
type bugIngestLoadedMsg struct {
	res      engine.BugIngestResult
	profile  string
	envelope int
	err      error
}

// bugIngestViewRender paints the bug-import review surface.
func (m *Shell) bugIngestViewRender(w, h int) string {
	bv := m.bugIngest
	s := m.styles
	if bv == nil {
		return ""
	}
	var b strings.Builder
	vis := bv.visible()
	countPill := fmt.Sprintf("%d keep", bv.keptCount())
	if bv.active() {
		countPill = fmt.Sprintf("%d/%d keep", bv.keptCount(), len(bv.props))
	}
	head := s.Title.Render("import bugs") + " " + s.Base.Render("· "+bv.source) +
		"  " + s.Pill.Render(countPill)
	b.WriteString("\n" + head + "\n")

	// the filter line: an editable input while filtering, a quiet summary
	// of the applied query otherwise.
	if bv.filtering {
		b.WriteString(s.KeyHint.Render("/ ") + bv.filter.View() + "\n")
	} else if bv.active() {
		b.WriteString(s.Faint.Render("/ ") + s.Subtle.Render(bv.filter.Value()) +
			s.Faint.Render(fmt.Sprintf("  (%d match)", len(vis))) + "\n")
	}
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	if len(vis) == 0 {
		b.WriteString(s.Faint.Render("no bugs match the filter") + "\n")
	}
	// count the header lines emitted so far so the list can be windowed to
	// whatever height remains.
	headerLines := strings.Count(b.String(), "\n")

	numW := len(fmt.Sprintf("%d", len(bv.props)))
	rows := make([]string, len(vis))
	for pos, i := range vis {
		ip := bv.props[i]
		marker := "  "
		title := ip.p.Title
		style := s.Base
		if ip.dropped {
			style = s.Faint
			title = "✗ " + title
		}
		if pos == bv.cursor {
			marker = s.Cursor.Render("▸ ")
			if !ip.dropped {
				style = s.Subtitle
			}
		}
		num := s.Faint.Render(fmt.Sprintf("%*d.", numW, i+1))
		line := marker + num + " " + style.Render(ansi.Truncate(title, max(w-numW-6, 8), "…"))
		if tag := bugProposalTags(ip.p); tag != "" {
			line += "  " + s.Faint.Render(tag)
		}
		rows[pos] = line
	}

	// Build the detail/skipped tail first, then window the list into the
	// height left over, so a long import can't push the selected row or the
	// detail block off the bottom of the clipped pane.
	var tail strings.Builder
	if bv.selected() >= 0 {
		tail.WriteString("\n" + bv.renderDetail(s, w))
	}
	if len(bv.skipped) > 0 {
		ids := make([]string, len(bv.skipped))
		for i, id := range bv.skipped {
			ids[i] = string(id)
		}
		tail.WriteString("\n" + s.Faint.Render(fmt.Sprintf("%d already on the board, skipped: %s", len(bv.skipped), strings.Join(ids, ", "))))
	}
	tailLines := 0
	if tail.Len() > 0 {
		tailLines = strings.Count(tail.String(), "\n") + 1
	}
	listBudget := max(h-headerLines-tailLines, 3)
	for _, line := range windowLines(rows, bv.cursor, listBudget) {
		b.WriteString(line + "\n")
	}
	b.WriteString(tail.String())

	return clipLines(b.String(), h)
}

func (bv *bugIngestView) renderDetail(s *theme.Styles, w int) string {
	i := bv.selected()
	if i < 0 {
		return ""
	}
	ip := bv.props[i]
	var b strings.Builder
	if ip.p.OneLiner != "" {
		b.WriteString(s.Subtle.Render(ansi.Truncate(ip.p.OneLiner, max(w-2, 8), "…")) + "\n")
	}
	if ip.p.ExternalRef != "" {
		b.WriteString(s.Faint.Render("ref: "+ansi.Truncate(ip.p.ExternalRef, max(w-6, 8), "…")) + "\n")
	}
	if ip.p.Report.Description != "" {
		b.WriteString(s.Base.Render(ansi.Truncate(oneLineText(ip.p.Report.Description), max(w-2, 8), "…")) + "\n")
	}
	return b.String()
}

// bugProposalTags summarizes a bug proposal's flags for the list line.
func bugProposalTags(p domain.BugProposal) string {
	var tags []string
	if p.Severity != "" {
		tags = append(tags, string(p.Severity))
	}
	if p.Skip.Triage {
		tags = append(tags, "skip triage")
	}
	if p.Skip.Diagnose {
		tags = append(tags, "skip diagnose")
	}
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, " · ") + "]"
}
