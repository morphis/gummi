package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// openPRLink runs the pre-fill probe and returns the shell with whatever
// it produced (a notice, an opened dialog, or both) settled.
func openPRLink(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.sel = 0
	cmd := m.runCardAction(cardAction{id: "prlink"})
	if cmd == nil {
		t.Fatalf("prlink produced no command (notice %q)", m.notice.text)
	}
	return pump(t, m, cmd)
}

// TestPRLinkPrefillsOnSingleMatch covers the probe's first outcome: gh pr
// list found exactly one PR for the branch, so the field is pre-filled
// with its number and no hint is shown — a pre-filled field the user
// confirms with one enter is the common case.
func TestPRLinkPrefillsOnSingleMatch(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error) {
		if spec != "" {
			t.Errorf("probe should resolve with an empty (auto) spec, got %q", spec)
		}
		return domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}, nil
	}
	m = openPRLink(t, m)

	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatalf("prlink did not open its dialog (notice %q)", m.notice.text)
	}
	if got := d.input.Value(); got != "42" {
		t.Errorf("prefill = %q, want \"42\"", got)
	}
	if d.hint != "" {
		t.Errorf("hint = %q, want none on a single match", d.hint)
	}
	if m.notice.isErr {
		t.Errorf("notice reported as an error on a clean single match: %q", m.notice.text)
	}
}

// TestPRLinkHintsOnNoMatch covers the second outcome: zero matches is not
// an error (most branches simply have no PR yet) — the dialog opens
// empty with a faint hint, not a blocking notice.
func TestPRLinkHintsOnNoMatch(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New(`--auto found no open PR with head branch "FD-001-rebase-me"`)
	}
	m = openPRLink(t, m)

	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatalf("prlink did not open its dialog (notice %q)", m.notice.text)
	}
	if d.input.Value() != "" {
		t.Errorf("prefill = %q, want empty on no match", d.input.Value())
	}
	if !strings.Contains(d.hint, "no PR found") {
		t.Errorf("hint = %q, want a no-PR-found note", d.hint)
	}
	if m.notice.isErr {
		t.Errorf("a zero-match probe should not raise a blocking notice, got %q", m.notice.text)
	}
}

// TestPRLinkNoticesGhFailure covers the third outcome: a genuine gh
// failure (auth, network, missing binary) surfaces as a blocking notice —
// and the dialog still opens empty, so manual entry remains the fallback
// rather than a dead end.
func TestPRLinkNoticesGhFailure(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New("gh: not authenticated")
	}
	m = openPRLink(t, m)

	if !m.notice.isErr || !strings.Contains(m.notice.text, "not authenticated") {
		t.Errorf("notice = %q, want the gh failure surfaced as a blocking notice", m.notice.text)
	}
	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatal("a gh failure should still open the dialog empty, not dead-end")
	}
	if d.input.Value() != "" {
		t.Errorf("prefill = %q, want empty after a gh failure", d.input.Value())
	}
}

// TestPRLinkSubmitLinksCard proves submit calls store.SetPullRequest with
// the resolved ref, using pr.Resolve's own URL/number/auto trichotomy
// (stood in for here by the stub) rather than a separate number path.
func TestPRLinkSubmitLinksCard(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New(`--auto found no open PR with head branch "x"`)
	}
	m = openPRLink(t, m)
	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatal("dialog did not open")
	}

	m.resolvePR = func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error) {
		if spec != "99" {
			t.Errorf("submit spec = %q, want the typed number", spec)
		}
		return domain.PullRequestRef{Repo: "o/r", Number: 99, URL: "https://github.com/o/r/pull/99", HeadSHA: strings.Repeat("b", 40)}, nil
	}
	d.input.SetValue("99")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.Overlay.Top() != nil {
		t.Fatal("dialog should have closed on submit")
	}
	if !strings.Contains(m.notice.text, "linked to o/r#99") {
		t.Errorf("notice = %q, want a linked confirmation", m.notice.text)
	}
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.PullRequest.Number != 99 || f.PullRequest.Repo != "o/r" {
		t.Errorf("PullRequest = %+v, want linked to o/r#99", f.PullRequest)
	}
}

