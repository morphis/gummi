package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestCreateFeatureSeedsDraft: a description that runs past its first
// line seeds the spec draft at creation — the full text lands in the
// Problem section verbatim, so brainstorm starts from what the user
// wrote instead of a blank template. A title-sized description seeds
// nothing and first open still gets the blank template's prompts.
func TestCreateFeatureSeedsDraft(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	desc := "Dark mode toggle\n\nRespect the OS preference by default.\nPersist an explicit override in config."
	if msg := m.createFeature(formResult{Desc: desc})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f, err := store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Dark mode toggle" {
		t.Errorf("card title = %q, want the first line", f.Title)
	}
	raw, err := os.ReadFile(filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f)))
	if err != nil {
		t.Fatalf("seeded draft missing: %v", err)
	}
	if !strings.Contains(string(raw), "## Problem\n\n"+desc) {
		t.Errorf("draft Problem section lost the description:\n%s", raw)
	}

	// one line carries no more than the card already does — no draft
	if msg := m.createFeature(formResult{Desc: "Add a healthz endpoint"})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f2 := &domain.Feature{ID: "FD-002", Slug: "add-a-healthz-endpoint"}
	if _, err := os.Stat(filepath.Join(ws.DraftsDir(), spec.DraftFilename(f2))); !os.IsNotExist(err) {
		t.Errorf("single-line description seeded a draft (stat err = %v)", err)
	}
}
