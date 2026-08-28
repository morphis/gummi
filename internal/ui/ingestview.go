package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// ingestView is the ingest review surface (DESIGN §11.4 phase B): the
// features an architect pass proposed from a source document, edited and
// approved before any are minted. It reuses the annotate-style line-cursor
// interaction — move over the proposals, then rename, edit, drop, or merge
// them, and approve the set.
type ingestView struct {
	source   string                 // .gummi/ingest/… provenance path
	coverage []domain.CoverageEntry // the source→feature map
	props    []ingestProposal
	cursor   int
	profile  string
	envelope int
	repo     string // managed repository the batch belongs to ("" = default)

	// decomposeFor (FD-081) is the done RS card this review surface is a
	// re-run of decompose for; zero means a standalone ingest. Non-zero
	// routes approval to Engine.MintProposals (which re-derives its own
	// target rows and repo/profile/envelope from the RS card) instead of
	// Engine.Materialize.
	decomposeFor domain.FeatureID
}

// ingestProposal is one proposed feature plus its review state.
type ingestProposal struct {
	p       domain.FeatureProposal
	dropped bool // excluded from materialization, kept visible for undo
}

func newIngestView(res domain.IngestResult, profile string, envelope int, repo string) *ingestView {
	props := make([]ingestProposal, len(res.Proposals))
	for i, p := range res.Proposals {
		props[i] = ingestProposal{p: p}
	}
	return &ingestView{
		source: res.SourcePath, coverage: res.Coverage, props: props,
		profile: profile, envelope: envelope, repo: repo,
	}
}

// newDecomposeReviewView builds the same review surface for a decompose
// re-run (FD-081): decomposeFor marks it so approval mints through
// Engine.MintProposals against that RS card instead of Engine.Materialize.
func newDecomposeReviewView(res domain.IngestResult, cardID domain.FeatureID) *ingestView {
	iv := newIngestView(res, "", 0, "")
	iv.decomposeFor = cardID
	return iv
}

// kept returns the proposals that survive review (not dropped) as an
// IngestResult ready to materialize.
func (iv *ingestView) kept() domain.IngestResult {
	var out []domain.FeatureProposal
	for _, ip := range iv.props {
		if !ip.dropped {
			out = append(out, ip.p)
		}
	}
	return domain.IngestResult{SourcePath: iv.source, Proposals: out, Coverage: iv.coverage}
}

// keptCount is how many proposals would be materialized.
func (iv *ingestView) keptCount() int {
	n := 0
	for _, ip := range iv.props {
		if !ip.dropped {
			n++
		}
	}
	return n
}

func (iv *ingestView) setCursor(n int) {
	iv.cursor = min(max(n, 0), max(len(iv.props)-1, 0))
}

// mergeIntoPrev folds the cursor proposal into the one above it: their
// refs, dependencies, open questions, and section content combine, and the
// cursor proposal is removed. The previous proposal's title and one-liner
// win (it is the survivor). A no-op at the top of the list.
func (iv *ingestView) mergeIntoPrev() bool {
	i := iv.cursor
	if i <= 0 || i >= len(iv.props) {
		return false
	}
	prev := &iv.props[i-1].p
	cur := iv.props[i].p
	prev.SourceRefs = dedupStrings(append(prev.SourceRefs, cur.SourceRefs...))
	prev.Draft.OpenQuestions = append(prev.Draft.OpenQuestions, cur.Draft.OpenQuestions...)
	prev.Draft.Problem = joinPara(prev.Draft.Problem, cur.Draft.Problem)
	prev.Draft.Constraints = joinPara(prev.Draft.Constraints, cur.Draft.Constraints)
	prev.Draft.Acceptance = joinPara(prev.Draft.Acceptance, cur.Draft.Acceptance)
	// combine dependencies, then drop any that now point at the merged
	// features themselves (a feature can't depend on itself).
	deps := dedupStrings(append(prev.DependsOn, cur.DependsOn...))
	prev.DependsOn = deps[:0]
	for _, d := range deps {
		if d != prev.Title && d != cur.Title {
			prev.DependsOn = append(prev.DependsOn, d)
		}
	}
	iv.props = append(iv.props[:i], iv.props[i+1:]...)
	iv.setCursor(i - 1)
	return true
}

