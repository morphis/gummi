package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func freeformCard(t *testing.T) Feature {
	t.Helper()
	id, err := NewID(KindFreeform, 12)
	if err != nil {
		t.Fatal(err)
	}
	return Feature{
		ID: id, Num: 12, Kind: KindFreeform, Title: "poke at the pty leak",
		Slug: "poke-at-the-pty-leak", Stage: StageOpen,
		BranchScheme: BranchSchemeKind,
		CreatedAt:    time.Now(), UpdatedAt: time.Now(),
	}
}

// TestAFreeformCardIsOffTheGraph: StageOpen is a legal stored stage and
// not a workflow stage, and those are two different questions. A card may
// hold it; a transition may not name it.
func TestAFreeformCardIsOffTheGraph(t *testing.T) {
	if !StageOpen.Valid() {
		t.Error("StageOpen is not storable — a freeform card could not be persisted")
	}
	if StageOpen.InGraph() {
		t.Error("StageOpen reports itself in the graph; nothing must be able to advance it")
	}
	for _, st := range Stages {
		if st == StageOpen {
			t.Fatal("StageOpen is listed in Stages — that list is the workflow, in order")
		}
		if !st.InGraph() {
			t.Errorf("%s is in Stages but not InGraph", st)
		}
	}
	// It groups with work in progress: a freeform card has no backlog
	// (minting one is starting it) and no gate to wait at.
	if got := StageOpen.SuperState(); got != SuperInProgress {
		t.Errorf("StageOpen.SuperState() = %q, want %q", got, SuperInProgress)
	}
	// Dependencies are settled from the moment it is minted: there is no
	// design stage in which taking one on could still change the work.
	if !AtOrPastCoding(StageOpen) {
		t.Error("a freeform card is not at-or-past coding; it is coding from its first turn")
	}
}

// TestAFreeformCardLandsWithoutVerifying is the whole trade the kind
// rests on, stated in one predicate: a card in the workflow lands on a
// verified branch, a freeform card lands on a human's read of its diff.
func TestAFreeformCardLandsWithoutVerifying(t *testing.T) {
	ff := freeformCard(t)
	if err := ff.MayLand(); err != nil {
		t.Errorf("a freeform card may not land: %v", err)
	}

	// Every other kind still has to earn it, at every stage short of a
	// verified one — which is DESIGN §10 D3's floor, unmoved.
	for _, st := range Stages {
		f := Feature{ID: "FD-001", Num: 1, Title: "t", Slug: "t", Stage: st}
		if st == StageVerify {
			if err := f.MayLand(); !errors.Is(err, ErrNotVerified) {
				t.Errorf("an unstamped card at %s: MayLand() = %v, want ErrNotVerified", st, err)
			}
			f.VerifiedAt = time.Now()
			if err := f.MayLand(); err != nil {
				t.Errorf("a verified card may not land: %v", err)
			}
			continue
		}
		if err := f.MayLand(); !errors.Is(err, ErrNotVerified) {
			t.Errorf("a feature at %s: MayLand() = %v, want ErrNotVerified", st, err)
		}
	}
}

// TestAFreeformCardHasNoArtifact: it is the one kind whose record is its
// thread, so both the noun and the path are empty — and a caller that
// joined that path onto a root would land on the root itself, which is
// why ArtifactNoun's emptiness is the documented signal to ask first.
func TestAFreeformCardHasNoArtifact(t *testing.T) {
	if got := KindFreeform.ArtifactNoun(); got != "" {
		t.Errorf("KindFreeform.ArtifactNoun() = %q, want no artifact at all", got)
	}
	ff := freeformCard(t)
	if got := ff.ArtifactPath(); got != "" {
		t.Errorf("ArtifactPath() = %q, want empty", got)
	}
	if !ff.IsFreeform() {
		t.Error("IsFreeform() is false on a freeform card")
	}
}

// TestAFreeformCardsIDAndBranch pins the spellings, because both end up
// somewhere permanent: the id in a commit subject, the branch in a
// checkout someone else's tooling may see.
func TestAFreeformCardsIDAndBranch(t *testing.T) {
	ff := freeformCard(t)
	if got := string(ff.ID); got != "FF-012" {
		t.Errorf("id = %q, want FF-012", got)
	}
	if got, err := ParseFeatureID("FF-012"); err != nil || got != ff.ID {
		t.Errorf("ParseFeatureID(FF-012) = %q, %v", got, err)
	}
	if got := FeatureID("FF-012").Kind(); got != KindFreeform {
		t.Errorf("FF-012 reads as kind %q", got)
	}
	if got := ff.BranchName(); got != "ff/poke-at-the-pty-leak" {
		t.Errorf("branch = %q, want ff/poke-at-the-pty-leak", got)
	}
}

