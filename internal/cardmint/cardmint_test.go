package cardmint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// newTestWorkspace builds a throwaway workspace + store pair, without
// spinning up an actual git repository: state.Init only ever stats
// RepoRoot/.git, so an empty directory standing in for it is enough for
// every path cardmint touches (it never shells out to git).
func newTestWorkspace(t *testing.T) (*state.Store, state.Workspace) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, ws
}

func readSeq(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func TestMintFeatureQuickRoute(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()

	f, err := Mint(ctx, store, ws, Input{
		Kind:        domain.KindFeature,
		Description: "Add a card_new tool\n\nSo hosted agents can mint cards too.",
		Envelope:    2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Add a card_new tool" {
		t.Errorf("title = %q", f.Title)
	}
	if f.Kind != domain.KindFeature {
		t.Errorf("kind = %q", f.Kind)
	}
	if f.GateApproval != domain.GateAttended {
		t.Errorf("gate approval = %q, want empty-default %q", f.GateApproval, domain.GateAttended)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("expected a seeded draft: %v", err)
	}
	if !strings.Contains(string(raw), "So hosted agents can mint cards too.") {
		t.Errorf("draft missing seeded problem text:\n%s", raw)
	}
	// persisted, not just returned.
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil || got.ID != f.ID {
		t.Fatalf("GetFeature(%s) = %+v, %v", f.ID, got, err)
	}
}

// TestMintBugSeedsBugTemplate: a bug description with overflow text seeds
// the bug report template (## Summary), not the feature spec template
// (## Problem) — cardmint.Mint's seeded-draft branch used to call
// spec.SeededTemplate unconditionally for every non-research kind, so a
// card_new-minted bug came out feature-shaped.
func TestMintBugSeedsBugTemplate(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind:        domain.KindBug,
		Description: "Repro bug\n\nThis is a multi-line description used to trigger the seeded draft path.",
		Envelope:    2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != domain.KindBug {
		t.Errorf("kind = %q", f.Kind)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("expected a seeded draft: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "## Summary") {
		t.Errorf("draft missing bug template's Summary section:\n%s", content)
	}
	if strings.Contains(content, "## Problem") {
		t.Errorf("draft has feature template's Problem section, want bug shape:\n%s", content)
	}
	if !strings.Contains(content, "This is a multi-line description used to trigger the seeded draft path.") {
		t.Errorf("draft missing seeded summary text:\n%s", content)
	}
}

// TestMintNoOverflowNoDraft: a title-only description (nothing beyond the
// first line) with no Acceptance text seeds no draft at all.
func TestMintNoOverflowNoDraft(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "Just a title", Envelope: 2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Errorf("expected no draft, stat err = %v", err)
	}
}

// TestMintAcceptanceAloneSeedsDraft: Acceptance text alone (no problem
// overflow) is still enough to warrant a draft.
func TestMintAcceptanceAloneSeedsDraft(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "Just a title", Envelope: 2400,
		Acceptance: "it must not panic",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	raw, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("expected a seeded draft: %v", err)
	}
	if !strings.Contains(string(raw), "it must not panic") {
		t.Errorf("draft missing seeded acceptance text:\n%s", raw)
	}
}

// TestMintResearch: research uses SplitDescription (no seed/draft), always
// takes the full route regardless of Full, and renders straight to the RS
// artifact path instead of a draft.
func TestMintResearch(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindResearch, Description: "grounded look at auth", Envelope: 2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != domain.KindResearch {
		t.Errorf("kind = %q", f.Kind)
	}
	artifact := filepath.Join(ws.Root, f.ArtifactPath())
	raw, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("expected a research artifact: %v", err)
	}
	if !strings.Contains(string(raw), "grounded look at auth") {
		t.Errorf("artifact missing brief text:\n%s", raw)
	}
	draft := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Errorf("research should not seed a draft, stat err = %v", err)
	}
}

// TestMintRejectsUnknownRepoBeforeMinting: an unconfigured repo fails, and
// fails before a sequence number is consumed — matching both prior
// duplicates' behavior (createFeature, Materialize's requireRepo).
func TestMintRejectsUnknownRepoBeforeMinting(t *testing.T) {
	store, ws := newTestWorkspace(t)
	seq := readSeq(t, ws.SeqFile())

	_, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "should not exist", Envelope: 2400,
		Repo:        "nope",
		RequireRepo: func(string) error { return errors.New("repository \"nope\" is not configured") },
	})
	if err == nil {
		t.Fatal("expected an error minting against an unconfigured repo")
	}
	if got := readSeq(t, ws.SeqFile()); got != seq {
		t.Errorf("seq advanced on rejected mint: %s -> %s", seq, got)
	}
}

// nameIsA is a RequireRepo that configures exactly one repository, "a" —
// so it refuses both a wrong name and the unnamed card, the way a
// `repos:`-only workspace's pool does.
func nameIsA(name string) error {
	if name == "a" {
		return nil
	}
	if name == "" {
		return errors.New("no default repository configured")
	}
	return fmt.Errorf("repository %q is not configured", name)
}

// TestMintNilRequireRepoFailsClosed: a non-empty Repo with a nil
// RequireRepo must be rejected, not silently accepted — cardmint has no
// way to ask a caller-less "is this configured" question, and failing
// open here would let an unvalidated repo name slip past every future
// caller that forgets to wire the callback.
func TestMintNilRequireRepoFailsClosed(t *testing.T) {
	store, ws := newTestWorkspace(t)
	_, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "should not exist", Envelope: 2400, Repo: "a",
	})
	if err == nil {
		t.Fatal("expected an error minting with Repo set and RequireRepo nil")
	}
}

