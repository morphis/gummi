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

// bugIngestView is the bug-import review surface (DESIGN §11.4 phase B,
// bug variant): the GitHub issues fetched as proposals, single-picked
// before any is minted. Unlike feature ingest there is no coverage map and
// no merge — issues are discrete — and unlike a batch gate there is no
// "kept set" either: only a cursor position, and enter imports exactly the
// row under it.
type bugIngestView struct {
	source string
	props  []domain.BugProposal
	// onBoard maps an external ref to the card that already carries it:
	// such an issue is listed greyed with its id rather than counted in a
	// footnote, and enter on it says so instead of filling the form.
	onBoard map[string]domain.FeatureID
	cursor  int // index into the filtered (visible) list
	// params is what was fetched — repo, owner/repo, label, state — so
	// the l/s/o keys can refetch with one of them changed.
	params bugIngestParams

	filter    textinput.Model // live substring filter over the fetched issues
	filtering bool            // the filter input has focus (typing into it)
	// edited marks a title or one-liner the user changed by hand. The
	// fetched batch itself is one `G` away from being re-fetched, so a
	// bare esc discarding it costs nothing — the hand edits are the only
	// unrecoverable part, and the only thing worth a confirm.
	edited bool
}

// bugIngestParams is one fetch's target: the managed repo whose checkout
// gh runs in, the owner/repo it lists, and the label/state filters.
type bugIngestParams struct {
	repo      string // configured repo name ("" = default)
	ownerRepo string // "owner/repo" for gh; "" lets gh detect from the checkout
	label     string
	state     string // open|closed|all
}

// newBugIngestView opens with the filter input focused: typing narrows the
// list live, and esc moves focus to the list without discarding the query.
// Issues already on the board are listed too, greyed with their card id.
func newBugIngestView(res engine.BugIngestResult, params bugIngestParams) *bugIngestView {
	filter := textinput.New()
	filter.Placeholder = "filter by title / label / text…"
	filter.CharLimit = 80
	filter.SetWidth(40)
	filter.Focus()
	onBoard := map[string]domain.FeatureID{}
	props := append([]domain.BugProposal(nil), res.Proposals...)
	for _, sk := range res.Skipped {
		onBoard[sk.Proposal.ExternalRef] = sk.LocalID
		props = append(props, sk.Proposal)
	}
	return &bugIngestView{
		source: res.Source, props: props, onBoard: onBoard,
		params: params, filter: filter, filtering: true,
	}
}

// markOnBoard records that ref was just minted as id, so the row greys
// out in place when Create returns here.
func (bv *bugIngestView) markOnBoard(ref string, id domain.FeatureID) {
	if ref == "" {
		return
	}
	bv.onBoard[ref] = id
}

