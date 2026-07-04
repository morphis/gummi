package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Headless is an Agent backed by an external agent process that speaks a
// line-delimited JSON protocol on stdio (DESIGN §4.1: the interface is
// pluggable beyond the Copilot SDK). Each session spawns the configured
// command in the feature's worktree; gummi writes turns to the process's
// stdin and reads its events from stdout. This is the generic escape
// hatch for any agent that can emit a handful of JSON event lines.
//
// Wire protocol — gummi → agent, one JSON object per line on stdin:
//
//	{"type":"init","workdir":"…","model":"…","hints":[…],
//	 "provider":{"type":"openai","base_url":"…","api_key_env":"NAME"}}
//	{"type":"send","text":"…"}
//	{"type":"interrupt"}
//
// agent → gummi, one JSON object per line on stdout:
//
//	{"type":"text","text":"…"}      → EventTextDelta
//	{"type":"message","text":"…"}   → EventMessage
//	{"type":"tool","name":"…"}      → EventToolCall
//	{"type":"usage","credits":N,"input":I,"output":O,"model":"…"} → EventUsage
//	{"type":"idle"}                 → EventIdle
//	{"type":"error","message":"…"}  → EventError
//
// The provider API key is never written into the protocol: init carries
// only the env-var NAME, and the child inherits gummi's environment, so it
// resolves the key itself (threat list: keys are references, not literals).
type Headless struct {
	argv []string

	mu       sync.Mutex
	sessions []*headlessSession
	closed   bool
}

// NewHeadless returns an Agent that runs argv (command + fixed args) once
// per session. argv must be non-empty.
func NewHeadless(argv []string) (*Headless, error) {
	if len(argv) == 0 {
		return nil, errors.New("headless agent: empty command")
	}
	return &Headless{argv: append([]string(nil), argv...)}, nil
}

// Name implements Agent: the configured command's basename, so the UI
// names the binary doing the work.
func (h *Headless) Name() string { return filepath.Base(h.argv[0]) }

// Capabilities implements Agent. The protocol carries a provider (BYOK),
// an interrupt frame, and a usage frame, so those are supported; there is
// no session persistence, so resume is not. A given child may choose not
// to emit usage frames — the orchestrator meters whatever arrives and
// enforces caps itself as a backstop regardless.
func (h *Headless) Capabilities() Capabilities {
	return Capabilities{BYOK: true, Interrupt: true, UsageEvents: true}
}

// initMsg / turnMsg are the gummi → agent frames.
type headlessInit struct {
	Type     string        `json:"type"`
	WorkDir  string        `json:"workdir"`
	Model    string        `json:"model,omitempty"`
	Hints    []string      `json:"hints,omitempty"`
	Provider *headlessProv `json:"provider,omitempty"`
}

type headlessProv struct {
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

// NewSession implements Agent: spawn the command in opts.WorkDir and send
// the init frame.
func (h *Headless) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("headless agent is closed")
	}
	if opts.Provider.BaseURL != "" && opts.Model == "" {
		return nil, errors.New("model required when a provider is set")
	}

	// spawn + init OUTSIDE the lock (fork/exec and the init write must not
	// serialize session creation or block a concurrent Close).
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, h.argv[0], h.argv[1:]...) //nolint:gosec // argv is operator config (GUMMI_AGENT_CMD), not agent/repo input
	cmd.Dir = opts.WorkDir
	// The child inherits gummi's full environment so it can resolve the
	// BYOK key by name (init carries only the env-var NAME). A generic
	// operator-chosen agent binary may need arbitrary env, so — unlike the
	// Copilot CLI, which takes a scrubbed allowlist — this passes the whole
	// environment; the command is trusted operator config, not repo input.
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("headless stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("headless stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting headless agent %q: %w", h.argv[0], err)
	}

	s := &headlessSession{
		cmd:      cmd,
		cancel:   cancel,
		stdin:    stdin,
		raw:      make(chan Event, 16),
		events:   make(chan Event),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	initFrame := headlessInit{Type: "init", WorkDir: opts.WorkDir, Model: opts.Model, Hints: opts.SystemHints}
	if opts.Provider.BaseURL != "" {
		p := opts.Provider
		initFrame.Provider = &headlessProv{Type: cmpOr(p.Type, "openai"), BaseURL: p.BaseURL, APIKeyEnv: p.APIKeyEnv}
	}
	if err := s.write(initFrame); err != nil {
		// no goroutines started yet: tear down directly (Close would block
		// on readDone, which read() will never close).
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("headless init: %w", err)
	}
	go s.forward()
	go s.read(stdout)

	// register under the lock, re-checking closed so a session started
	// concurrently with Close is torn down rather than leaked.
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("headless agent is closed")
	}
	h.sessions = append(h.sessions, s)
	h.mu.Unlock()
	return s, nil
}

