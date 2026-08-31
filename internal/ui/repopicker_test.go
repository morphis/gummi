package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

func TestRepoPickerCycles(t *testing.T) {
	f := domain.Feature{ID: "FD-001"}
	var submitted string
	var called bool
	d := newRepoPickerDialog(f, []string{"a", "b"}, func(repo string) tea.Cmd {
		submitted = repo
		called = true
		return nil
	})

	// the candidates are the configured names and nothing else — a
	// workspace with `repos:` has no default to offer, so a card carrying
	// the empty name starts on the first configured repo, not on a
	// "default" entry that could never resolve.
	if len(d.candidates) != 2 || d.candidates[0] != "a" {
		t.Fatalf("candidates = %q, want [a b] with no default entry", d.candidates)
	}
	if d.idx != 0 {
		t.Fatalf("initial idx = %d, want 0 for empty repo", d.idx)
	}
	if _, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}); d.idx != 1 {
		t.Fatalf("forward idx = %d, want 1", d.idx)
	}
	closed, _ := d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !closed || !called || submitted != "b" {
		t.Fatalf("submit: closed=%v called=%v submitted=%q, want true/true/%q", closed, called, submitted, "b")
	}

	// backward from idx 0 wraps to the last candidate
	d = newRepoPickerDialog(f, []string{"a", "b"}, func(string) tea.Cmd { return nil })
	if _, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft}); d.idx != 1 {
		t.Fatalf("backward wrap idx = %d, want 1", d.idx)
	}
	if d.candidates[d.idx] != "b" {
		t.Fatalf("backward wrap candidate = %q, want %q", d.candidates[d.idx], "b")
	}

	// a card already on a named repo opens on that repo
	d = newRepoPickerDialog(domain.Feature{ID: "FD-002", Repo: "b"}, []string{"a", "b"}, func(string) tea.Cmd { return nil })
	if d.idx != 1 {
		t.Fatalf("idx for a card on repo b = %d, want 1", d.idx)
	}

	// esc cancels without calling onSubmit
	d = newRepoPickerDialog(f, []string{"a", "b"}, func(string) tea.Cmd { t.Fatal("esc should not submit"); return nil })
	closed, _ = d.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Fatal("esc should close the dialog")
	}
}