// isOnBoard reports the card already carrying the proposal's ref.
func (bv *bugIngestView) isOnBoard(p domain.BugProposal) (domain.FeatureID, bool) {
	id, ok := bv.onBoard[p.ExternalRef]
	return id, ok
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
	for i, p := range bv.props {
		if bugMatches(p, q) {
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

func (bv *bugIngestView) setCursor(n int) {
	if vis := len(bv.visible()); vis > 0 {
		bv.cursor = min(max(n, 0), vis-1)
	} else {
		bv.cursor = 0
	}
}

// bindings is the bug-import surface's key table (see keymap.go), split by
// filter focus like handleBugIngestKey routes. up/down/pgup/pgdn always
// move the cursor regardless of focus, so they aren't re-listed per state.
func (bv *bugIngestView) bindings() []binding {
	if bv.filtering {
		// the filter takes every printable key, ? included, so alt+/ is
		// the only route to this table from here.
		return withHelpKey([]binding{
			{key: "type", label: "filter", help: "type to filter the list", bar: true},
			{key: "up/down/pgup/pgdn", label: "move", help: "move the highlighted row without leaving the filter"},
			{key: "enter", label: "use", help: "fill the new-card form with the highlighted issue", bar: true},
			// esc last here too: leaving the filter is this sub-surface's
			// own way out, and it is the row that must outlive the others.
			{key: "esc", label: "list", help: "leave the filter for the list, keeping the query", bar: true},
		})
	}
	return []binding{
		{key: "j/k", label: "select", help: "move over the bugs"},
		{key: "pgup/pgdn", label: "page", help: "move by a page over the bugs"},
		{key: "/", label: "filter", help: "focus the filter", bar: true},
		{key: "r", label: "rename", help: "rename the bug (also c)", bar: true},
		{key: "e", label: "one-liner", help: "edit the one-line summary"},
		{key: "l", label: "label", help: "change the label filter and fetch again"},
		{key: "s", label: "state", help: "cycle open / closed / all and fetch again"},
		{key: "o", label: "other repo", help: "list another owner/repo's issues"},
		{key: "enter", label: "use", help: "fill the new-card form with the highlighted issue", bar: true},
		// esc stays last: the status bar drops hints from the
		// second-to-last backwards precisely so the surface's escape hatch
		// outlives every other row (statusbar.Render).
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "leave the picker — back to the form, nothing created (also q)", bar: true},
	}
}

// handleBugIngestKey routes keys while the bug-import review surface is
// open. While the filter is focused, keys type into it; otherwise they
// navigate and act on the filtered list.
func (m *Shell) handleBugIngestKey(msg tea.KeyPressMsg) tea.Cmd {
	bv := m.bugIngest
	key := msg.String()

	// up/down/pgup/pgdn move the cursor regardless of which side has focus,
	// so a few filter characters and a move need no extra step in between.
	switch key {
	case "up":
		bv.setCursor(bv.cursor - 1)
		return nil
	case "down":
		bv.setCursor(bv.cursor + 1)
		return nil
	case "pgup":
		bv.setCursor(bv.cursor - m.mainPage())
		return nil
	case "pgdown":
		bv.setCursor(bv.cursor + m.mainPage())
		return nil
	}

	if bv.filtering {
		switch key {
		case "esc":
			// esc is "back one level" everywhere else, and the filter is a
			// level: it drops focus to the list, keeping the query and the
			// pass intact. A second esc from the list discards. This used
			// to be tab, which is a tab switch now, and esc-discards-from-
			// anywhere was the more dangerous of the two shapes anyway.
			bv.filtering = false
			bv.filter.Blur()
		case "enter":
			return m.useInForm()
		default:
			bv.filter, _ = bv.filter.Update(msg)
			bv.setCursor(bv.cursor) // reclamp: the visible set may have shrunk
		}
		return nil
	}
	switch key {
	case "esc", "q":
		return m.discardBugIngest()
	case "/":
		// the conventional "focus the filter" key, and free here — tab
		// used to do this, back when it meant five different things.
		bv.filtering = true
		bv.filter.Focus()
	case "j":
		bv.setCursor(bv.cursor + 1)
	case "k":
		bv.setCursor(bv.cursor - 1)
	case "r", "c":
		bv.promptTitle(m)
	case "e":
		bv.promptOneLiner(m)
	case "l":
		m.Overlay.Push(newTextPrompt("label filter", bv.params.label, "label (empty = every issue)", nil, func(v string) tea.Cmd {
			params := bv.params
			params.label = strings.TrimSpace(v)
			return m.refetchBugIngest(params)
		}))
	case "s":
		params := bv.params
		switch params.state {
		case "open":
			params.state = "closed"
		case "closed":
			params.state = "all"
		default:
			params.state = "open"
		}
		return m.refetchBugIngest(params)
	case "o":
		m.Overlay.Push(newTextPrompt("browse issues of", bv.params.ownerRepo, "owner/repo", nil, func(v string) tea.Cmd {
			params := bv.params
			params.ownerRepo = strings.TrimSpace(v)
			return m.refetchBugIngest(params)
		}))
	case "enter":
		return m.useInForm()
	}
	return nil
}

// refetchBugIngest replaces the open picker with a fresh fetch under
// params, keeping the parked form where it is.
func (m *Shell) refetchBugIngest(params bugIngestParams) tea.Cmd {
	m.bugIngest = nil
	return m.startBugIngest(params)
}

func (bv *bugIngestView) promptTitle(m *Shell) {
	i := bv.selected()
	if i < 0 {
		return
	}
	m.Overlay.Push(newTextPrompt("rename bug", bv.props[i].Title, "bug title",
		func(s string) error { _, err := domain.Slugify(s); return err },
		func(s string) tea.Cmd { bv.props[i].Title = s; bv.edited = true; return nil }))
}

func (bv *bugIngestView) promptOneLiner(m *Shell) {
	i := bv.selected()
	if i < 0 {
		return
	}
	m.Overlay.Push(newTextPrompt("edit one-liner", bv.props[i].OneLiner, "one-line summary", nil,
		func(s string) tea.Cmd { bv.props[i].OneLiner = s; bv.edited = true; return nil }))
}

// discardBugIngest drops the whole fetched batch. The fetch itself is
// one `G` away from being redone, so an untouched batch goes without
// ceremony; hand-edited titles and one-liners are the part no re-fetch
// brings back, so those get asked about first.
func (m *Shell) discardBugIngest() tea.Cmd {
	bv := m.bugIngest
	if bv == nil {
		return nil
	}
	drop := func() tea.Cmd {
		m.bugIngest = nil
		m.notice = noticeMsg{text: "left the issue picker — nothing created"}
		m.restorePendingCard()
		return nil
	}
	if !bv.edited {
		return drop()
	}
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-bug-ingest-discard",
		cancelLabel:  "Keep",
		confirmLabel: "Discard",
		question:     "discard the import?",
		detail:       "your renamed titles and one-liners are not kept — re-importing fetches the issues again as they are on GitHub",
		onConfirm:    drop,
	})
	return nil
}

