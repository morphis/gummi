package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// scheduleTestNotifier records every needs-you the board raises.
type scheduleTestNotifier struct {
	mu  sync.Mutex
	got []string
}

func (n *scheduleTestNotifier) NeedsYou(id domain.FeatureID, text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, string(id)+" "+text)
}

func (n *scheduleTestNotifier) taken() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.got...)
}

func scheduleFire(id, name string, kind domain.ScheduleKind, st domain.ScheduleStatus, card, detail string) engine.ScheduleFire {
	return engine.ScheduleFire{
		ID: domain.ScheduleID(id), Name: name, Kind: kind,
		Outcome: domain.ScheduleOutcome{At: fixedTime, Status: st, Detail: detail, Card: domain.FeatureID(card)},
	}
}

// TestScheduleTickAlertsByStatus: every outcome except skipped-busy
// raises an alert on the board's attention notifiers (bell, web push) —
// a five-minute heartbeat against a working session must not ring every
// five minutes.
func TestScheduleTickAlertsByStatus(t *testing.T) {
	m := populatedShell(120, 34)
	n := &scheduleTestNotifier{}
	m.AddAttentionNotifier(n)

	m.Update(scheduleTickMsg{fires: []engine.ScheduleFire{
		scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleOK, "FF-001", ""),
	}})
	if got := n.taken(); len(got) != 1 {
		t.Fatalf("an ok fire raised %v, want one alert", got)
	}
	n.mu.Lock()
	n.got = nil
	n.mu.Unlock()

	m.Update(scheduleTickMsg{fires: []engine.ScheduleFire{
		scheduleFire("nightly", "nightly triage", domain.ScheduleMint, domain.ScheduleFailed, "FF-002", "the kickoff was refused"),
		scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleSkippedBusy, "FF-001", "FF-001 is mid-turn; the fire is skipped, not queued"),
	}})
	got := n.taken()
	if len(got) != 1 {
		t.Fatalf("a failed and a skipped fire raised %v, want one alert (failed only)", got)
	}
	if !strings.Contains(got[0], "nightly") {
		t.Errorf("the alert is %q, want the failed schedule's", got[0])
	}

	// A pause is an alert too: the schedule stopped spending on its own.
	n.mu.Lock()
	n.got = nil
	n.mu.Unlock()
	m.Update(scheduleTickMsg{fires: []engine.ScheduleFire{
		scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.SchedulePausedExhausted, "FF-001", "FF-001 has spent its envelope of 400 credits; raise it to carry on"),
	}})
	if got := n.taken(); len(got) != 1 {
		t.Fatalf("a paused fire raised %v, want one alert", got)
	}
}

// TestScheduleTickNoticeText: the notice a fire leaves names the
// schedule and says what happened, in the words the row's status uses.
func TestScheduleTickNoticeText(t *testing.T) {
	for _, tc := range []struct {
		fire engine.ScheduleFire
		want string
	}{
		{
			scheduleFire("nightly", "nightly triage", domain.ScheduleMint, domain.ScheduleOK, "FF-012", ""),
			"schedule nightly triage: minted FF-012",
		},
		{
			scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleOK, "FF-007", ""),
			"schedule hourly: sent its turn to FF-007",
		},
		{
			scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleSkippedBusy, "", "FF-007 is mid-turn; the fire is skipped, not queued"),
			"schedule hourly: skipped — FF-007 is mid-turn; the fire is skipped, not queued",
		},
		{
			scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.SchedulePausedExhausted, "", "FF-007 has spent its envelope"),
			"schedule hourly paused: FF-007 has spent its envelope",
		},
		{
			scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleDisabledTargetClosed, "", "FF-007 is closed — the heartbeat has nothing to send to"),
			"schedule hourly turned off: FF-007 is closed — the heartbeat has nothing to send to",
		},
		{
			scheduleFire("nightly", "nightly triage", domain.ScheduleMint, domain.ScheduleFailed, "", "repository \"x\" is not configured"),
			"schedule nightly triage failed: repository \"x\" is not configured",
		},
	} {
		if got := scheduleFireNotice(tc.fire); got != tc.want {
			t.Errorf("notice = %q, want %q", got, tc.want)
		}
	}
}