// bindings is the ingest review surface's key table (see keymap.go).
func (iv *ingestView) bindings() []binding {
	return []binding{
		{key: "j/k", label: "select", help: "move over the proposals"},
		{key: "pgup/pgdn", label: "page", help: "move by a page over the proposals"},
		{key: "r", label: "rename", help: "rename the proposal (also c)", bar: true},
		{key: "o", label: "one-liner", help: "edit the one-line summary", bar: true},
		{key: "x", label: "drop", help: "drop/undrop the proposal", bar: true},
		{key: "m", label: "merge up", help: "fold the proposal into the one above", bar: true},
		{key: "A", label: "approve", help: "materialize the kept proposals into todo", bar: true},
		{key: "esc", label: "discard", help: "discard the ingest — nothing created (also q)", bar: true},
		{key: "?", label: "help", bar: true},
	}
}

// handleIngestKey routes keys while the ingest review surface is open.
func (m *Shell) handleIngestKey(key string) tea.Cmd {
	iv := m.ingest
	switch key {
	case "esc", "q":
		// the proposals cost an architect pass to produce and nothing has
		// been written yet, so a reflex esc used to throw away paid work
		// with no way back. Ask, and name what is being lost.
		n := len(iv.props)
		m.Overlay.Push(&confirmDialog{
			id:           "confirm-ingest-discard",
			cancelLabel:  "Keep",
			confirmLabel: "Discard",
			question:     fmt.Sprintf("discard %d proposal(s)?", n),
			detail:       "they came from a paid architect pass over " + iv.source + " and are not recoverable — nothing has been created yet",
			onConfirm: func() tea.Cmd {
				m.ingest = nil
				m.notice = noticeMsg{text: "ingest discarded — nothing created"}
				return nil
			},
		})
		return nil
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "j", "down":
		iv.setCursor(iv.cursor + 1)
	case "k", "up":
		iv.setCursor(iv.cursor - 1)
	case "pgdown":
		iv.setCursor(iv.cursor + m.mainPage())
	case "pgup":
		iv.setCursor(iv.cursor - m.mainPage())
	case "x":
		if len(iv.props) > 0 {
			iv.props[iv.cursor].dropped = !iv.props[iv.cursor].dropped
		}
	case "r", "c":
		iv.promptTitle(m)
	case "o":
		iv.promptOneLiner(m)
	case "m":
		if !iv.mergeIntoPrev() {
			m.notice = noticeMsg{text: "merge folds a proposal into the one above — move down first"}
		}
	case "A":
		return m.approveIngest()
	}
	return nil
}

// promptTitle opens the rename dialog for the cursor proposal; the new
// title must slugify (it becomes the feature's ID slug).
func (iv *ingestView) promptTitle(m *Shell) {
	if len(iv.props) == 0 {
		return
	}
	i := iv.cursor
	cur := iv.props[i].p.Title
	m.Overlay.Push(newTextPrompt("rename feature", cur, "feature title",
		func(s string) error {
			_, err := domain.Slugify(s)
			return err
		},
		func(s string) tea.Cmd {
			iv.props[i].p.Title = s
			return nil
		}))
}

// promptOneLiner opens the one-liner editor for the cursor proposal.
func (iv *ingestView) promptOneLiner(m *Shell) {
	if len(iv.props) == 0 {
		return
	}
	i := iv.cursor
	cur := iv.props[i].p.OneLiner
	m.Overlay.Push(newTextPrompt("edit one-liner", cur, "one-line summary", nil,
		func(s string) tea.Cmd {
			iv.props[i].p.OneLiner = s
			return nil
		}))
}