// TestStageOpenAndKindFreeformImplyEachOther: a card half-way between the
// graph and outside it is what either half without the other would be, and
// both halves are refused at Validate rather than discovered later by a
// surface that cannot move the card.
func TestStageOpenAndKindFreeformImplyEachOther(t *testing.T) {
	ff := freeformCard(t)
	if err := ff.Validate(); err != nil {
		t.Fatalf("a minted freeform card does not validate: %v", err)
	}
	// Closed is the other stage it may hold: its ending is done, like
	// every other card's.
	ff.Stage = StageDone
	if err := ff.Validate(); err != nil {
		t.Errorf("a closed freeform card does not validate: %v", err)
	}
	for _, st := range []Stage{StageTodo, StagePlan, StageImplement, StageVerify} {
		ff.Stage = st
		if err := ff.Validate(); err == nil {
			t.Errorf("a freeform card at %s validated; it is at open or done, never a workflow stage", st)
		}
	}
	// And the converse: StageOpen has no outgoing edge, so a card in the
	// workflow parked there could never be moved by anything.
	other := Feature{ID: "FD-001", Num: 1, Title: "t", Slug: "t", Stage: StageOpen}
	if err := other.Validate(); err == nil {
		t.Error("a feature at stage open validated; nothing could ever advance it")
	}
}

// TestArtifactFileRefusesACardWithNoArtifact is the guard on the whole
// class of bug a pty drive found the expensive way: a freeform card's
// ArtifactPath is empty, filepath.Join(root, "") is root, and the delete
// path ran os.RemoveAll over it — deleting the entire workspace.
func TestArtifactFileRefusesACardWithNoArtifact(t *testing.T) {
	ff := freeformCard(t)
	if p, ok := ff.ArtifactFile("/ws"); ok || p != "" {
		t.Errorf("ArtifactFile on a freeform card = %q, %v; want \"\", false", p, ok)
	}
	// And it must never hand back the root, which is what the bare join did.
	if p, _ := ff.ArtifactFile("/ws"); p == "/ws" {
		t.Error("ArtifactFile handed back the root itself")
	}

	// Every other kind resolves as before.
	for _, k := range []Kind{KindFeature, KindBug, KindResearch, KindGoal} {
		f := Feature{ID: "FD-001", Num: 1, Kind: k, Title: "t", Slug: "t", Stage: StagePlan}
		if k != KindFeature {
			id, err := NewID(k, 1)
			if err != nil {
				t.Fatal(err)
			}
			f.ID = id
		}
		p, ok := f.ArtifactFile("/ws")
		if !ok {
			t.Errorf("kind %q has no artifact file", k)
			continue
		}
		if p != "/ws/"+f.ArtifactPath() {
			t.Errorf("kind %q: ArtifactFile = %q, want /ws/%s", k, p, f.ArtifactPath())
		}
	}
}

// TestOnlyASessionNamesItsOwnModel: a freeform card may carry the agent
// and model its session runs on; a card in the workflow may not, because
// its stages take theirs from its profile and nothing else.
func TestOnlyASessionNamesItsOwnModel(t *testing.T) {
	f := freeformCard(t)
	f.SessionBackend, f.SessionModel = "codex", "gpt-5"
	if err := f.Validate(); err != nil {
		t.Fatalf("a freeform card naming its session model was refused: %v", err)
	}
	id, err := NewID(KindFeature, 3)
	if err != nil {
		t.Fatal(err)
	}
	stage := Feature{
		ID: id, Num: 3, Kind: KindFeature, Title: "configurable retries",
		Slug: "configurable-retries", Stage: StageTodo,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
		SessionModel: "gpt-5",
	}
	if err := stage.Validate(); err == nil {
		t.Error("a feature naming a session model was accepted; its stages take their models from its profile")
	}
}

// TestOnlyASessionIsContinuedAsASpec: ContinuedAs is a freeform card's
// alone, and it is the one handed-off card that may not land after all.
func TestOnlyASessionIsContinuedAsASpec(t *testing.T) {
	ff := freeformCard(t)
	ff.ContinuedAs = "FD-002"
	if err := ff.Validate(); err != nil {
		t.Errorf("a session continued as a spec does not validate: %v", err)
	}
	if err := ff.MayLandAfterAll(); !errors.Is(err, ErrContinued) || !strings.Contains(err.Error(), "FD-002") {
		t.Errorf("MayLandAfterAll = %v, want ErrContinued naming FD-002", err)
	}
	ff.ContinuedAs = ""
	if err := ff.MayLandAfterAll(); err != nil {
		t.Errorf("a plain hand-off may not land after all: %v", err)
	}

	f := Feature{ID: "FD-001", Num: 1, Kind: KindFeature, Title: "t", Slug: "t", Stage: StagePlan, ContinuedAs: "FD-002"}
	if err := f.Validate(); err == nil {
		t.Error("a feature card validates carrying ContinuedAs")
	}
}
