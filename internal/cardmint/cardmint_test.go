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

// TestMintFeatureQuickRoute: a plain feature description with overflow
// text mints on the quick route, splits title/one-liner/seed, and seeds a
// draft with the overflow as the Problem section.
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
	if f.Skip != domain.QuickRoute() {
		t.Errorf("skip = %+v, want quick route", f.Skip)
	}
	if f.GateApproval != domain.GateGates {
		t.Errorf("gate approval = %q, want empty-default %q", f.GateApproval, domain.GateGates)
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

// TestMintFeatureFullRoute: Full opts a feature into brainstorm+plan
// (empty SkipFlags) instead of the quick route.
func TestMintFeatureFullRoute(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind: domain.KindFeature, Description: "A full-route feature", Envelope: 2400, Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Skip != (domain.SkipFlags{}) {
		t.Errorf("skip = %+v, want empty (full route)", f.Skip)
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
	if f.Skip != (domain.SkipFlags{}) {
		t.Errorf("skip = %+v, want empty (research has no quick route)", f.Skip)
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
		GateApproval: domain.GateOff, ExternalRef: "gh-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.GateApproval != domain.GateOff {
		t.Errorf("gate approval = %q, want %q", f.GateApproval, domain.GateOff)
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
