package cardmint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// inspector stands in for the git-backed callback the real callers wire.
// cardmint never shells out to git, which is exactly why the callback
// exists, so the tests supply the answer directly.
func inspector(w domain.AdoptedWork, err error) func(string) (domain.AdoptedWork, error) {
	return func(branch string) (domain.AdoptedWork, error) {
		if err != nil {
			return domain.AdoptedWork{}, err
		}
		w.Branch = branch
		return w, nil
	}
}

func theirWork() domain.AdoptedWork {
	return domain.AdoptedWork{
		Base:    "main",
		Commits: []string{"abc1234 start the parser", "def5678 handle the empty case"},
		Stat:    " parser.go | 42 +++++++",
		Behind:  7,
	}
}

func TestMintOntoAnExistingBranch(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()

	f, err := Mint(ctx, store, ws, Input{
		Kind:           domain.KindFeature,
		Description:    "Finish the parser\n\nTheir branch stops at the empty case.",
		Envelope:       2400,
		Adopt:          "feat/their-parser",
		InspectAdopted: inspector(theirWork(), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Adopted() {
		t.Fatalf("card is not marked adopted: scheme = %q", f.BranchScheme)
	}
	// The branch is the one they named, verbatim — not a derived one that
	// happens to look similar.
	if got := f.BranchName(); got != "feat/their-parser" {
		t.Errorf("branch = %s, want the adopted ref", got)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("a minted adopted card does not validate: %v", err)
	}

	// The inherited work is in the artifact, because the architect has to
	// read the branch before it can plan a rework of it.
	draft, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatal(err)
	}
	body := string(draft)
	for _, want := range []string{
		"## Inherited work",
		"feat/their-parser",
		"def5678 handle the empty case",
		"7 commits behind main",
		"parser.go",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("draft is missing %q:\n%s", want, body)
		}
	}
}

// A one-line description normally seeds no draft at all. An adopted card
// must get one anyway: the inherited work is the card's whole starting
// point, and a blank template would throw it away.
func TestMintOntoABranchAlwaysWritesTheDraft(t *testing.T) {
	store, ws := newTestWorkspace(t)
	f, err := Mint(context.Background(), store, ws, Input{
		Kind:           domain.KindFeature,
		Description:    "Finish the parser",
		Adopt:          "feat/their-parser",
		InspectAdopted: inspector(theirWork(), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatalf("an adopted card was minted with no draft: %v", err)
	}
	if !strings.Contains(string(draft), "## Inherited work") {
		t.Errorf("draft has no inherited-work section:\n%s", draft)
	}
}

func TestOneBranchOneCard(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()
	in := Input{
		Kind:           domain.KindFeature,
		Description:    "Finish the parser",
		Adopt:          "feat/their-parser",
		InspectAdopted: inspector(theirWork(), nil),
	}
	if _, err := Mint(ctx, store, ws, in); err != nil {
		t.Fatal(err)
	}
	// A second card onto the same branch would be two cards committing
	// over each other with no way to tell whose work a diff belonged to.
	in.Description = "Also finish the parser"
	_, err := Mint(ctx, store, ws, in)
	if err == nil || !strings.Contains(err.Error(), "one branch, one card") {
		t.Errorf("second adoption of the same branch: err = %v, want a refusal", err)
	}
}

func TestAdoptionRefusesWhatItCannotVerify(t *testing.T) {
	store, ws := newTestWorkspace(t)
	ctx := context.Background()
	base := Input{Kind: domain.KindFeature, Description: "Finish the parser"}

	seqBefore := readSeq(t, ws.SeqFile())

	// No inspector wired: minting onto an unverified ref is the bug the
	// callback exists to catch, so it fails closed exactly as RequireRepo does.
	in := base
	in.Adopt = "feat/their-parser"
	if _, err := Mint(ctx, store, ws, in); err == nil {
		t.Error("minted onto a branch with no way to verify it")
	}

	// The inspector's refusal is the mint's refusal.
	in.InspectAdopted = inspector(domain.AdoptedWork{}, errors.New("no local branch feat/their-parser"))
	if _, err := Mint(ctx, store, ws, in); err == nil || !strings.Contains(err.Error(), "no local branch") {
		t.Errorf("err = %v, want the inspector's own message", err)
	}

	// A research card runs in a detached scratch tree and holds no branch.
	in = base
	in.Kind, in.Adopt = domain.KindResearch, "feat/their-parser"
	in.InspectAdopted = inspector(theirWork(), nil)
	if _, err := Mint(ctx, store, ws, in); err == nil {
		t.Error("a research card adopted a branch it can never check out")
	}

	// Every refusal above happens before a sequence number is spent: a
	// failed adoption must not leave a gap in the card numbering.
	if after := readSeq(t, ws.SeqFile()); after != seqBefore {
		t.Errorf("seq = %s after three refused mints, want %s untouched", after, seqBefore)
	}
}
