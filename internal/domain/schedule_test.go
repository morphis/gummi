package domain

import "testing"

func TestScheduleValidate(t *testing.T) {
	base := Schedule{
		ID: "nightly", Name: "nightly triage", Kind: ScheduleMint,
		Cron: "0 5 * * *", Prompt: "triage new issues", Envelope: 50,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid mint refused: %v", err)
	}
	heartbeat := base
	heartbeat.Kind = ScheduleHeartbeat
	heartbeat.Target = "FF-012"
	heartbeat.Envelope = 0
	if err := heartbeat.Validate(); err != nil {
		t.Fatalf("valid heartbeat refused: %v", err)
	}

	refusals := []struct {
		name string
		edit func(*Schedule)
	}{
		{"no id", func(s *Schedule) { s.ID = "" }},
		{"bad id", func(s *Schedule) { s.ID = "Nightly!" }},
		{"no name", func(s *Schedule) { s.Name = "  " }},
		{"no cron", func(s *Schedule) { s.Cron = "" }},
		{"no prompt", func(s *Schedule) { s.Prompt = "  " }},
		{"unknown kind", func(s *Schedule) { s.Kind = "weekly" }},
		{"unknown last status", func(s *Schedule) { s.LastStatus = "succeeded" }},
		{"repo is a path", func(s *Schedule) { s.Repo = "a/b" }},
	}
	for _, tc := range refusals {
		s := base
		tc.edit(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate = ok, want refused", tc.name)
		}
	}
	// The per-kind rules, applied to an otherwise valid row of each kind.
	hbRefusals := []struct {
		name string
		edit func(*Schedule)
	}{
		{"heartbeat without a target", func(s *Schedule) { s.Target = "" }},
		{"heartbeat target is not freeform", func(s *Schedule) { s.Target = "FD-012" }},
		{"heartbeat with an envelope", func(s *Schedule) { s.Envelope = 50 }},
		{"heartbeat with a repo", func(s *Schedule) { s.Repo = "gummi" }},
		{"heartbeat with a backend", func(s *Schedule) { s.Backend = "claude" }},
		{"heartbeat with a model", func(s *Schedule) { s.Model = "m" }},
	}
	for _, tc := range hbRefusals {
		s := heartbeat
		tc.edit(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate = ok, want refused", tc.name)
		}
	}
	mintRefusals := []struct {
		name string
		edit func(*Schedule)
	}{
		{"mint without an envelope", func(s *Schedule) { s.Envelope = 0 }},
		{"mint with a negative envelope", func(s *Schedule) { s.Envelope = -5 }},
		{"mint names a target", func(s *Schedule) { s.Target = "FF-012" }},
	}
	for _, tc := range mintRefusals {
		s := base
		tc.edit(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate = ok, want refused", tc.name)
		}
	}
}

func TestScheduleKindHelpers(t *testing.T) {
	if !ScheduleMint.Valid() || !ScheduleHeartbeat.Valid() {
		t.Error("the two kinds must both be valid")
	}
	if ScheduleKind("weekly").Valid() {
		t.Error("an invented kind is valid")
	}
	if ScheduleOK.Disables() || ScheduleSkippedBusy.Disables() || ScheduleFailed.Disables() {
		t.Error("only the two pause statuses disable")
	}
	if !SchedulePausedExhausted.Disables() || !ScheduleDisabledTargetClosed.Disables() {
		t.Error("the two pause statuses must disable")
	}
}

func TestNewScheduleID(t *testing.T) {
	id, err := NewScheduleID("Nightly Triage")
	if err != nil || id != "nightly-triage" {
		t.Errorf("NewScheduleID = %q, %v; want nightly-triage", id, err)
	}
	if _, err := NewScheduleID("!!!"); err == nil {
		t.Error("a name with nothing usable in it must be refused, not hashed around")
	}
}