// TestMintRejectsUnnamedRepoWhenThereIsNoDefault: the card that names no
// repository is checked too. In a `repos:`-only workspace nothing
// resolves the empty name, so such a card is unmintable — and must be
// refused before a sequence number is spent, exactly like a misnamed one.
// cardmint used to skip the check entirely when Repo was empty, which let
// the card through and burned its id on something that could never cut a
// worktree.
func TestMintRejectsUnnamedRepoWhenThereIsNoDefault(t *testing.T) {
	store, ws := newTestWorkspace(t)
	seq := readSeq(t, ws.SeqFile())

	_, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "should not exist", Envelope: 2400,
		RequireRepo: nameIsA,
	})
	if err == nil {
		t.Fatal("expected an error minting an unnamed card with no default repository")
	}
	if got := readSeq(t, ws.SeqFile()); got != seq {
		t.Errorf("seq advanced on rejected mint: %s -> %s", seq, got)
	}
}

// TestMintAcceptsKnownRepo: a repo RepoKnown reports as configured is
// accepted and persisted on the card.
func TestMintAcceptsKnownRepo(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "lives in repo a", Envelope: 2400,
		Repo:        "a",
		RequireRepo: func(name string) error { return nameIsA(name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo != "a" {
		t.Errorf("repo = %q, want a", f.Repo)
	}
}

// TestMintEmptyRepoWithNoChecker: a caller that wires no RequireRepo at
// all has a single implicit repository and nothing to choose between, so
// the unnamed card mints. (A caller that *does* wire one has it asked
// about the unnamed card too — see
// TestMintRejectsUnnamedRepoWhenThereIsNoDefault.)
func TestMintEmptyRepoWithNoChecker(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "default repo", Envelope: 2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo != "" {
		t.Errorf("repo = %q, want empty", f.Repo)
	}
}

// TestMintPropagatesGateApprovalAndRef: an explicit GateApproval and
// ExternalRef both persist untouched.
func TestMintPropagatesGateApprovalAndRef(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "checkpointed card", Envelope: 2400,
		GateApproval: domain.GateAttended, ExternalRef: "gh-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.GateApproval != domain.GateAttended {
		t.Errorf("gate approval = %q, want %q", f.GateApproval, domain.GateAttended)
	}
	if f.ExternalRef != "gh-123" {
		t.Errorf("external ref = %q, want gh-123", f.ExternalRef)
	}
}

// TestMintSequenceIncrements: two mints in the same workspace get
// consecutive numbers.
func TestMintSequenceIncrements(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()
	f1, err := Mint(ctx, store, ws, Input{Kind: domain.KindFeature, Description: "first", Envelope: 2400})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := Mint(ctx, store, ws, Input{Kind: domain.KindBug, Description: "second", Envelope: 2400})
	if err != nil {
		t.Fatal(err)
	}
	if f2.Num != f1.Num+1 {
		t.Errorf("f2.Num = %d, want %d", f2.Num, f1.Num+1)
	}
}

// TestMintBugSeverityAndSections: a bug's severity lands on the card and
// in its report header; headings typed into the description route into
// the report's sections through the same parser the GitHub import uses;
// an imported issue's comments and provenance ride alongside.
func TestMintBugSeverityAndSections(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind:        domain.KindBug,
		Description: "Login loops\n\nSSO users bounce back.\n\n## Steps to reproduce\n1. log in\n\n## Expected\nthe dashboard",
		Envelope:    2400,
		Severity:    domain.SeverityHigh,
		Source:      "github",
		ExternalRef: "https://github.com/o/r/issues/42",
		Discussion:  "**b:** same here",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Severity != domain.SeverityHigh {
		t.Errorf("card severity = %q, want high", f.Severity)
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != domain.SeverityHigh || got.ExternalRef != "https://github.com/o/r/issues/42" {
		t.Errorf("persisted severity/ref = %q %q", got.Severity, got.ExternalRef)
	}
	raw, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatalf("expected a seeded draft: %v", err)
	}
	content := string(raw)
	for _, want := range []string{"## Reproduction\n\n1. log in", "the dashboard", "**b:** same here", "github", "issues/42"} {
		if !strings.Contains(content, want) {
			t.Errorf("draft missing %q:\n%s", want, content)
		}
	}

	// comments alone are enough to warrant a bug draft
	f2, err := Mint(context.Background(), store, ws, Input{Kind: domain.KindBug, Description: "Just a title", Envelope: 1, Discussion: "**a:** hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f2))); err != nil {
		t.Errorf("discussion-only bug seeded no draft: %v", err)
	}
}

// TestMintFeatureAcceptanceHeading: a feature description's `## Acceptance`
// section seeds the Verification plan and leaves the Problem, while the
// rest of the text stays verbatim (no other heading is recognised).
func TestMintFeatureAcceptanceHeading(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind:        domain.KindFeature,
		Description: "Dark mode\n\nThe console is white at night.\n\n## Steps to reproduce\nnot a bug heading here\n\n## Acceptance\n- a toggle in settings",
		Envelope:    2400,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatalf("expected a seeded draft: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "- a toggle in settings") {
		t.Errorf("acceptance not seeded:\n%s", content)
	}
	if !strings.Contains(content, "## Steps to reproduce\nnot a bug heading here") {
		t.Errorf("feature overflow was not kept verbatim:\n%s", content)
	}
	if strings.Contains(strings.SplitN(content, "## Verification", 2)[0], "a toggle in settings") {
		t.Errorf("acceptance text left in the Problem section:\n%s", content)
	}
}
