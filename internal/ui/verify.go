package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/verifydoc"
)

// verifyResultMsg carries the outcome of a verify run.
type verifyResultMsg struct {
	feature domain.FeatureID
	stage   domain.Stage
	results []verify.Result
	err     error
}

// stagedChecks is a manual verify run's result, scoped to the stage it ran
// against. A card that has since moved to a different stage no longer
// matches, so the entry stops counting as current without needing an
// explicit delete at every stage-transition call site.
type stagedChecks struct {
	stage   domain.Stage
	results []verify.Result
}

// checksFor returns the last manual verify results for f, or nil if there
// are none or they were produced on a stage f has since moved off of.
func (m *Shell) checksFor(f domain.Feature) []verify.Result {
	c, ok := m.checks[f.ID]
	if !ok || c.stage != f.Stage {
		return nil
	}
	return c.results
}

// runChecks surfaces the artifact's gummi-checks commands, then (on
// confirm) runs them in the feature's worktree. The surface-before-run
// step is the safety for artifact-carried commands (DESIGN §4.4 threat
// list) — the dialog shows exactly what will execute.
func (m *Shell) runChecks(f domain.Feature) tea.Cmd {
	if f.Kind == domain.KindResearch {
		return m.runDocVerify(f)
	}
	workDir := filepath.Join(m.wt.Root(), f.WorktreePath())
	path := m.artifactFile(&f)
	if path == "" {
		m.notice = noticeMsg{text: "no checks yet — gummi discovers them into the " + artifactNoun(f.Kind) + " at approval"}
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
		return nil
	}
	checks, _, err := spec.ParseChecks(string(raw))
	if err != nil {
		m.notice = noticeMsg{text: sanitize(err.Error()) + " — fix the block in the " + artifactNoun(f.Kind), isErr: true}
		return nil
	}
	if len(checks) == 0 {
		m.notice = noticeMsg{text: "no gummi-checks block in the " + artifactNoun(f.Kind) +
			" — discovery runs at approval, or add the block by hand"}
		return nil
	}
	m.Overlay.Push(newVerifyDialog(f, checks, func() tea.Cmd {
		return m.execChecks(f, workDir, checks)
	}))
	return nil
}

// artifactNoun names a card's design artifact for the surfaces that show
// it — the thread's pinned line, the action inventory row, the header of
// the view that row opens, the gate wording and the verify notices — so
// the document cannot be renamed between the line pointing at it and the
// thing that line opens.
//
// The wording itself belongs to the kind (domain.Kind.ArtifactNoun), not
// to the UI: the engine's stage kickoff names the same document to the
// agent, and the two must agree.
func artifactNoun(k domain.Kind) string { return k.ArtifactNoun() }

// noWorktreeYet is the refusal a surface gives when it needs a worktree
// the card has not got. It exists so the sentence names a gate the card
// will actually cross: the worktree appears when the design phase is
// approved, and what you approve there is the card's own artifact — a
// feature's spec, a bug's report. The five copies of this line all said
// "created at spec approval", which is a stage a bug card's route
// (triage → diagnose → fix) does not contain, so it sent that reader
// looking for something that was never coming.
//
// Naming the artifact rather than the stage also survives the skip
// flags: a bug with diagnose skipped approves its report at triage, and
// the sentence stays true.
func noWorktreeYet(f domain.Feature) string {
	return string(f.ID) + " has no worktree yet (created when you approve the " + artifactNoun(f.Kind) + ")"
}

// verifyTimeout bounds a whole verify run so a hung repo command can't
// wedge the run goroutine forever.
const verifyTimeout = 10 * time.Minute

// execChecks runs the checks in a command (off the UI goroutine), holding
// the card's lock while repo commands execute in its worktree — the same
// lock `gummi verify` takes for the same work.
func (m *Shell) execChecks(f domain.Feature, workDir string, checks []domain.Check) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
		defer cancel()
		results := verify.RunBounded(ctx, workDir, checks, verify.CheckTimeout)
		return verifyResultMsg{feature: f.ID, stage: f.Stage, results: results}
	})
}

// verifyDialog surfaces the check commands and asks for confirmation
// before running them.
type verifyDialog struct {
	feature domain.FeatureID
	checks  []domain.Check
	onRun   func() tea.Cmd
}

func newVerifyDialog(f domain.Feature, checks []domain.Check, onRun func() tea.Cmd) *verifyDialog {
	return &verifyDialog{feature: f.ID, checks: checks, onRun: onRun}
}

func (d *verifyDialog) ID() string { return "verify" }

func (d *verifyDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "n", "q":
		return true, nil
	case "enter", "y":
		return true, d.onRun()
	}
	return false, nil
}

func (d *verifyDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("verify "+string(d.feature)) + "\n")
	b.WriteString(s.Warning.Render("these commands run in the feature's worktree") + "\n\n")
	width := max(min(w-10, 72), 24)
	for _, ch := range d.checks {
		b.WriteString("  " + s.CardID.Render(sanitize(ch.Name)) + "\n")
		// show the FULL command, wrapped — never truncated. The user must
		// see exactly what will run (the surface-before-run safety); a
		// hidden tail would let a repo smuggle in extra commands.
		for _, l := range strings.Split(wrapText(sanitize(ch.Cmd), width), "\n") {
			b.WriteString("    " + s.Faint.Render(l) + "\n")
		}
	}
	b.WriteString("\n" + s.KeyHint.Render("enter") + s.KeyLabel.Render(" run") +
		s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" cancel"))
	return s.DialogFrame.Render(b.String())
}