// TestScheduleTickPollRearms: the poll's message answers with the next
// poll, so the clock keeps ticking while the engine is out (no engine:
// the tick itself is a no-op that still rearms). A tick WITH fires also
// rearms — the notice must never stop the clock — and so does a tick
// whose engine call failed: a transient store error must not end the
// poll chain and silently stop every schedule while the board runs on.
func TestScheduleTickPollRearms(t *testing.T) {
	m := populatedShell(120, 34)
	_, cmd := m.Update(schedulePollMsg{})
	if cmd == nil {
		t.Fatal("the poll did not rearm")
	}
	_, cmd = m.Update(scheduleTickMsg{fires: []engine.ScheduleFire{
		scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleOK, "FF-001", ""),
	}})
	if cmd == nil {
		t.Fatal("a tick with fires did not rearm the poll; the clock would stop")
	}
	_, cmd = m.Update(scheduleTickMsg{err: fmt.Errorf("store unavailable")})
	if cmd == nil {
		t.Fatal("a failed tick did not rearm the poll; the clock would stop")
	}
}

// TestScheduleFiredMsgAlerts: a forced run-now reports like the poll's
// fire does — one alert for anything that is not a busy skip.
func TestScheduleFiredMsgAlerts(t *testing.T) {
	m := populatedShell(120, 34)
	n := &scheduleTestNotifier{}
	m.AddAttentionNotifier(n)

	m.Update(scheduleFiredMsg{fire: scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleOK, "FF-001", "")})
	if got := n.taken(); len(got) != 1 {
		t.Fatalf("a forced ok fire raised %v, want one alert", got)
	}

	n.mu.Lock()
	n.got = nil
	n.mu.Unlock()
	m.Update(scheduleFiredMsg{fire: scheduleFire("hourly", "hourly", domain.ScheduleHeartbeat, domain.ScheduleSkippedBusy, "FF-001", "mid-turn")})
	if got := n.taken(); len(got) != 0 {
		t.Fatalf("a forced skip raised %v, want none", got)
	}
}

// TestSchedulesDialogToggleKeyOpensConfirm: enabling asks (it is the
// switch that starts spending); disabling does not.
func TestSchedulesDialogToggleKeyOpensConfirm(t *testing.T) {
	m := populatedShell(120, 34)
	off := domain.Schedule{
		ID: "nightly", Name: "nightly triage", Kind: domain.ScheduleMint,
		Cron: "0 5 * * *", Prompt: "p", Envelope: 50, NextRun: time.Date(2026, 7, 4, 5, 0, 0, 0, time.UTC),
	}
	on := domain.Schedule{
		ID: "hourly", Name: "hourly", Kind: domain.ScheduleHeartbeat,
		Target: "FF-001", Cron: "0 * * * *", Prompt: "p", Enabled: true,
		NextRun: time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC),
	}
	d := &schedulesDialog{m: m, rows: []domain.Schedule{off, on}}
	m.Overlay.Push(d)

	// e on the disabled row asks to enable; answering no leaves it off.
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'e'})
	if !m.Overlay.Contains("confirm-schedule-enable") {
		t.Fatal("enabling a disabled schedule did not ask")
	}
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'n'})
	if m.Overlay.Contains("confirm-schedule-enable") {
		t.Fatal("the confirm did not close on n")
	}

	// e on the enabled row disables at once: it is safe and does not ask.
	d.cursor = 1
	m.Overlay.HandleKey(tea.KeyPressMsg{Code: 'e'})
	if m.Overlay.Contains("confirm-schedule-enable") || m.Overlay.Contains("confirm-schedule-disable") {
		t.Fatal("disabling an enabled schedule asked; it is safe and does not")
	}
}
