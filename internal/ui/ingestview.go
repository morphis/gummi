package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/ui/theme"
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
}

// ingestProposal is one proposed feature plus its review state.
type ingestProposal struct {
	p       domain.FeatureProposal
	dropped bool // excluded from materialization, kept visible for undo
}

func newIngestView(res domain.IngestResult, profile string, envelope int) *ingestView {
	props := make([]ingestProposal, len(res.Proposals))
	for i, p := range res.Proposals {
		props[i] = ingestProposal{p: p}
	}
	return &ingestView{
		source: res.SourcePath, coverage: res.Coverage, props: props,
		profile: profile, envelope: envelope,
	}
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

// handleIngestKey routes keys while the ingest review surface is open.
func (m *Shell) handleIngestKey(key string) tea.Cmd {
	iv := m.ingest
	switch key {
	case "esc", "q":
		m.ingest = nil
		m.notice = noticeMsg{text: "ingest discarded — nothing created"}
		return nil
	case "j", "down":
		iv.setCursor(iv.cursor + 1)
	case "k", "up":
		iv.setCursor(iv.cursor - 1)
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
		id:        "confirm-ingest",
		question:  fmt.Sprintf("materialize %d feature(s) into todo?", n),
		detail:    detail,
		onConfirm: m.materializeIngest,
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
	opts := engine.MaterializeOpts{Profile: iv.profile, Envelope: iv.envelope}
	m.ingest = nil
	return func() tea.Msg {
		created, err := eng.Materialize(context.Background(), res, opts)
		if err != nil {
			return noticeMsg{text: "ingest: " + sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("ingested %d feature(s) into todo", len(created))}
	}
}

// startIngest runs the architect decomposition pass for a source file and
// opens the review surface on success. The pass spawns a transient agent
// session, so it runs in a command (off the main loop).
func (m *Shell) startIngest(path, profile string) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — ingestion needs one", isErr: true}
		return nil
	}
	if m.ingesting {
		m.notice = noticeMsg{text: "an ingest is already decomposing — wait for it", isErr: true}
		return nil
	}
	eng, envelope := m.engine, m.envelope
	m.ingesting = true
	m.notice = noticeMsg{text: "ingesting " + path + " — decomposing…"}
	return func() tea.Msg {
		res, err := eng.Ingest(context.Background(), path, profile)
		if err != nil {
			return ingestLoadedMsg{err: err}
		}
		return ingestLoadedMsg{res: res, profile: profile, envelope: envelope}
	}
}

// ingestLoadedMsg delivers the result of an ingest pass to the shell.
type ingestLoadedMsg struct {
	res      domain.IngestResult
	profile  string
	envelope int
	err      error
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
	for i, ip := range iv.props {
		marker := "  "
		title := ip.p.Title
		style := s.Base
		if ip.dropped {
			style = s.Faint
			title = "✗ " + title
		}
		if i == iv.cursor {
			marker = s.Cursor.Render("▸ ")
			if !ip.dropped {
				style = s.Subtitle
			}
		}
		num := s.Faint.Render(fmt.Sprintf("%*d.", numW, i+1))
		line := marker + num + " " + style.Render(ansi.Truncate(title, max(w-numW-6, 8), "…"))
		line += "  " + s.Faint.Render(proposalTags(ip.p))
		b.WriteString(line + "\n")
	}

	// details for the cursor proposal
	if iv.cursor < len(iv.props) {
		b.WriteString("\n")
		b.WriteString(iv.renderDetail(s, w))
	}

	b.WriteString("\n" + iv.renderCoverage(s, w))

	b.WriteString("\n" + s.KeyHint.Render("r") + s.KeyLabel.Render(" rename") +
		s.Faint.Render(" · ") + s.KeyHint.Render("o") + s.KeyLabel.Render(" one-liner") +
		s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" drop") +
		s.Faint.Render(" · ") + s.KeyHint.Render("m") + s.KeyLabel.Render(" merge up") +
		s.Faint.Render(" · ") + s.KeyHint.Render("A") + s.KeyLabel.Render(" approve") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" discard"))

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