// useInForm fills the new-card form with the row under the cursor and
// brings the form back. Nothing is minted here: the form is the one
// confirmation for every source, and Create from a form filled this way
// returns to this picker with the row greyed.
func (m *Shell) useInForm() tea.Cmd {
	bv := m.bugIngest
	if bv == nil {
		return nil
	}
	i := bv.selected()
	if i < 0 {
		m.notice = noticeMsg{text: "no issue matches the filter", isErr: true}
		return nil
	}
	p := bv.props[i]
	if id, ok := bv.isOnBoard(p); ok {
		m.notice = noticeMsg{text: fmt.Sprintf("%s is already on the board as %s", p.ExternalRef, id), isErr: true}
		return nil
	}
	d := m.pendingCard
	m.pendingCard = nil
	if d == nil {
		d = m.openCardForm(domain.KindBug)
	}
	if d.repo.multi() && d.repo.name() != bv.params.repo {
		for j, name := range d.repo.options() {
			if name == bv.params.repo {
				d.repo.idx = j
			}
		}
	}
	d.fill(p)
	m.Overlay.Push(d)
	return nil
}

// startBugIngest fetches issues for params and opens the picker. gh runs
// in the chosen repository's checkout with an explicit owner/repo, so a
// `repos:` workspace whose root is no checkout still lists the right
// issues. The fetch shells out, so it runs off the main loop.
func (m *Shell) startBugIngest(params bugIngestParams) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — bug import needs the engine", isErr: true}
		m.restorePendingCard()
		return nil
	}
	if m.bugIngesting {
		m.notice = noticeMsg{text: "an import is already running — wait for it", isErr: true}
		return nil
	}
	fetch := m.ghIssues
	if fetch == nil {
		eng := m.engine
		fetch = func(ctx context.Context, src engine.GitHubSource) (engine.BugIngestResult, error) {
			return eng.IngestBugs(ctx, src)
		}
	}
	dir, _ := m.repoRoot(params.repo)
	if dir == "" && m.wt != nil {
		dir = m.wt.Root()
	}
	m.bugIngesting = true
	m.notice = noticeMsg{text: "fetching GitHub issues…"}
	return func() tea.Msg {
		src := engine.GitHubSource{Repo: params.ownerRepo, Label: params.label, State: params.state, Dir: dir}
		res, err := fetch(context.Background(), src)
		if err != nil {
			return bugIngestLoadedMsg{err: err}
		}
		return bugIngestLoadedMsg{res: res, params: params}
	}
}

// bugIngestLoadedMsg delivers the result of a fetch to the shell.
type bugIngestLoadedMsg struct {
	res    engine.BugIngestResult
	params bugIngestParams
	err    error
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
	countPill := fmt.Sprintf("%d issue(s)", len(bv.props))
	if bv.active() {
		countPill = fmt.Sprintf("%d/%d match", len(vis), len(bv.props))
	}
	target := bv.params.ownerRepo
	if target == "" {
		target = bv.source
	}
	filters := ""
	if bv.params.label != "" {
		filters += "  " + s.Faint.Render("label ") + s.Subtle.Render(bv.params.label)
	}
	if bv.params.state != "" {
		filters += "  " + s.Faint.Render("state ") + s.Subtle.Render(bv.params.state)
	}
	head := s.Title.Render("issues") + " " + s.Base.Render("· "+target) + filters +
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
		p := bv.props[i]
		marker := "  "
		style := s.Base
		meta := s.Faint
		onBoard, taken := bv.isOnBoard(p)
		if taken {
			style = s.Faint
		}
		sel := pos == bv.cursor
		if sel {
			marker = s.BandMarker(true)
			style = s.BandText.Bold(true)
			meta = s.BandTextDim // s.Faint vanishes on the band
		}
		num := meta.Render(fmt.Sprintf("%*d.", numW, i+1))
		line := marker + num + " " + style.Render(ansi.Truncate(p.Title, max(w-numW-6, 8), "…"))
		if taken {
			line += "  " + meta.Render("on board · "+string(onBoard))
		} else if tag := bugProposalTags(p); tag != "" {
			line += "  " + meta.Render(tag)
		}
		if sel {
			line = s.Band(line, w, true)
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
	p := bv.props[i]
	var b strings.Builder
	if p.OneLiner != "" {
		b.WriteString(s.Subtle.Render(ansi.Truncate(p.OneLiner, max(w-2, 8), "…")) + "\n")
	}
	if p.ExternalRef != "" {
		b.WriteString(s.Faint.Render("ref: "+ansi.Truncate(p.ExternalRef, max(w-6, 8), "…")) + "\n")
	}
	if p.Report.Description != "" {
		b.WriteString(s.Base.Render(ansi.Truncate(oneLineText(p.Report.Description), max(w-2, 8), "…")) + "\n")
	}
	return b.String()
}

// bugProposalTags summarizes a bug proposal's flags for the list line.
func bugProposalTags(p domain.BugProposal) string {
	var tags []string
	if p.Severity != "" {
		tags = append(tags, string(p.Severity))
	}
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, " · ") + "]"
}