// Close implements Agent: end every live session.
func (h *Headless) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, s := range h.sessions {
		_ = s.Close()
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type headlessSession struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser

	raw      chan Event
	events   chan Event
	stop     chan struct{}
	readDone chan struct{} // closed when read() has finished draining stdout

	wmu       sync.Mutex // serializes writes to stdin
	closeOnce sync.Once
}

func (s *headlessSession) Events() <-chan Event { return s.events }

// forward owns events: it copies raw→events and closes events exactly
// once (the read goroutine and Send never touch events directly).
func (s *headlessSession) forward() {
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

// read scans the child's stdout, maps each JSON line to an Event, and
// feeds the forwarder. A malformed line becomes an EventError rather than
// silently vanishing. When stdout closes (the child exited), it stops.
func (s *headlessSession) read(stdout io.Reader) {
	defer close(s.readDone)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow large messages
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		ev, ok := decodeHeadless(line)
		if !ok {
			continue
		}
		select {
		case s.raw <- ev:
		case <-s.stop:
			return
		}
	}
	// stdout closed: the turn/session ended. Surface a scan error, else
	// treat a clean EOF as idle so the orchestrator isn't left hanging.
	final := Event{Kind: EventIdle}
	if err := sc.Err(); err != nil {
		final = Event{Kind: EventError, Err: fmt.Errorf("headless agent stream: %w", err)}
	}
	select {
	case s.raw <- final:
	case <-s.stop:
	}
}

// headlessEvent is the agent → gummi frame.
type headlessEvent struct {
	Type    string  `json:"type"`
	Text    string  `json:"text"`
	Name    string  `json:"name"`
	Message string  `json:"message"`
	Credits float64 `json:"credits"`
	Input   int64   `json:"input"`
	Output  int64   `json:"output"`
	Model   string  `json:"model"`
}

func decodeHeadless(line []byte) (Event, bool) {
	var m headlessEvent
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{Kind: EventError, Err: fmt.Errorf("headless agent sent malformed JSON: %w", err)}, true
	}
	switch m.Type {
	case "text":
		return Event{Kind: EventTextDelta, Text: m.Text}, true
	case "message":
		return Event{Kind: EventMessage, Text: m.Text}, true
	case "tool":
		return Event{Kind: EventToolCall, Tool: m.Name}, true
	case "usage":
		return Event{Kind: EventUsage, Usage: Usage{Credits: m.Credits, InputTokens: m.Input, OutputTokens: m.Output, Model: m.Model}}, true
	case "idle":
		return Event{Kind: EventIdle}, true
	case "error":
		return Event{Kind: EventError, Err: errors.New(cmpOr(m.Message, "headless agent reported an error"))}, true
	default:
		return Event{}, false // unknown type: ignore rather than guess
	}
}

func (s *headlessSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.stdin.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *headlessSession) Send(_ context.Context, msg string) error {
	return s.write(map[string]string{"type": "send", "text": msg})
}

func (s *headlessSession) Interrupt(_ context.Context) error {
	return s.write(map[string]string{"type": "interrupt"})
}

func (s *headlessSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()    // SIGKILL the child → stdout EOF → read()'s Scan ends
		close(s.stop) // stop forward; unblock read()'s raw sends
		_ = s.stdin.Close()
		// join the read goroutine before Wait: reading a StdoutPipe after
		// Wait closes it is a documented error, so wait for the pipe to be
		// drained first (bounded, since cancel() EOFs it).
		select {
		case <-s.readDone:
		case <-time.After(3 * time.Second):
		}
		_ = s.cmd.Wait() // reap
	})
	return nil
}
