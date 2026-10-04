package ui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/domain"
)

// TestSchedulesView golden: the fixture is one enabled heartbeat (mid
// cadence, an outcome to show) and one disabled mint (armed at enable,
// never fired), so the list shows both kinds, both on/off states, a next
// run and a last outcome.
func TestSchedulesView(t *testing.T) {
	m := populatedShell(120, 34)
	on := domain.Schedule{
		ID: "hourly", Name: "check CI", Kind: domain.ScheduleHeartbeat,
		Target: "FF-007", Cron: "0 * * * *", Prompt: "check CI, keep going",
		Enabled: true, NextRun: fixedTime.Add(45 * time.Minute),
		LastRun: fixedTime.Add(-30 * time.Minute), LastStatus: domain.ScheduleOK,
		LastCard: "FF-007",
	}
	off := domain.Schedule{
		ID: "nightly", Name: "nightly triage", Kind: domain.ScheduleMint,
		Repo: "gummi", Cron: "0 5 * * *", Prompt: "triage new issues", Envelope: 50,
	}
	d := &schedulesDialog{m: m, rows: []domain.Schedule{on, off}}
	m.Overlay.Push(d)
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestSchedulesViewEmpty: a board with no schedules says where one is
// defined instead of drawing an empty frame.
func TestSchedulesViewEmpty(t *testing.T) {
	m := populatedShell(120, 34)
	m.Overlay.Push(&schedulesDialog{m: m})
	golden.RequireEqual(t, []byte(m.View().Content))
}

// TestSchedulesViewKeys: j/k move the cursor within the rows, esc pops
// the view.
func TestSchedulesViewKeys(t *testing.T) {
	m := populatedShell(120, 34)
	d := &schedulesDialog{m: m, rows: []domain.Schedule{
		{ID: "a", Name: "a", Kind: domain.ScheduleMint, Cron: "* * * * *", Prompt: "p", Envelope: 1},
		{ID: "b", Name: "b", Kind: domain.ScheduleMint, Cron: "* * * * *", Prompt: "p", Envelope: 1},
	}}
	m.Overlay.Push(d)
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'j'})
	if d.cursor != 1 {
		t.Errorf("cursor = %d after j, want 1", d.cursor)
	}
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'k'})
	if d.cursor != 0 {
		t.Errorf("cursor = %d after k, want 0", d.cursor)
	}
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'k'})
	if d.cursor != 0 {
		t.Errorf("cursor moved above the first row: %d", d.cursor)
	}
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 27}) // esc
	if m.Overlay.Contains("schedules") {
		t.Fatal("esc did not close the schedules view")
	}
}

// TestScheduleNextWords: the relative phrase a row shows for its next
// fire.
func TestScheduleNextWords(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		next time.Time
		want string
	}{
		{now.Add(-time.Minute), "due now"},
		{now.Add(30 * time.Second), "in under a minute"},
		{now.Add(45 * time.Minute), "in 45m"},
		{now.Add(3 * time.Hour), "in 3h"},
		{now.Add(48 * time.Hour), "in 2d"},
	} {
		if got := scheduleNextWords(tc.next, time.Time{}, now); got != tc.want {
			t.Errorf("scheduleNextWords(%s) = %q, want %q", tc.next, got, tc.want)
		}
	}
}

// TestScheduleLastWordsTruncatesByRune: a long detail is cut at a rune
// boundary, never mid-rune — a detail can name a card or a repo path
// with non-ASCII text, and a byte slice would leave invalid UTF-8 in
// the row tail.
func TestScheduleLastWordsTruncatesByRune(t *testing.T) {
	long := strings.Repeat("é", 80)
	got := scheduleLastWords(domain.Schedule{
		ID: "x", LastStatus: domain.ScheduleFailed, LastDetail: long,
	})
	if !utf8.ValidString(got) {
		t.Fatal("the truncated tail is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "…") || strings.Contains(got, "ééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé") {
		t.Fatalf("the tail was not truncated: %d runes", utf8.RuneCountInString(got))
	}
}

// TestTheScheduleViewOpensTheDialog: n opens the create dialog from the
// schedules list, and enter opens the same dialog prefilled on the
// selected row.
func TestTheScheduleViewOpensTheDialog(t *testing.T) {
	m := scheduleFormBoard(t)
	seed := &domain.Schedule{
		ID: "hourly", Name: "hourly", Kind: domain.ScheduleHeartbeat,
		Target: "FF-003", Cron: "0 * * * *", Prompt: "keep going",
	}
	if err := m.store.CreateSchedule(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.openSchedules())

	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	d, ok := m.Overlay.Top().(*scheduleForm)
	if !ok {
		t.Fatalf("n did not open the schedule dialog: %T", m.Overlay.Top())
	}
	if d.edit != nil {
		t.Error("n opened the dialog in edit mode")
	}
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	d2 := &schedulesDialog{m: m, rows: func() []domain.Schedule {
		rows, err := m.store.ListSchedules(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}()}
	m.Overlay.Push(d2)
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	d3, ok := m.Overlay.Top().(*scheduleForm)
	if !ok {
		t.Fatalf("enter did not open the schedule dialog: %T", m.Overlay.Top())
	}
	if d3.edit == nil || d3.edit.ID != "hourly" {
		t.Fatalf("enter opened %v, want the selected row's edit", d3.edit)
	}
	if d3.name.Value() != "hourly" || d3.target.Value() != "FF-003" {
		t.Errorf("the edit dialog prefilled name=%q target=%q", d3.name.Value(), d3.target.Value())
	}
}