// approveIngest confirms, then materializes the kept proposals. Unmapped
// requirements are surfaced in the confirmation so the review can't skip
// past a gap unknowingly.
func (m *Shell) approveIngest() tea.Cmd {
	iv := m.ingest
	if iv == nil {
		return nil
	}
	n := iv.keptCount()
	if n == 0 {
		m.notice = noticeMsg{text: "every proposal is dropped — nothing to create", isErr: true}
		return nil
	}
	detail := iv.source
	if u := len(iv.kept().Unmapped()); u > 0 {
		detail = fmt.Sprintf("%d source requirement(s) UNMAPPED · %s", u, iv.source)
	}
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-ingest",
		cancelLabel:  "Cancel",
		confirmLabel: "Materialize",
		question:     fmt.Sprintf("materialize %d feature(s) into todo?", n),
		detail:       detail,
		onConfirm:    m.materializeIngest,
	})
	return nil
}

// materializeIngest mints the kept proposals and reloads the board. It
// captures the engine and result before clearing the surface so the
// command can't race a re-open.
func (m *Shell) materializeIngest() tea.Cmd {
	iv := m.ingest
	if iv == nil || m.engine == nil {
		return nil
	}
	eng := m.engine
	res := iv.kept()
	m.ingest = nil
	if cardID := iv.decomposeFor; cardID != "" {
		return func() tea.Msg {
			created, err := eng.MintProposals(context.Background(), cardID, res)
			if err != nil {
				return noticeMsg{text: "decompose: " + sanitize(err.Error()), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("minted %d feature(s) from %s's decomposition", len(created), cardID), reload: true}
		}
	}
	opts := engine.MaterializeOpts{Profile: iv.profile, Envelope: iv.envelope, Repo: iv.repo}
	return func() tea.Msg {
		created, err := eng.Materialize(context.Background(), res, opts)
		if err != nil {
			return noticeMsg{text: "ingest: " + sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("ingested %d feature(s) into todo", len(created)), reload: true}
	}
}

// startIngest runs the architect decomposition pass for a source file and
// opens the review surface on success. The pass spawns a transient agent
// session, so it runs in a command (off the main loop); its live steps
// stream through a channel into the ingestRun feed so the user can watch
// the architect work instead of staring at a static notice.
func (m *Shell) startIngest(path, profile, repo string) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — ingestion needs one", isErr: true}
		return nil
	}
	if m.ingestRun != nil {
		m.notice = noticeMsg{text: "an ingest is already decomposing — wait for it", isErr: true}
		return nil
	}
	eng, envelope := m.engine, m.envelope
	m.ingestRun = newIngestRunView(path)
	m.notice = noticeMsg{text: "ingesting " + path + " — decomposing…"}
	steps := make(chan engine.IngestStep, 256)
	pass := func() tea.Msg {
		defer close(steps)
		res, err := eng.Ingest(context.Background(), path, profile, func(st engine.IngestStep) {
			select {
			case steps <- st:
			default: // progress is advisory — never stall the pass on a full feed
			}
		})
		if err != nil {
			return ingestLoadedMsg{err: err}
		}
		return ingestLoadedMsg{res: res, profile: profile, envelope: envelope, repo: repo}
	}
	return tea.Batch(pass, listenIngestSteps(steps))
}

// startDecomposeReRun runs the FD-081 decompose pass for a done RS card
// and opens the review surface on success — the board-key counterpart to
// the headless `--request-changes` re-run (Chosen approach § Contract:
// "Auto-trigger, headless verb, and board key all call the same two
// ops"). Unlike startIngest it needs no source-file/profile/repo form:
// DecomposeForCard resolves everything from the card itself, and the
// plain re-run carries no operator note (the note field is TUI-only via
// --request-changes headless, not exposed here).
func (m *Shell) startDecomposeReRun(f domain.Feature) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — decompose needs one", isErr: true}
		return nil
	}
	if m.ingestRun != nil {
		m.notice = noticeMsg{text: "an ingest/decompose is already running — wait for it", isErr: true}
		return nil
	}
	eng := m.engine
	m.ingestRun = newIngestRunView(string(f.ID))
	m.notice = noticeMsg{text: "re-running decompose for " + string(f.ID) + "…"}
	return func() tea.Msg {
		res, err := eng.DecomposeForCard(context.Background(), f.ID, "")
		if err != nil {
			return ingestLoadedMsg{err: err}
		}
		return ingestLoadedMsg{res: res, decomposeFor: f.ID}
	}
}

