package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Fake is an in-process Agent for tests: no subprocess, no network.
// It scripts each session's response from a Responder so orchestrator
// and UI logic can be exercised deterministically.
type Fake struct {
	// Responder produces the event stream for a turn. If nil, Reply is
	// used to build a single message + idle.
	Responder func(opts SessionOpts, msg string) []Event
	// Reply is the assistant text echoed when Responder is nil.
	Reply string
	// Caps is what Capabilities reports.
	Caps Capabilities
	// OnInterrupt, if set, is called each time a session is interrupted
	// (lets a test observe orchestrator-side budget enforcement).
	OnInterrupt func()
	// Rate is the fake's reported CreditRate (0 = engine default). Tests
	// exercising the token-priced fallback set it to price the fake's
	// synthetic token usage into credits.
	Rate float64

	mu       sync.Mutex
	sessions []*fakeSession
	closed   bool
}

// NewFake returns a Fake that echoes a fixed reply and advertises full
// capabilities.
func NewFake(reply string) *Fake {
	return &Fake{
		Reply: reply,
		Caps:  Capabilities{Resume: true, UsageEvents: true, Interrupt: true},
	}
}

// Name implements Agent.
func (f *Fake) Name() string { return "fake" }

// Capabilities implements Agent.
func (f *Fake) Capabilities() Capabilities { return f.Caps }

// CreditRate implements Agent. Tests inject the rate they need via the
// engine's Config.StageBudget / TurnReserve knobs; fake sessions carry no
// realized rate of their own.
func (f *Fake) CreditRate(string) float64 { return f.Rate }

// NewSession implements Agent.
func (f *Fake) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("fake agent is closed")
	}
	s := &fakeSession{
		agent: f,
		opts:  opts,
		// small buffer so Interrupt takes effect within a few events
		// rather than after a large pre-buffered burst
		raw:    make(chan Event, 4),
		events: make(chan Event),
		stop:   make(chan struct{}),
	}
	go s.forward()
	f.sessions = append(f.sessions, s)
	return s, nil
}

// Close implements Agent.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for _, s := range f.sessions {
		s.closeOnce()
	}
	return nil
}

type fakeSession struct {
	agent *Fake
	opts  SessionOpts

	// raw carries events from streaming goroutines to the forwarder;
	// events is the consumer-facing stream, owned solely by the
	// forwarder so it closes exactly once with no send-on-closed race
	// and no wait for goroutines that a test may have blocked.
	raw    chan Event
	events chan Event
	stop   chan struct{}

	mu        sync.Mutex
	closed    bool
	sends     int
	lastMsg   string
	interrupt bool
	resolved  map[string]string // client-tool callID → result (Resolve)
}

// Resolve implements ToolResolver: records the result so a test can
// assert what the orchestrator answered a client-tool call with.
func (s *fakeSession) Resolve(_ context.Context, callID, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session closed")
	}
	if s.resolved == nil {
		s.resolved = map[string]string{}
	}
	s.resolved[callID] = result
	return nil
}

// Resolved returns the result a client-tool call was answered with
// (test aid).
func (s *fakeSession) Resolved(callID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resolved[callID]
	return r, ok
}

func (s *fakeSession) Events() <-chan Event { return s.events }

func (s *fakeSession) forward() {
	defer close(s.events)
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.raw:
			select {
			case s.events <- e:
			case <-s.stop:
				return
			}
		}
	}
}

func (s *fakeSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	s.sends++
	s.lastMsg = msg
	s.interrupt = false
	s.mu.Unlock()

	// Stream asynchronously: a real session delivers events while the
	// caller consumes them, so Send must not block — and the Responder
	// (which a test may block to hold a slot) runs inside the goroutine,
	// not in Send, so it never stalls the caller.
	go func() {
		var stream []Event
		if s.agent.Responder != nil {
			stream = s.agent.Responder(s.opts, msg)
		} else {
			reply := s.agent.Reply
			if reply == "" {
				reply = fmt.Sprintf("ack: %s", strings.TrimSpace(msg))
			}
			stream = []Event{
				{Kind: EventMessage, Text: reply},
				{Kind: EventUsage, Usage: Usage{Credits: 1, OutputTokens: int64(len(reply)), Model: s.opts.Model}},
				{Kind: EventIdle},
			}
		}
		for _, e := range stream {
			s.mu.Lock()
			interrupted := s.interrupt
			s.mu.Unlock()
			if interrupted {
				e = Event{Kind: EventIdle}
			}
			select {
			case s.raw <- e:
			case <-s.stop:
				return
			}
			if interrupted {
				return
			}
		}
	}()
	return nil
}

func (s *fakeSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session closed")
	}
	s.interrupt = true
	if s.agent.OnInterrupt != nil {
		s.agent.OnInterrupt()
	}
	return nil
}

func (s *fakeSession) Close() error {
	s.closeOnce()
	return nil
}

func (s *fakeSession) closeOnce() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop) // forwarder closes events; blocked goroutines exit via stop
}

// SendCount reports how many turns the session has received (test aid).
func (s *fakeSession) SendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}
