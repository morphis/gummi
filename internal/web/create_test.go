package web

import (
	"net/http"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// The form offers what the TUI's card form offers, and a card created
// from it waits on the cards it names and sits on the stack it names.
func TestCreateACardWithDependenciesAndAStack(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	below := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Row loader"})

	var form webapi.Form
	if st := h.call(http.MethodGet, "/api/form", nil, &form); st != http.StatusOK {
		t.Fatalf("form = %d", st)
	}
	kinds := make([]string, 0, len(form.Kinds))
	for _, k := range form.Kinds {
		kinds = append(kinds, k.Value)
	}
	if !slices.Equal(kinds, []string{"feature", "bug", "research", "research:diagnosis", "goal", "freeform"}) {
		t.Errorf("kinds = %v", kinds)
	}
	if form.Envelope <= 0 || len(form.Severities) == 0 || len(form.Profiles) == 0 {
		t.Errorf("form = %+v", form)
	}
	if !slices.ContainsFunc(form.Dependable, func(c webapi.CardRef) bool { return c.ID == below.ID }) ||
		!slices.ContainsFunc(form.Stackable, func(c webapi.CardRef) bool { return c.ID == below.ID }) {
		t.Errorf("the form does not offer %s to wait on or stack on: %+v", below.ID, form)
	}

	c := h.create(webapi.CreateCardRequest{
		Kind: "feature", Title: "Row cache", Description: "cache the rows",
		DependsOn: []string{below.ID}, StackOn: below.ID,
	})
	c = h.waitCard(c.ID, "the stack", func(c webapi.Card) bool { return c.Stack != nil })
	if c.Stack.Pos != 1 || c.Stack.Of != 2 || c.Stack.ID == "" {
		t.Errorf("stack = %+v, want second of two", c.Stack)
	}
	deps, err := h.store.ListDependencies(t.Context(), domain.FeatureID(c.ID))
	if err != nil || !slices.Equal(deps, []domain.FeatureID{domain.FeatureID(below.ID)}) {
		t.Errorf("dependencies = %v (%v)", deps, err)
	}

	// what the form refuses, the request is refused for
	var e webapi.Error
	if st := h.call(http.MethodPost, "/api/cards", webapi.CreateCardRequest{Kind: "feature", Title: "x", DependsOn: []string{"FD-099"}}, &e); st != http.StatusBadRequest {
		t.Errorf("a dependency on nothing = %d %+v", st, e)
	}
	if st := h.call(http.MethodPost, "/api/cards", webapi.CreateCardRequest{Kind: "feature", Title: "!!!"}, &e); st != http.StatusBadRequest || e.Error != "the first line needs a letter or digit to make a title" {
		t.Errorf("a title with no word in it = %d %+v, want the form's refusal", st, e)
	}
	if st := h.call(http.MethodPost, "/api/cards", webapi.CreateCardRequest{Kind: "epic", Title: "x"}, &e); st != http.StatusBadRequest {
		t.Errorf("an unknown kind = %d", st)
	}
}

// The form says which branches a card may adopt, with the refusal each
// other one would meet: held by a card, the branch it lands on, nothing
// of its own past that branch.
func TestFormSaysWhichBranchesCanBeAdopted(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ok"))
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", h.root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	for _, b := range []string{"feat/dark", "feat/wave"} {
		git("checkout", "-q", "-b", b, "main")
		git("commit", "-q", "--allow-empty", "-m", "work on "+b)
	}
	git("checkout", "-q", "main")
	git("branch", "stale", "main")
	held := h.create(webapi.CreateCardRequest{Kind: "feature", Title: "Dark mode", Adopt: "feat/dark"})

	var form webapi.Form
	if st := h.call(http.MethodGet, "/api/form", nil, &form); st != http.StatusOK {
		t.Fatalf("form = %d", st)
	}
	got := map[string]webapi.AdoptChoice{}
	for _, a := range form.Adoptable {
		got[a.Branch] = a
	}
	want := map[string]webapi.AdoptChoice{
		"main":      {Branch: "main", Why: "the branch it lands on"},
		"feat/dark": {Branch: "feat/dark", Why: held.ID + " has it", Held: held.ID},
		"feat/wave": {Branch: "feat/wave"},
		"stale":     {Branch: "stale", Why: "no commits past main"},
	}
	for b, w := range want {
		if got[b] != w {
			t.Errorf("%s = %+v, want %+v", b, got[b], w)
		}
	}
	if len(form.Adoptable) != len(form.Branches) {
		t.Errorf("adoptable has %d rows for %d branches", len(form.Adoptable), len(form.Branches))
	}
}

// A freeform card starts working the moment it exists, as it does from
// the TUI's form: its description is its first turn.
func TestCreateAFreeformCardStartsIt(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("on it"))
	c := h.create(webapi.CreateCardRequest{Kind: "freeform", Title: "Poke at the pty leak", Description: "find where it leaks"})
	if c.Kind != string(domain.KindFreeform) {
		t.Fatalf("kind = %s", c.Kind)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ff := h.eng.Freeform(domain.FeatureID(c.ID)); ff != nil && len(ff.Snapshot().Transcript) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the freeform card never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.card(c.ID).Composer.Route; got != webapi.RouteFreeform {
		t.Errorf("a freeform card's composer routes to %s", got)
	}
	// the opening turn is the line of whoever created the card — Simon —
	// not an anonymous "you" every viewer would read as their own
	if first := h.eng.Freeform(domain.FeatureID(c.ID)).Snapshot().Transcript[0]; first.By != state.PersonActor("Simon") {
		t.Errorf("the opening turn is by %q, want Simon", first.By)
	}
}