// listenIngestSteps bridges the pass's progress channel into Bubble Tea:
// it blocks for one step and returns it as a message, and is re-issued
// after each one so the feed stays live until the channel closes.
func listenIngestSteps(ch <-chan engine.IngestStep) tea.Cmd {
	return func() tea.Msg {
		st, ok := <-ch
		if !ok {
			return ingestStreamClosedMsg{}
		}
		return ingestStepMsg{step: st, ch: ch}
	}
}

// ingestStepMsg carries one live step of the running pass (plus its
// stream, so Update can keep listening on the same channel).
type ingestStepMsg struct {
	step engine.IngestStep
	ch   <-chan engine.IngestStep
}

// ingestStreamClosedMsg marks the end of a pass's progress stream.
type ingestStreamClosedMsg struct{}

// ingestLoadedMsg delivers the result of an ingest pass to the shell.
type ingestLoadedMsg struct {
	res      domain.IngestResult
	profile  string
	envelope int
	repo     string
	err      error

	// decomposeFor (FD-081) is set by startDecomposeReRun so the shell
	// opens the review surface tagged for MintProposals instead of a
	// standalone ingest.
	decomposeFor domain.FeatureID
}

// ingestViewRender paints the review surface into the main pane.
func (m *Shell) ingestViewRender(w, h int) string {
	iv := m.ingest
	s := m.styles
	if iv == nil {
		return ""
	}
	var b strings.Builder
	head := s.Title.Render("ingest") + " " + s.Base.Render("· "+iv.source) +
		"  " + s.Pill.Render(fmt.Sprintf("%d keep", iv.keptCount()))
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	numW := len(fmt.Sprintf("%d", len(iv.props)))
	rows := make([]string, len(iv.props))
	for i, ip := range iv.props {
		marker := "  "
		title := ip.p.Title
		style := s.Base
		if ip.dropped {
			style = s.Faint
			title = "✗ " + title
		}
		sel := i == iv.cursor
		tag := s.Faint
		num := s.Faint
		if sel {
			marker = s.BandMarker(true)
			if !ip.dropped {
				style = s.BandText.Bold(true)
			} else {
				// dropped stays a tier below kept, but s.Faint vanishes on
				// the band — both lift.
				style = s.BandTextDim
			}
			tag, num = s.BandTextDim, s.BandTextDim
		}
		line := marker + num.Render(fmt.Sprintf("%*d.", numW, i+1)) + " " +
			style.Render(ansi.Truncate(title, max(w-numW-6, 8), "…"))
		line += "  " + tag.Render(proposalTags(ip.p))
		if sel {
			line = s.Band(line, w, true)
		}
		rows[i] = line
	}

	// Render the detail and coverage first so the list can be windowed to
	// whatever height is left — otherwise a long list pushes the selected
	// row and the detail block off the bottom of the clipped pane.
	var tail strings.Builder
	if iv.cursor < len(iv.props) {
		tail.WriteString("\n" + iv.renderDetail(s, w))
	}
	tail.WriteString("\n" + iv.renderCoverage(s, w))
	tailLines := strings.Count(tail.String(), "\n") + 1

	const headerLines = 3 // leading blank, head, separator
	listBudget := max(h-headerLines-tailLines, 3)
	for _, line := range windowLines(rows, iv.cursor, listBudget) {
		b.WriteString(line + "\n")
	}
	b.WriteString(tail.String())

	return clipLines(b.String(), h)
}

