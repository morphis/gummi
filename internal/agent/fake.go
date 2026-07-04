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

	mu       sync.Mutex
	sessions []*fakeSession
	closed   bool
}

// NewFake returns a Fake that echoes a fixed reply and advertises full
// capabilities.
func NewFake(reply string) *Fake {
	return &Fake{
		Reply: reply,
		Caps:  Capabilities{BYOK: true, Resume: true, UsageEvents: true, Interrupt: true},
	}
}

// Capabilities implements Agent.
func (f *Fake) Capabilities() Capabilities { return f.Caps }

// NewSession implements Agent.
func (f *Fake) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("fake agent is closed")
	}
	if opts.Provider.BaseURL != "" && opts.Model == "" {
		return nil, errors.New("model required when a provider is set")
	}
	s := &fakeSession{agent: f, opts: opts, events: make(chan Event, 32), stop: make(chan struct{})}
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

	events    chan Event
	stop      chan struct{}
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	sends     int
	lastMsg   string
	interrupt bool
}

func (s *fakeSession) Events() <-chan Event { return s.events }

func (s *fakeSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	s.sends++
	s.lastMsg = msg
	s.interrupt = false
	s.wg.Add(1)
	s.mu.Unlock()

	reply := s.agent.Reply
	if reply == "" {
		reply = fmt.Sprintf("ack: %s", strings.TrimSpace(msg))
	}
	stream := []Event{
		{Kind: EventMessage, Text: reply},
		{Kind: EventUsage, Usage: Usage{Credits: 1, OutputTokens: int64(len(reply)), Model: s.opts.Model}},
		{Kind: EventIdle},
	}
	if s.agent.Responder != nil {
		stream = s.agent.Responder(s.opts, msg)
	}

	// Stream asynchronously: a real session delivers events while the
	// caller consumes them, so Send must not block on the channel.
	go func() {
		defer s.wg.Done()
		for _, e := range stream {
			s.mu.Lock()
			interrupted := s.interrupt
			s.mu.Unlock()
			if interrupted {
				e = Event{Kind: EventIdle}
			}
			select {
			case s.events <- e:
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
	close(s.stop) // unblock any in-flight streaming goroutines
	s.mu.Unlock()

	s.wg.Wait()     // let them exit before closing the channel they send on
	close(s.events) // then close the stream
}

// SendCount reports how many turns the session has received (test aid).
func (s *fakeSession) SendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}
