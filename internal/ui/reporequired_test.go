package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestRepoPickerNeverOffersDefaultBesideNames: a workspace with `repos:`
// has no default repository, so the picker offers the configured names
// and nothing else — even if the caller claims a default resolves. The
// two are mutually exclusive in config, and a "default" option here would
// submit a name that only fails later at worktree creation.
func TestRepoPickerNeverOffersDefaultBesideNames(t *testing.T) {
	p := newRepoPicker([]string{"a", "b"}, true)
	if got := p.options(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("options = %q, want exactly [a b]", got)
	}
}

// TestRepoPickerStartsUnset: with a real choice to make, nothing is
// selected until the user selects it.
func TestRepoPickerStartsUnset(t *testing.T) {
	p := newRepoPicker([]string{"a", "b"}, false)
	if !p.multi() {
		t.Fatal("two repos should be a choice")
	}
	if p.chosen() {
		t.Error("picker should start unselected")
	}
	if !p.needsChoice() {
		t.Error("an unselected multi picker needs a choice")
	}
	if got := p.name(); got != "" {
		t.Errorf("unselected name = %q, want empty", got)
	}
	if got := p.label(); got != repoUnsetLabel {
		t.Errorf("unselected label = %q, want %q", got, repoUnsetLabel)
	}

	// → off the unset state lands on the first repo, not the second
	p.cycle(1)
	if !p.chosen() || p.name() != "a" {
		t.Errorf("after one forward cycle: chosen=%v name=%q, want true/a", p.chosen(), p.name())
	}
	if p.needsChoice() {
		t.Error("a chosen picker no longer needs a choice")
	}
	p.cycle(1)
	if p.name() != "b" {
		t.Errorf("after two forward cycles name = %q, want b", p.name())
	}

	// ← off the unset state lands on the last repo
	q := newRepoPicker([]string{"a", "b"}, false)
	q.cycle(-1)
	if q.name() != "b" {
		t.Errorf("backward off unset = %q, want b", q.name())
	}
}

// TestRepoPickerSingleRepoNeedsNoChoice: one repository — named or the
// lone workspace default — is not a choice, so there is no tab stop and
// the one repo is what the card gets. A configured name is still shown
// (see TestSingleNamedRepoStillRenders); the anonymous default is not.
func TestRepoPickerSingleRepoNeedsNoChoice(t *testing.T) {
	sole := newRepoPicker([]string{"only"}, false)
	if sole.multi() || sole.needsChoice() {
		t.Error("a single named repo is not a choice")
	}
	if got := sole.name(); got != "only" {
		t.Errorf("sole named repo = %q, want only", got)
	}

	def := newRepoPicker(nil, true)
	if def.multi() || def.needsChoice() {
		t.Error("the lone workspace default is not a choice")
	}
	if got := def.name(); got != "" {
		t.Errorf("workspace default name = %q, want empty", got)
	}
}

// TestCardFormRefusesUnchosenRepo: the door will not create a card until
// a repository is named. It opens on the repo row, since that is the one
// field with no default; enter there is refused and focus stays. Once
// chosen, the card carries that repo.
func TestCardFormRefusesUnchosenRepo(t *testing.T) {
	var got formResult
	var created bool
	f := newCardForm(domain.KindFeature, nil, []string{"a", "b"}, false, "", nil, 0, func(res formResult) tea.Cmd {
		got, created = res, true
		return nil
	})
	if f.focus != cardStopRepo {
		t.Fatalf("focus = %d, want the repo row", f.focus)
	}
	f.SetText("teach the board to whistle")

	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || created {
		t.Fatal("form submitted without a repository")
	}
	if f.errText != repoUnchosenErr {
		t.Errorf("errText = %q, want %q", f.errText, repoUnchosenErr)
	}
	if view := f.View(theme.New(theme.GummiDark()), 80, 24); !strings.Contains(view, repoUnchosenErr) {
		t.Errorf("refusal not shown in the dialog:\n%s", view)
	}

	// choose the second repo, then create
	f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || !created {
		t.Fatalf("form did not submit after choosing: done=%v created=%v err=%q", done, created, f.errText)
	}
	if got.Repo != "b" {
		t.Errorf("created repo = %q, want b", got.Repo)
	}
}

