package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestDialogDescSize covers the sizing contract's two goldens plus a
// boundary case at each clamp edge.
func TestDialogDescSize(t *testing.T) {
	const staticRows = 8 // matches the old shared dialogStaticRows constant this test was written against
	cases := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"golden small area", 60, 20, 54, 7},
		{"golden large area", 200, 60, 104, 20},
		{"width just under the floor's trigger point stays at the floor", 51, 20, descWidthMin, 7},
		{"width past the ceiling's trigger point clamps to the ceiling", 111, 20, descWidthMax, 7},
		{"height just under the floor's trigger point stays at the floor", 60, 16, 54, descHeightMin},
		{"height past the ceiling's trigger point clamps to the ceiling", 60, 100, 54, descHeightMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH := dialogDescSize(c.w, c.h, staticRows)
			if gotW != c.wantW || gotH != c.wantH {
				t.Errorf("dialogDescSize(%d, %d, %d) = (%d, %d), want (%d, %d)", c.w, c.h, staticRows, gotW, gotH, c.wantW, c.wantH)
			}
		})
	}
}

// TestFeatureFormSizing renders the feature dialog at a small and a large
// draw area and asserts the description editor's own rendered size
// matches the sizing helper's output exactly for that area.
func TestFeatureFormSizing(t *testing.T) {
	styles := theme.New(theme.GummiDark())
	for _, area := range []struct{ w, h int }{{60, 20}, {200, 60}} {
		form := newFeatureForm(nil, nil, false, 0, func(formResult) tea.Cmd { return nil })
		form.View(styles, area.w, area.h)
		wantW, wantH := dialogDescSize(area.w, area.h, 9) // no repo field: base static rows only
		assertDescRendersAt(t, form.desc.View(), area.w, area.h, wantW, wantH)
	}
}

// assertDescRendersAt checks a description textarea's rendered View()
// output — every line the given rendered width, and the given number of
// lines — matching dialogDescSize's output for the area it was sized at.
func assertDescRendersAt(t *testing.T, rendered string, areaW, areaH, wantW, wantH int) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	if len(lines) != wantH {
		t.Errorf("at %dx%d: desc rendered %d lines, want %d", areaW, areaH, len(lines), wantH)
	}
	for i, l := range lines {
		if gotW := lipgloss.Width(l); gotW != wantW {
			t.Errorf("at %dx%d: desc line %d width = %d, want %d", areaW, areaH, i, gotW, wantW)
		}
	}
}

// TestFeatureFormCharLimit: the description accepts up to 4096
// characters without truncation.
func TestFeatureFormCharLimit(t *testing.T) {
	form := newFeatureForm(nil, nil, false, 0, func(formResult) tea.Cmd { return nil })
	long := strings.Repeat("a", 4096)
	form.desc.SetValue(long)
	if got := len(form.desc.Value()); got != 4096 {
		t.Fatalf("description length = %d, want 4096 (not truncated)", got)
	}
}

// TestFormEnvelopeDefault: the envelope input pre-fills from the global
// default, so the intended budget is visible before the user edits it.
func TestFormEnvelopeDefault(t *testing.T) {
	form := newFeatureForm(nil, nil, false, 5000, func(formResult) tea.Cmd { return nil })
	if got := form.env.Value(); got != "5000" {
		t.Fatalf("envelope pre-fill = %q, want \"5000\"", got)
	}
}

// TestFormEnvelopeCustom: a valid custom value parses into an explicit
// Envelope pointer on submit.
func TestFormEnvelopeCustom(t *testing.T) {
	var got *int
	form := newFeatureForm(nil, nil, false, 5000, func(res formResult) tea.Cmd {
		got = res.Envelope
		return nil
	})
	form.desc.SetValue("dark mode")
	form.env.SetValue("7500")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got == nil || *got != 7500 {
		t.Fatalf("Envelope = %v, want explicit 7500", got)
	}
}

// TestFormEnvelopeNegativeBlocksSubmit: a negative value is rejected in
// the enter branch, showing an error and never reaching onSubmit.
func TestFormEnvelopeNegativeBlocksSubmit(t *testing.T) {
	submitted := false
	form := newFeatureForm(nil, nil, false, 5000, func(formResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.desc.SetValue("dark mode")
	form.env.SetValue("-100")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("negative envelope submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with a negative envelope")
	}
	if form.errText == "" {
		t.Fatal("expected an envelope error message")
	}
}

// TestFormEnvelopeNonNumericBlocksSubmit: non-numeric input is rejected
// the same way, so a typo can't silently clear the envelope.
func TestFormEnvelopeNonNumericBlocksSubmit(t *testing.T) {
	submitted := false
	form := newFeatureForm(nil, nil, false, 5000, func(formResult) tea.Cmd {
		submitted = true
		return nil
	})
	form.desc.SetValue("dark mode")
	form.env.SetValue("abc")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("non-numeric envelope submitted")
	}
	if submitted {
		t.Fatal("onSubmit ran with a non-numeric envelope")
	}
	if form.errText == "" {
		t.Fatal("expected an envelope error message")
	}
}

// TestFormEnvelopeEmpty: clearing the input yields Envelope nil, the
// "use the global default" signal distinct from an explicit 0.
func TestFormEnvelopeEmpty(t *testing.T) {
	var got *int
	form := newFeatureForm(nil, nil, false, 5000, func(res formResult) tea.Cmd {
		got = res.Envelope
		return nil
	})
	form.desc.SetValue("dark mode")
	form.env.SetValue("")
	if done, _ := form.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("form did not submit")
	}
	if got != nil {
		t.Fatalf("Envelope = %v, want nil (use default)", got)
	}
}

// TestFormEnvelopeTabOrder: tab walks every field in order — description,
// envelope, profile, route — and wraps back to description, skipping the
// repo stop since no repos are configured (nothing to choose there).
func TestFormEnvelopeTabOrder(t *testing.T) {
	form := newFeatureForm(nil, nil, false, 0, func(formResult) tea.Cmd { return nil })
	for _, want := range []int{featureFieldDesc, featureFieldEnvelope, featureFieldProfile, featureFieldRoute, featureFieldDesc} {
		if form.focus != want {
			t.Fatalf("focus = %d, want %d", form.focus, want)
		}
		form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	}
}

// TestCreateFeatureEnvelope: the parsed envelope stamps the persisted
// card's Budget; nil falls back to the global default and an explicit 0
// stays uncapped.
func TestCreateFeatureEnvelope(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.SetEnvelope(1000)
	m.Attach(store, wt, ws)

	zero := 0
	if msg := m.createFeature(formResult{Desc: "Explicit uncapped", Envelope: &zero})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 0 {
		t.Errorf("envelope = %d, want 0 (uncapped)", f.Budget.Envelope)
	}

	if msg := m.createFeature(formResult{Desc: "Default envelope"})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	f, err = store.GetFeature(ctx, "FD-002")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 1000 {
		t.Errorf("envelope = %d, want the global default 1000", f.Budget.Envelope)
	}
}

// TestCreateBugEnvelope: the bug form result's envelope flows the same
// way onto a new bug's budget.
func TestCreateBugEnvelope(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.SetEnvelope(1000)
	m.Attach(store, wt, ws)

	threeK := 3000
	if msg := m.createBug(bugFormResult{Title: "Crash on empty diff", Severity: domain.SeverityLow, Envelope: &threeK})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create bug failed: %s", nm.text)
		}
	}
	f, err := store.GetFeature(ctx, "BG-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 3000 {
		t.Errorf("envelope = %d, want 3000", f.Budget.Envelope)
	}
}