// runDocVerify runs the deterministic verifydoc floor against a research
// card's artifact — zero-token and pure, so it re-runs on every press with
// no memoization; a stale report would mislead. Dispatches a report dialog
// on any failure, or a notice on a clean pass or a doc/repo that isn't
// there yet to check.
func (m *Shell) runDocVerify(f domain.Feature) tea.Cmd {
	path := m.artifactFile(&f)
	if path == "" {
		m.notice = noticeMsg{text: string(f.ID) + ": no doc yet — verifydoc runs on the artifact"}
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
		return nil
	}
	mgr, err := m.wt.ManagerFor(context.Background(), &f)
	if err != nil {
		m.notice = noticeMsg{text: string(f.ID) + ": repo unresolved — " + err.Error(), isErr: true}
		return nil
	}
	artifact := string(raw)
	files := readCitedFiles(mgr.RepoRoot(), verifydoc.CitedPaths(artifact))
	report := verifydoc.Check(artifact, files)
	if report.Pass() {
		m.notice = noticeMsg{text: string(f.ID) + ": document verify — clean"}
		return nil
	}
	m.Overlay.Push(newDocVerifyDialog(f, report))
	return nil
}

// readCitedFiles reads each cited path's lines from root, keyed by the
// path as cited. A path that would resolve outside root is skipped and
// never read; an unreadable file is silently dropped — verifydoc reports
// the missing citation itself. Mirrors engine.fileMap, the same
// containment contract the engine's own document-verify gate uses.
func readCitedFiles(root string, paths []string) map[string][]string {
	out := make(map[string][]string, len(paths))
	for _, p := range paths {
		full := filepath.Join(root, p)
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		data, err := os.ReadFile(full) //nolint:gosec // rel is checked above to stay under root
		if err != nil {
			continue
		}
		out[p] = strings.Split(string(data), "\n")
	}
	return out
}

// docVerifyDialog presents a research document's verifydoc report: every
// broken citation and unmapped brief question, plus the open-thread count
// when nonzero. Pushed only on a failing report — a clean one goes to a
// notice instead.
type docVerifyDialog struct {
	feature domain.FeatureID
	report  verifydoc.Report
}

func newDocVerifyDialog(f domain.Feature, report verifydoc.Report) *docVerifyDialog {
	return &docVerifyDialog{feature: f.ID, report: report}
}

func (d *docVerifyDialog) ID() string { return "doc-verify" }

func (d *docVerifyDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "enter", "q":
		return true, nil
	}
	return false, nil
}

func (d *docVerifyDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("verify "+string(d.feature)) + "\n\n")
	if len(d.report.Citations) > 0 {
		b.WriteString(s.Warning.Render("broken citations") + "\n")
		for _, c := range d.report.Citations {
			b.WriteString("  " + s.Error.Render(sanitize(c.Citation)) + s.Faint.Render(" — "+sanitize(c.Reason)) + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.report.Coverage) > 0 {
		b.WriteString(s.Warning.Render("unmapped questions") + "\n")
		for _, c := range d.report.Coverage {
			b.WriteString("  " + s.Error.Render(sanitize(c.Item)) + s.Faint.Render(" — "+sanitize(c.Reason)) + "\n")
		}
		b.WriteString("\n")
	}
	if d.report.OpenThreads > 0 {
		b.WriteString(s.Warning.Render(fmt.Sprintf("open threads: %d", d.report.OpenThreads)) + "\n\n")
	}
	b.WriteString(s.Faint.Render("enter/esc close"))
	return s.DialogFrame.Render(b.String())
}

// baselineNotice summarizes a baseline run: quiet on all-green, loud on
// a failing command — at approval the block is still the architect's to
// fix, and a failure here is a bad command or pre-existing breakage,
// never the feature's fault.
func baselineNotice(id domain.FeatureID, results []verify.Result) noticeMsg {
	for _, r := range results {
		if r.OK {
			continue
		}
		reason := fmt.Sprintf("FAILS on the fresh branch (exit %d) — pre-existing failure or wrong command", r.ExitCode)
		switch r.Status {
		case verify.StatusTimeout:
			reason = "did not finish — timed out"
		case verify.StatusNotRun:
			reason = "could not run — check budget exhausted"
		case verify.StatusFail:
			if r.ExitCode == -1 {
				reason = "could not run — malformed command"
			}
		}
		return noticeMsg{isErr: true, text: fmt.Sprintf(
			"%s: baseline — check '%s' %s; fix the gummi-checks block or it reads FAIL (pre-existing) at verify",
			id, sanitize(r.Name), reason)}
	}
	return noticeMsg{text: fmt.Sprintf("%s: baseline — all %d repo check(s) pass on the fresh branch", id, len(results))}
}

// verifySummary renders the last verify results for the dashboard.
func verifySummary(s *theme.Styles, results []verify.Result) string {
	if len(results) == 0 {
		return ""
	}
	passed := 0
	for _, r := range results {
		if r.OK {
			passed++
		}
	}
	var b strings.Builder
	head := s.Subtitle.Render("verify")
	if passed == len(results) {
		head += "  " + s.Success.Render(fmt.Sprintf("✓ %d/%d passed", passed, len(results)))
	} else {
		head += "  " + s.Error.Render(fmt.Sprintf("✗ %d/%d passed", passed, len(results)))
	}
	b.WriteString(head + "\n")
	for _, r := range results {
		mark := s.Success.Render("✓")
		if !r.OK {
			mark = s.Error.Render("✗")
		}
		b.WriteString("  " + mark + " " + s.Subtle.Render(sanitize(r.Name)) +
			s.Faint.Render(" · "+sanitize(r.Cmd)) + "\n")
	}
	return b.String()
}
