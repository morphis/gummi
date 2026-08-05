package agent

import (
	"context"
	"testing"
)

func drain(t *testing.T, s Session) []Event {
	t.Helper()
	var evs []Event
	for e := range s.Events() {
		evs = append(evs, e)
		if e.Kind == EventIdle {
			// consume until idle, then stop reading (Close closes chan)
			break
		}
	}
	return evs
}

func TestFakeEchoRoundTrip(t *testing.T) {
	ag := NewFake("hi there")
	defer ag.Close()
	if r := ag.CreditRate("m"); r != 0 {
		t.Errorf("default fake CreditRate = %v, want 0", r)
	}
	s, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: "/tmp", Role: RoleScribe})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	evs := drain(t, s)
	var msg string
	var sawUsage, sawIdle bool
	for _, e := range evs {
		switch e.Kind {
		case EventMessage:
			msg = e.Text
		case EventUsage:
			sawUsage = true
		case EventIdle:
			sawIdle = true
		}
	}
	if msg != "hi there" || !sawUsage || !sawIdle {
		t.Fatalf("unexpected stream: msg=%q usage=%v idle=%v", msg, sawUsage, sawIdle)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFakeResponderScript(t *testing.T) {
	ag := &Fake{Responder: func(opts SessionOpts, msg string) []Event {
		return []Event{
			{Kind: EventTextDelta, Text: "wor"},
			{Kind: EventTextDelta, Text: "king"},
			{Kind: EventToolCall, Tool: "shell"},
			{Kind: EventIdle},
		}
	}}
	defer ag.Close()
	s, _ := ag.NewSession(context.Background(), SessionOpts{WorkDir: "/tmp"})
	if err := s.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	evs := drain(t, s)
	if len(evs) != 4 || evs[2].Tool != "shell" {
		t.Fatalf("script mismatch: %+v", evs)
	}
}

func TestFakeClosedRejectsSessions(t *testing.T) {
	ag := NewFake("x")
	if err := ag.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: "/tmp"}); err == nil {
		t.Fatal("closed agent created a session")
	}
}

func TestFakeInterrupt(t *testing.T) {
	// A long stream of deltas; interrupting mid-stream must cut it
	// short and yield idle. The 1-buffer channel plus reading one
	// event at a time makes the interrupt point deterministic.
	ag := &Fake{Responder: func(opts SessionOpts, msg string) []Event {
		out := make([]Event, 100)
		for i := range out {
			out[i] = Event{Kind: EventTextDelta, Text: "x"}
		}
		return append(out, Event{Kind: EventIdle})
	}}
	defer ag.Close()
	s, _ := ag.NewSession(context.Background(), SessionOpts{WorkDir: "/tmp"})
	if err := s.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	var count int
	sawIdle := false
	for e := range s.Events() {
		count++
		if e.Kind == EventIdle {
			sawIdle = true
			break
		}
		if count == 2 {
			// interrupt after two deltas; the stream should wrap up
			if err := s.Interrupt(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !sawIdle {
		t.Fatal("interrupt did not end at idle")
	}
	if count > 40 {
		t.Errorf("interrupt did not short-circuit: %d events before idle", count)
	}
}
