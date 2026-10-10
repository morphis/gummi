package domain

import (
	"strings"
	"testing"
)

func TestParseAudit(t *testing.T) {
	cases := []struct {
		reply string
		want  AuditVerdict
		note  string
	}{
		{"VERDICT: MET — all tests pass", AuditMet, "all tests pass"},
		{"Looks fine.\n**VERDICT: CONTINUE** - the parser is half done", AuditContinue, "the parser is half done"},
		{"## Verdict: stuck: waiting on credentials", AuditStuck, "waiting on credentials"},
		{"The agent asked for a key.\nVERDICT: STUCK", AuditStuck, "The agent asked for a key."},
		{"VERDICT: CONTINUE first\nVERDICT: MET last", AuditMet, "last"},
		{"I think it is done", AuditStuck, "the auditor gave no verdict: I think it is done"},
		{"", AuditStuck, "the auditor gave no verdict: "},
		{"VERDICT: METHODICAL", AuditStuck, "the auditor gave no verdict: VERDICT: METHODICAL"},
	}
	for _, c := range cases {
		v, note := ParseAudit(c.reply)
		if v != c.want || note != c.note {
			t.Errorf("ParseAudit(%q) = %s %q, want %s %q", c.reply, v, note, c.want, c.note)
		}
	}
}

func TestObjectiveNext(t *testing.T) {
	active := Objective{Text: "x", State: ObjectiveActive}
	failed := CheckResult{Ran: true, Tail: "FAIL TestX"}

	cases := []struct {
		name     string
		o        Objective
		v        AuditVerdict
		check    CheckResult
		state    ObjectiveState
		send     bool
		turns    int
		streak   int
		noteHint string
	}{
		{"continue sends a turn", active, AuditContinue, CheckResult{}, ObjectiveActive, true, 1, 0, "n"},
		{"met without a check settles", active, AuditMet, CheckResult{}, ObjectiveMet, false, 0, 0, "n"},
		{"met with a passing check settles", active, AuditMet, CheckResult{Ran: true, Passed: true}, ObjectiveMet, false, 0, 0, "n"},
		{"met with a failing check continues", active, AuditMet, failed, ObjectiveActive, true, 1, 0, "FAIL TestX"},
		{"one stuck continues", active, AuditStuck, CheckResult{}, ObjectiveActive, true, 1, 1, "n"},
		{"third stuck settles", Objective{Text: "x", State: ObjectiveActive, StuckStreak: 2}, AuditStuck, CheckResult{}, ObjectiveStuck, false, 0, 3, "n"},
		{"continue resets the streak", Objective{Text: "x", State: ObjectiveActive, StuckStreak: 2}, AuditContinue, CheckResult{}, ObjectiveActive, true, 1, 0, "n"},
		{"the cap settles", Objective{Text: "x", State: ObjectiveActive, Turns: ObjectiveTurnCap}, AuditContinue, CheckResult{}, ObjectiveCapped, false, ObjectiveTurnCap, 0, "n"},
		{"met beats the cap", Objective{Text: "x", State: ObjectiveActive, Turns: ObjectiveTurnCap}, AuditMet, CheckResult{}, ObjectiveMet, false, ObjectiveTurnCap, 0, "n"},
	}
	for _, c := range cases {
		got, send := c.o.Next(c.v, "n", c.check)
		if got.State != c.state || send != c.send || got.Turns != c.turns || got.StuckStreak != c.streak || !strings.Contains(got.Note, c.noteHint) {
			t.Errorf("%s: got %s send=%v turns=%d streak=%d note=%q", c.name, got.State, send, got.Turns, got.StuckStreak, got.Note)
		}
	}
}

func TestObjectiveStateSettled(t *testing.T) {
	for _, s := range []ObjectiveState{ObjectiveActive, ObjectivePaused} {
		if s.Settled() {
			t.Errorf("%s reads as settled", s)
		}
	}
	for _, s := range []ObjectiveState{ObjectiveMet, ObjectiveStuck, ObjectiveExhausted, ObjectiveCapped, ObjectiveFailed} {
		if !s.Settled() {
			t.Errorf("%s does not read as settled", s)
		}
	}
	if (Objective{Text: " ", State: ObjectiveActive}).Validate() == nil {
		t.Error("an empty objective validated")
	}
}
