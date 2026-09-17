package driver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestNextCarriesTheRunsOwnMode locks the suggested resume against the
// mode the run was actually driven in. `--gate-approval` rides on the
// card, so a resume inherits it; `--autonomous` is per-invocation and did
// not, so the suggested command was strictly less autonomous than the run
// it continues — it crosses its own gates but parks (exit 2) on the first
// question this run would have answered. On the lxd autopilot drive a
// card driven `run --autonomous --gate-approval autopilot` ran dry and
// suggested `gummi resume BG-001 --envelope 120`.
func TestNextCarriesTheRunsOwnMode(t *testing.T) {
	gateNext := func(t *testing.T, autonomous bool) string {
		t.Helper()
		h := newHarness(t, true, map[domain.Stage]stageFn{
			domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
				return msgIdle(o.Model, "Spec drafted.")
			},
			stageCritique: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
				return toolVerdict(o.Model, "pass")
			},
		})
		if _, err := h.driver(Options{GateApproval: GateAttended, Autonomous: autonomous}).
			Run(context.Background(), "feature"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		g := lastEvent(h, "gate")
		if g == nil {
			t.Fatalf("no gate event; kinds=%v", h.eventKinds())
		}
		next, _ := g["next"].(string)
		return next
	}

	attended := gateNext(t, false)
	if !strings.Contains(attended, "--approve") {
		t.Fatalf("attended next = %q, want the --approve verb", attended)
	}
	if strings.Contains(attended, "--autonomous") {
		t.Errorf("attended run suggests %q — it would change the card's mode", attended)
	}

	auto := gateNext(t, true)
	if !strings.Contains(auto, "--approve") {
		t.Fatalf("autonomous next = %q, want the --approve verb", auto)
	}
	if !strings.Contains(auto, "--autonomous") {
		t.Errorf("autonomous run suggests %q — the resume it names would park on a "+
			"question this run would have answered itself", auto)
	}
}

// TestAFailedRunClosesItsAutopilotPeriod: every other terminal parks the
// card with its reason — blocked, escalation, exhausted, done — and a
// backend failure did not, so the event log kept a period nothing closed
// and the board read that as a driving process that had vanished. It drew
// "autopilot stopped without saying so" directly beneath the transcript
// line where the run had said "claude run failed: You've hit your session
// limit". Seen on the verification run for this drive's own fixes.
func TestAFailedRunClosesItsAutopilotPeriod(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{{Kind: agent.EventError, Err: errors.New("backend is out of quota")}}
		},
	})
	out, _ := h.driver(Options{Autonomous: true}).Run(context.Background(), "add export")
	if out.Status != StatusError {
		t.Fatalf("status = %q, want %q", out.Status, StatusError)
	}
	marks, err := h.store.LatestCardMarks(context.Background(), h.only())
	if err != nil {
		t.Fatal(err)
	}
	if marks.Park.Seq == 0 {
		t.Fatal("a failed run left the card's autopilot period open — the board " +
			"reads that as a driver that vanished without saying anything")
	}
	if _, detail := marks.ParkReason(); !strings.Contains(detail, "quota") {
		t.Errorf("park detail = %q, want the failure the run reported", detail)
	}
	// and the resume it suggests keeps the mode the run was driven in
	if e := lastEvent(h, "error"); e != nil {
		if next, _ := e["next"].(string); next != "" && !strings.Contains(next, "--autonomous") {
			t.Errorf("error next = %q, drops the run's own mode", next)
		}
	}
}