// TestPRLinkSubmitWarnsWhenSquashMergeDisallowed proves the squash-method
// caution (the Chosen approach's Linking section, "rides along as a
// non-blocking notice") appears in the link confirmation when
// prSquashMergeAllowed reports the repo cannot squash-merge.
func TestPRLinkSubmitWarnsWhenSquashMergeDisallowed(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New(`--auto found no open PR with head branch "x"`)
	}
	m = openPRLink(t, m)
	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatal("dialog did not open")
	}

	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{Repo: "o/r", Number: 99, URL: "https://github.com/o/r/pull/99"}, nil
	}
	m.prSquashMergeAllowed = func(ctx context.Context, repo string) (bool, error) {
		if repo != "o/r" {
			t.Errorf("prSquashMergeAllowed repo = %q, want the resolved ref's repo", repo)
		}
		return false, nil
	}
	d.input.SetValue("99")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !strings.Contains(m.notice.text, "linked to o/r#99") {
		t.Errorf("notice = %q, want the link confirmation", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "does not allow squash-merge") {
		t.Errorf("notice = %q, want the squash-merge caution riding along", m.notice.text)
	}
	if m.notice.isErr {
		t.Errorf("the squash-merge caution is a non-blocking notice, not an error: %q", m.notice.text)
	}
}

// TestPRLinkSubmitSwallowsSquashMergeProbeError proves a prSquashMergeAllowed
// failure is swallowed silently — best-effort like prepareMerge's own
// provenance warn — rather than surfacing as a separate error or blocking
// the link itself.
func TestPRLinkSubmitSwallowsSquashMergeProbeError(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New(`--auto found no open PR with head branch "x"`)
	}
	m = openPRLink(t, m)
	d, ok := m.Overlay.Top().(*prLinkDialog)
	if !ok {
		t.Fatal("dialog did not open")
	}

	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{Repo: "o/r", Number: 99, URL: "https://github.com/o/r/pull/99"}, nil
	}
	m.prSquashMergeAllowed = func(ctx context.Context, repo string) (bool, error) {
		return false, errors.New("gh: rate limited")
	}
	d.input.SetValue("99")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !strings.Contains(m.notice.text, "linked to o/r#99") {
		t.Errorf("notice = %q, want the link confirmation despite the probe failure", m.notice.text)
	}
	if strings.Contains(m.notice.text, "squash-merge") {
		t.Errorf("notice = %q, a failed squash-merge probe should not surface any warning text", m.notice.text)
	}
	if m.notice.isErr {
		t.Errorf("a failed squash-merge probe should not turn the link confirmation into an error: %q", m.notice.text)
	}
}

// TestPRLinkSubmitRefusesWhenAlreadyLinked proves the already-linked guard
// is re-checked at submit time against the store, not just at the moment
// the dialog opened — the board row (and the dialog it opened) may be
// stale against a concurrent `gummi pr link` in another terminal.
func TestPRLinkSubmitRefusesWhenAlreadyLinked(t *testing.T) {
	m, _, _ := rebaseFeatureFixture(t)
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		return domain.PullRequestRef{}, errors.New(`--auto found no open PR with head branch "x"`)
	}
	m = openPRLink(t, m)
	if _, ok := m.Overlay.Top().(*prLinkDialog); !ok {
		t.Fatal("dialog did not open")
	}

	// a concurrent link lands between the dialog opening and the submit.
	stolen := domain.PullRequestRef{Repo: "o/r", Number: 1, URL: "https://github.com/o/r/pull/1"}
	if err := m.store.SetPullRequest(context.Background(), "FD-001", stolen); err != nil {
		t.Fatal(err)
	}

	called := false
	m.resolvePR = func(context.Context, string, string, string) (domain.PullRequestRef, error) {
		called = true
		return domain.PullRequestRef{Repo: "o/r", Number: 2, URL: "https://github.com/o/r/pull/2"}, nil
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if called {
		t.Error("resolvePR should not run once the run-time guard finds the card already linked")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "already linked to o/r#1") {
		t.Errorf("notice = %q, want the already-linked refusal naming the concurrent link", m.notice.text)
	}
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.PullRequest.Number != 1 {
		t.Errorf("PullRequest = %+v, the concurrent link should survive untouched", f.PullRequest)
	}
}
