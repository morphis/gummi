package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/ui/theme"
)

// bugIngest form fields, in tab order.
const (
	bugIngestFieldRepo = iota
	bugIngestFieldLabel
	bugIngestFieldProfile
	bugIngestFieldCount
)

// bugIngestForm collects the GitHub target for a bug import: the repo
// (blank = this repo's origin remote), a label filter, and the profile
// the new bugs adopt. State defaults to open (the CLI exposes the rest).
type bugIngestForm struct {
	repo     textinput.Model
	label    textinput.Model
	profiles []string
	profile  int
	focus    int

	onSubmit func(repo, label, profile string) tea.Cmd
}

func newBugIngestForm(profiles []string, onSubmit func(repo, label, profile string) tea.Cmd) *bugIngestForm {
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
		return true, d.onSubmit(strings.TrimSpace(d.repo.Value()), strings.TrimSpace(d.label.Value()), d.profiles[d.profile])
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
	case bugIngestFieldRepo:
		d.repo, _ = d.repo.Update(key)
	case bugIngestFieldLabel:
		d.label, _ = d.label.Update(key)
	}
	return false, nil
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

	hint := "enter import · tab next · esc cancel"
	if d.focus == bugIngestFieldProfile {
		hint = "←/→ profile · enter import · esc cancel"
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
	skipped  int
	cursor   int
	profile  string
	envelope int
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
	return &bugIngestView{source: res.Source, props: props, skipped: len(res.Skipped), profile: profile, envelope: envelope}
}

func (bv *bugIngestView) keptCount() int {
	n := 0
	for _, ip := range bv.props {
		if !ip.dropped {
			n++
		}
	}
	return n
}

func (bv *bugIngestView) kept() []domain.BugProposal {
	var out []domain.BugProposal
	for _, ip := range bv.props {
		if !ip.dropped {
			out = append(out, ip.p)
		}
	}
	return out
}

func (bv *bugIngestView) setCursor(n int) {
	bv.cursor = min(max(n, 0), max(len(bv.props)-1, 0))
}

// handleBugIngestKey routes keys while the bug-import review surface is open.
func (m *Shell) handleBugIngestKey(key string) tea.Cmd {
	bv := m.bugIngest
	switch key {
	case "esc", "q":
		m.bugIngest = nil
		m.notice = noticeMsg{text: "import discarded — nothing created"}
		return nil
	case "j", "down":
		bv.setCursor(bv.cursor + 1)
	case "k", "up":
		bv.setCursor(bv.cursor - 1)
	case "x":
		if len(bv.props) > 0 {
			bv.props[bv.cursor].dropped = !bv.props[bv.cursor].dropped
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
	if len(bv.props) == 0 {
		return
	}
	i := bv.cursor
	m.Overlay.Push(newTextPrompt("rename bug", bv.props[i].p.Title, "bug title",
		func(s string) error { _, err := domain.Slugify(s); return err },
		func(s string) tea.Cmd { bv.props[i].p.Title = s; return nil }))
}

func (bv *bugIngestView) promptOneLiner(m *Shell) {
	if len(bv.props) == 0 {
		return
	}
	i := bv.cursor
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
func (m *Shell) startBugIngest(repo, label, profile string) tea.Cmd {
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
		src := engine.GitHubSource{Repo: repo, Label: label, Dir: root}
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
	head := s.Title.Render("import bugs") + " " + s.Base.Render("· "+bv.source) +
		"  " + s.Pill.Render(fmt.Sprintf("%d keep", bv.keptCount()))
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	numW := len(fmt.Sprintf("%d", len(bv.props)))
	for i, ip := range bv.props {
		marker := "  "
		title := ip.p.Title
		style := s.Base
		if ip.dropped {
			style = s.Faint
			title = "✗ " + title
		}
		if i == bv.cursor {
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
		b.WriteString(line + "\n")
	}

	if bv.cursor < len(bv.props) {
		b.WriteString("\n" + bv.renderDetail(s, w))
	}
	if bv.skipped > 0 {
		b.WriteString("\n" + s.Faint.Render(fmt.Sprintf("%d already on the board, skipped", bv.skipped)) + "\n")
	}

	b.WriteString("\n" + s.KeyHint.Render("r") + s.KeyLabel.Render(" rename") +
		s.Faint.Render(" · ") + s.KeyHint.Render("o") + s.KeyLabel.Render(" one-liner") +
		s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" drop") +
		s.Faint.Render(" · ") + s.KeyHint.Render("A") + s.KeyLabel.Render(" approve") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" discard"))

	return clipLines(b.String(), h)
}

func (bv *bugIngestView) renderDetail(s *theme.Styles, w int) string {
	ip := bv.props[bv.cursor]
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
