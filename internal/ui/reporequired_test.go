package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
// lone workspace default — is not a choice, so the field stays hidden and
// the one repo is what the card gets.
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

// TestFeatureFormRefusesUnchosenRepo: the new-feature dialog will not
// create a card until a repository is named, and it points focus at the
// field it is complaining about. Once chosen, the card carries that repo.
func TestFeatureFormRefusesUnchosenRepo(t *testing.T) {
	var got formResult
	var created bool
	f := newFeatureForm(nil, []string{"a", "b"}, false, 0, func(res formResult) tea.Cmd {
		got, created = res, true
		return nil
	})
	f.desc.SetValue("teach the board to whistle")

	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || created {
		t.Fatal("form submitted without a repository")
	}
	if f.errText != repoUnchosenErr {
		t.Errorf("errText = %q, want %q", f.errText, repoUnchosenErr)
	}
	if f.focus != featureFieldRepo {
		t.Errorf("focus = %d, want the repo field (%d)", f.focus, featureFieldRepo)
	}
	if view := f.View(theme.New(theme.GummiDark()), 80, 24); !strings.Contains(view, repoUnchosenErr) {
		t.Errorf("refusal not shown in the dialog:\n%s", view)
	}

	// choose the second repo, then create
	f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || !created {
		t.Fatalf("form did not submit after choosing: done=%v created=%v", done, created)
	}
	if got.Repo != "b" {
		t.Errorf("created repo = %q, want b", got.Repo)
	}
}

// TestBugFormRefusesUnchosenRepo mirrors the feature form: the same
// refusal guards every creation dialog.
func TestBugFormRefusesUnchosenRepo(t *testing.T) {
	var created bool
	f := newBugForm(nil, []string{"a", "b"}, false, 0, func(bugFormResult) tea.Cmd {
		created = true
		return nil
	})
	f.desc.SetValue("the board forgets the selection")

	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done || created {
		t.Fatal("bug form submitted without a repository")
	}
	if f.errText != repoUnchosenErr {
		t.Errorf("errText = %q, want %q", f.errText, repoUnchosenErr)
	}
	if f.focus != bugFieldRepo {
		t.Errorf("focus = %d, want the repo field (%d)", f.focus, bugFieldRepo)
	}

	f.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || !created {
		t.Fatalf("bug form did not submit after choosing: done=%v created=%v", done, created)
	}
}

// TestSingleRepoFormsSubmitUnprompted: the forced choice must not reach
// the ordinary single-repo workspace — the field is hidden there and
// enter still creates a card on the first press.
func TestSingleRepoFormsSubmitUnprompted(t *testing.T) {
	var created bool
	f := newFeatureForm(nil, nil, true, 0, func(formResult) tea.Cmd {
		created = true
		return nil
	})
	f.desc.SetValue("a card in the only repo there is")
	if done, _ := f.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || !created {
		t.Fatalf("single-repo form did not submit: done=%v created=%v err=%q", done, created, f.errText)
	}
}