// renderDetail shows the selected proposal's one-liner, provenance, and
// seed highlights.
func (iv *ingestView) renderDetail(s *theme.Styles, w int) string {
	ip := iv.props[iv.cursor]
	var b strings.Builder
	if ip.p.OneLiner != "" {
		b.WriteString(s.Subtle.Render(ansi.Truncate(ip.p.OneLiner, max(w-2, 8), "…")) + "\n")
	}
	if len(ip.p.SourceRefs) > 0 {
		b.WriteString(s.Faint.Render("from: "+ansi.Truncate(strings.Join(ip.p.SourceRefs, ", "), max(w-8, 8), "…")) + "\n")
	}
	if len(ip.p.DependsOn) > 0 {
		b.WriteString(s.Faint.Render("needs: "+ansi.Truncate(strings.Join(ip.p.DependsOn, ", "), max(w-8, 8), "…")) + "\n")
	}
	if ip.p.Draft.Problem != "" {
		b.WriteString(s.Base.Render(ansi.Truncate(oneLineText(ip.p.Draft.Problem), max(w-2, 8), "…")) + "\n")
	}
	for _, q := range ip.p.Draft.OpenQuestions {
		b.WriteString(s.Warning.Render("☐ ") + s.Subtle.Render(ansi.Truncate(oneLineText(q), max(w-4, 8), "…")) + "\n")
	}
	return b.String()
}

// renderCoverage summarizes the source→feature map, flagging unmapped
// requirements loudly (DESIGN §11.2).
func (iv *ingestView) renderCoverage(s *theme.Styles, w int) string {
	if len(iv.coverage) == 0 {
		return ""
	}
	var mapped, oos int
	var unmapped []domain.CoverageEntry
	for _, c := range iv.coverage {
		switch c.Status {
		case domain.CoverageMapped:
			mapped++
		case domain.CoverageOutOfScope:
			oos++
		case domain.CoverageUnmapped:
			unmapped = append(unmapped, c)
		}
	}
	var b strings.Builder
	b.WriteString(s.Subtitle.Render("coverage") + " " +
		s.Faint.Render(fmt.Sprintf("%d mapped · %d out-of-scope · %d unmapped", mapped, oos, len(unmapped))) + "\n")
	for _, c := range unmapped {
		txt := c.Requirement
		if c.Note != "" {
			txt += " — " + c.Note
		}
		b.WriteString(s.Error.Render("! ") + s.Warning.Render(ansi.Truncate(txt, max(w-4, 8), "…")) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// proposalTags summarizes a proposal's flags for the list line.
func proposalTags(p domain.FeatureProposal) string {
	var tags []string
	if p.Skip.Brainstorm {
		tags = append(tags, "skip bs")
	}
	if p.Skip.Plan {
		tags = append(tags, "skip plan")
	}
	if n := len(p.Draft.OpenQuestions); n > 0 {
		tags = append(tags, fmt.Sprintf("%d?", n))
	}
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, " · ") + "]"
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// joinPara joins two section bodies with a blank line, skipping empties.
func joinPara(a, b string) string {
	switch {
	case strings.TrimSpace(a) == "":
		return b
	case strings.TrimSpace(b) == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// oneLineText flattens text to a single line for compact display.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipLines truncates rendered content to at most h lines so the surface
// never overflows its rect.
func clipLines(s string, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= h {
		return s
	}
	return strings.Join(lines[:h], "\n")
}

// windowLines returns at most h entries from lines, scrolled so the entry at
// cursor stays visible. Without this a selection past the fold would be
// acted on while off-screen. Returns lines unchanged when it already fits.
func windowLines(lines []string, cursor, h int) []string {
	if h <= 0 || len(lines) <= h {
		return lines
	}
	off := cursor - (h-1)/2
	if off < 0 {
		off = 0
	}
	if off > len(lines)-h {
		off = len(lines) - h
	}
	return lines[off : off+h]
}
