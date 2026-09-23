package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// stackDoor is the new-card dialog with a board's worth of cards on the
// stack row and repos to choose between, focused on that row.
func stackDoor(t *testing.T, ct domain.CardType, cands []stackCand, repos []string) *cardForm {
	t.Helper()
	d := newCardForm(ct, []string{"thrifty"}, repos, false, "", nil, 2400, nil)
	d.setStackCands(cands)
	d.expanded = true
	d.setFocus(cardStopStack)
	return d
}

func stackCandAt(id domain.FeatureID, repo string, ago time.Duration) stackCand {
	return stackCand{ID: id, Title: string(id) + " title", Repo: repo, Touched: time.Now().Add(-ago)}
}

// TestTheStackRowOffersTheBoardsCards is the whole point of the row: a
// dialog opened with `n` can still stack, most recently touched card
// first, and cycles back to standalone.
func TestTheStackRowOffersTheBoardsCards(t *testing.T) {
	d := stackDoor(t, domain.CardType{Kind: domain.KindFeature}, []stackCand{
		stackCandAt("FD-001", "", time.Hour),
		stackCandAt("FD-002", "", time.Minute),
	}, nil)

	if got := d.stackChoices(); len(got) != 3 || got[0] != "standalone" || got[1] != "on FD-002" {
		t.Fatalf("choices = %v, want standalone then the most recent card", got)
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "FD-002" {
		t.Fatalf("one right = %q, want FD-002 — the card touched last", d.stackOnto)
	}
	if d.stackLabel != "FD-002 title" {
		t.Errorf("label = %q, want the candidate's title", d.stackLabel)
	}
	if got := d.forkFrom(); got != "FD-002's branch" {
		t.Errorf("becomes readout = %q, want the card's branch", got)
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "FD-001" {
		t.Fatalf("two rights = %q, want FD-001", d.stackOnto)
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "" || d.stackLabel != "" {
		t.Fatalf("the cycle should wrap back to standalone, got %q/%q", d.stackOnto, d.stackLabel)
	}
}

// TestTheStackRowKeepsWhatTGaveIt: T preselects a card that the row
// still holds, still names, and can still be cycled off and back onto.
func TestTheStackRowKeepsWhatTGaveIt(t *testing.T) {
	d := stackDoor(t, domain.CardType{Kind: domain.KindFeature}, []stackCand{
		stackCandAt("FD-001", "", time.Hour),
	}, nil)
	d.stackOn("FD-001", "token parser", "")

	if d.stackIdx() != 1 {
		t.Fatalf("stackIdx = %d, want the preselected card", d.stackIdx())
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "" {
		t.Fatalf("right off the only card = %q, want standalone", d.stackOnto)
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "FD-001" {
		t.Fatalf("right again = %q, want the offer back", d.stackOnto)
	}
}

// TestTheStackRowCarriesAnOfferTheBoardDoesNotHave: T can name a card
// the candidate list is missing. The row must still show it and still be
// able to step off it, or the gesture would be silently dropped.
func TestTheStackRowCarriesAnOfferTheBoardDoesNotHave(t *testing.T) {
	d := stackDoor(t, domain.CardType{Kind: domain.KindFeature}, nil, nil)
	d.stackOn("FD-404", "vanished", "")

	if got := d.stackChoices(); len(got) != 2 || got[1] != "on FD-404" {
		t.Fatalf("choices = %v, want the offer carried", got)
	}
	if d.stackIdx() != 1 {
		t.Errorf("stackIdx = %d, want the offer selected", d.stackIdx())
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "" {
		t.Errorf("the carried offer should still be escapable, got %q", d.stackOnto)
	}
}

// TestTheStackRowIsOneRepository: a stack is one repository (DESIGN
// §18.1), so only the chosen repo's cards are on offer — and a choice
// left stale by a later move of the repo row is refused at submit rather
// than surviving to become the store's ErrStackRepoMismatch warning,
// which arrives only after the card has been created.
func TestTheStackRowIsOneRepository(t *testing.T) {
	d := stackDoor(t, domain.CardType{Kind: domain.KindFeature}, []stackCand{
		stackCandAt("FD-001", "api", time.Hour),
		stackCandAt("FD-002", "web", time.Minute),
	}, []string{"api", "web"})
	d.repo.selectName("api")

	if got := d.stackChoices(); len(got) != 2 || got[1] != "on FD-001" {
		t.Fatalf("choices = %v, want only the chosen repo's card", got)
	}
	d.HandleKey(keyRight)
	if d.stackOnto != "FD-001" {
		t.Fatalf("stackOnto = %q, want FD-001", d.stackOnto)
	}

	d.repo.selectName("web")
	d.text.SetValue("a card in the other repo")
	if done, _ := d.submit(false); done {
		t.Fatal("submit should refuse a card stacked across repositories")
	}
	if !strings.Contains(d.errText, "one repository") {
		t.Errorf("errText = %q, want it to name the repository rule", d.errText)
	}
	if d.focus != cardStopStack {
		t.Errorf("focus = %d, want the row the refusal is about", d.focus)
	}
	// The stale choice stays visible, so the refusal is about something
	// the reader can see and undo.
	if d.stackIdx() == 0 {
		t.Error("the refused card should still be the row's answer")
	}
}

// TestTheStackRowSkipsKindsWithNoBranch: research cards and goals have
// no branch of their own to fork, so they get no row at all rather than
// one that can only say no.
func TestTheStackRowSkipsKindsWithNoBranch(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindResearch, domain.KindGoal} {
		d := newCardForm(domain.CardType{Kind: kind}, []string{"thrifty"}, nil, false, "", nil, 2400, nil)
		d.expanded = true
		for _, stop := range d.stops() {
			if stop == cardStopStack {
				t.Errorf("%s offers a stack row", kind)
			}
		}
		if strings.Contains(d.View(theme.New(theme.GummiDark()), 80, 40), "stack") {
			t.Errorf("%s renders a stack row", kind)
		}
	}
}

// TestStackCandsAreTheCardsWithABranch: the shell's side of the same
// rule, plus the one a goal's members get — they share the goal's
// branch instead of stacking. A done card stays on offer: done is a
// verified branch, not a landed one, and the next slice goes on top of
// it while it waits for review.
func TestStackCandsAreTheCardsWithABranch(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageDone, UpdatedAt: time.Now().Add(-time.Hour)}},
		{F: domain.Feature{ID: "RS-001", Kind: domain.KindResearch}},
		{F: domain.Feature{ID: "GL-001", Kind: domain.KindGoal}},
		{F: domain.Feature{ID: "FD-002", Kind: domain.KindFeature, GoalID: "GL-001"}},
		{F: domain.Feature{ID: "BG-001", Kind: domain.KindBug, UpdatedAt: time.Now()}},
	}
	cands := m.stackCands()
	var got []domain.FeatureID
	for _, c := range cands {
		got = append(got, c.ID)
	}
	if len(got) != 2 || got[0] != "FD-001" || got[1] != "BG-001" {
		t.Fatalf("stackCands = %v, want the two cards with a branch of their own", got)
	}
	// and the row puts the most recently touched of them first.
	d := stackDoor(t, domain.CardType{Kind: domain.KindFeature}, cands, nil)
	if choices := d.stackChoices(); len(choices) != 3 || choices[1] != "on BG-001" {
		t.Errorf("choices = %v, want the most recently touched card first", choices)
	}
}