// TestCardFormRepoRowNeverEatsTyping: on the repo row, digits and arrows
// choose; any other printable key falls through to the text, so a person
// who starts describing the card never cycles anything by accident. The
// last repo chosen this session is the preselect.
func TestCardFormRepoRowNeverEatsTyping(t *testing.T) {
	f := newCardForm(domain.KindFeature, nil, []string{"a", "b", "c"}, false, "", nil, 0, nil)
	f.HandleKey(tea.KeyPressMsg{Code: '3', Text: "3"})
	if f.repo.name() != "c" {
		t.Errorf("digit 3 chose %q, want c", f.repo.name())
	}
	f.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if f.focus != cardStopText || f.Text() != "x" {
		t.Errorf("typing on the repo row: focus=%d text=%q", f.focus, f.Text())
	}
	if f.repo.name() != "c" {
		t.Errorf("typing changed the repo to %q", f.repo.name())
	}
	sticky := newCardForm(domain.KindFeature, nil, []string{"a", "b", "c"}, false, "b", nil, 0, nil)
	if sticky.repo.name() != "b" || sticky.focus != cardStopText {
		t.Errorf("last repo preselect: repo=%q focus=%d", sticky.repo.name(), sticky.focus)
	}
	if bogus := newCardForm(domain.KindFeature, nil, []string{"a", "b"}, false, "zzz", nil, 0, nil); bogus.repo.chosen() {
		t.Error("an unconfigured last repo should not preselect")
	}
}

// TestSingleRepoFormsSubmitUnprompted: the forced choice must not reach
// the ordinary single-repo workspace — the field is hidden there and
// enter still creates a card on the first press.
func TestSingleRepoFormsSubmitUnprompted(t *testing.T) {
	var created bool
	f := newCardForm(domain.KindFeature, nil, nil, true, "", nil, 0, func(formResult) tea.Cmd {
		created = true
		return nil
	})
	f.SetText("a card in the only repo there is")
	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || !created {
		t.Fatalf("single-repo form did not submit: done=%v created=%v err=%q", done, created, f.errText)
	}
}

// TestSingleNamedRepoStillRenders: a `repos:` workspace with exactly one
// entry has nothing to pick, but every creation dialog still prints the
// repository row. Hiding it left the dialog silent about where the card
// was about to be created, which reads as "repos: was ignored" rather
// than "there is only one".
func TestSingleNamedRepoStillRenders(t *testing.T) {
	s := theme.New(theme.GummiDark())
	views := map[string]string{
		"card":   ansi.Strip(newCardForm(domain.KindFeature, nil, []string{"lxd"}, false, "", nil, 0, nil).View(s, 80, 24)),
		"ingest": ansi.Strip(newIngestForm(nil, []string{"lxd"}, false, nil).View(s, 80, 24)),
	}
	for name, view := range views {
		if !strings.Contains(view, "repo") || !strings.Contains(view, "lxd") {
			t.Errorf("%s dialog does not name its sole repository:\n%s", name, view)
		}
	}
}

// TestLoneWorkspaceDefaultRendersNoRepoRow: the unconfigured single-repo
// workspace has no repository name to report — "default" is not one — so
// the row stays absent and the dialog keeps its space for real fields.
func TestLoneWorkspaceDefaultRendersNoRepoRow(t *testing.T) {
	s := theme.New(theme.GummiDark())
	views := map[string]string{
		"card":   ansi.Strip(newCardForm(domain.KindFeature, nil, nil, true, "", nil, 0, nil).View(s, 80, 24)),
		"ingest": ansi.Strip(newIngestForm(nil, nil, true, nil).View(s, 80, 24)),
	}
	for name, view := range views {
		if strings.Contains(view, "repo ") || strings.Contains(view, "repo:") {
			t.Errorf("%s dialog renders a repo row for the anonymous default:\n%s", name, view)
		}
	}
}

// TestSingleNamedRepoIsNotATabStop: the row a single-repo workspace shows
// is read-only. Tabbing through the dialog must never land on it, because
// ←/→ there would do nothing.
func TestSingleNamedRepoIsNotATabStop(t *testing.T) {
	f := newCardForm(domain.KindFeature, nil, []string{"lxd"}, false, "", nil, 0, nil)
	for _, s := range f.stops() {
		if s == cardStopRepo {
			t.Error("the read-only repo row is a tab stop")
		}
	}
	seen := map[int]bool{}
	for i := 0; i < 12; i++ {
		f.advanceFocus(1)
		seen[f.focus] = true
	}
	if seen[cardStopRepo] {
		t.Error("focus landed on the read-only repo row")
	}
}
