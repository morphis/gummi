// Package term runs a person's own shell in a card's worktree, for the web
// face's Terminal tab (`gummi web --terminal`).
//
// A Session is one shell on a pty. It outlives the page that opened it: a
// phone that sleeps comes back to the same shell, and is replayed the tail
// of what it missed from a bounded scrollback. A Registry holds at most one
// session per key (a card's id), caps how many run at once, and ends the
// ones nobody has been attached to for a while or whose directory is gone.
//
// This is not an agent's terminal and nothing here is sandboxed: the shell
// runs as whoever runs gummi, with gummi's environment. Who may open one is
// the caller's to decide (internal/web's routes_term.go).
package term

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/morphis/gummi/internal/childproc"
)

const (
	// scrollback is how much of a shell's output is kept to replay to a
	// page that attaches.
	scrollback = 256 << 10
	// subBuffer is how many chunks an attached page may fall behind by
	// before it is dropped; it reattaches and is replayed the scrollback.
	subBuffer = 256
	// hangupGrace is how long a shell told to hang up gets before its
	// process group is killed.
	hangupGrace = 500 * time.Millisecond
	// sweepEvery is how often a Registry looks for sessions to end.
	sweepEvery = 30 * time.Second

	// DefaultMax and DefaultIdle are what `gummi web --terminal` runs with.
	DefaultMax  = 4
	DefaultIdle = time.Hour
)

// ErrTooMany is Open's refusal when the registry already runs Max shells.
var ErrTooMany = errors.New("too many terminals are open on this board; exit one first")

// Session is one shell on a pty.
type Session struct {
	// Dir is the directory the shell started in.
	Dir string

	ptmx   *os.File
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	ring     []byte
	subs     map[*Sub]struct{}
	detached time.Time // when the last page left; zero while one is attached
	code     int
}

// Sub is one attached page's view of a session's output.
type Sub struct {
	// C carries the output written after the attach. It is closed when the
	// shell ends, or when the page fell too far behind to keep.
	C chan []byte
	s *Session
}

// start runs shell in dir on a new pty of the given size.
func start(shell, dir string, cols, rows int, now time.Time) (*Session, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("opening a pty: %w", err)
	}
	defer func() { _ = tty.Close() }()
	_ = pty.Setsize(ptmx, winsize(cols, rows))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, shell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	childproc.Session(cmd)
	if err := childproc.Start(cmd); err != nil {
		cancel()
		_ = ptmx.Close()
		return nil, fmt.Errorf("starting %s: %w", shell, err)
	}
	s := &Session{
		Dir:      dir,
		ptmx:     ptmx,
		cmd:      cmd,
		cancel:   cancel,
		done:     make(chan struct{}),
		subs:     map[*Sub]struct{}{},
		detached: now,
	}
	return s, nil
}

// run pumps the shell's output until it ends, then calls onExit. The shell
// ending is what ends the session, whatever it left running on the pty:
// the wait closes the master, which is what stops the read.
func (s *Session) run(onExit func()) {
	waited := make(chan int, 1)
	go func() {
		err := s.cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		_ = s.ptmx.Close()
		waited <- code
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.fanOut(bytes.Clone(buf[:n]))
		}
		if err != nil {
			break
		}
	}
	s.cancel()
	code := <-waited
	s.mu.Lock()
	s.code = code
	// done closes first: a page whose subscription closes can then tell
	// the shell ending from having been dropped for falling behind
	close(s.done)
	for sub := range s.subs {
		close(sub.C)
	}
	s.subs = nil
	s.mu.Unlock()
	onExit()
}

func (s *Session) fanOut(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = append(s.ring, chunk...)
	if len(s.ring) > 2*scrollback {
		s.ring = append([]byte(nil), s.ring[len(s.ring)-scrollback:]...)
	}
	for sub := range s.subs {
		select {
		case sub.C <- chunk:
		default:
			delete(s.subs, sub)
			close(sub.C)
		}
	}
}

// Attach subscribes to the shell's output. It answers what the shell has
// written so far (the tail of it, bounded) and the subscription everything
// after that arrives on, with nothing lost or repeated between the two.
func (s *Session) Attach() ([]byte, *Sub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tail := s.ring
	if len(tail) > scrollback {
		tail = tail[len(tail)-scrollback:]
	}
	sub := &Sub{C: make(chan []byte, subBuffer), s: s}
	if s.subs == nil { // the shell has ended
		close(sub.C)
	} else {
		s.subs[sub] = struct{}{}
		s.detached = time.Time{}
	}
	return bytes.Clone(tail), sub
}

// Detach ends a subscription. now is when the page left.
func (sub *Sub) Detach(now time.Time) {
	s := sub.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; ok {
		delete(s.subs, sub)
		close(sub.C)
	}
	if s.subs != nil && len(s.subs) == 0 && s.detached.IsZero() {
		s.detached = now
	}
}

// Write types p into the shell.
func (s *Session) Write(p []byte) error {
	_, err := s.ptmx.Write(p)
	return err
}

// Resize tells the shell its window is cols by rows.
func (s *Session) Resize(cols, rows int) {
	_ = pty.Setsize(s.ptmx, winsize(cols, rows))
}

// Done is closed once the shell has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// ExitCode is the shell's exit status, meaningful once Done is closed.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Close ends the shell: a hangup, as a closed terminal window sends, then
// a kill of its process group if it has not gone. It returns once the
// shell is gone.
func (s *Session) Close() {
	select {
	case <-s.done:
		return
	default:
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGHUP)
	select {
	case <-s.done:
	case <-time.After(hangupGrace):
		s.cancel()
		<-s.done
	}
}

// idleSince is when the last page left, zero while one is attached.
func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}

// winsize clamps a page's idea of its size to something a shell can use.
func winsize(cols, rows int) *pty.Winsize {
	return &pty.Winsize{
		Cols: uint16(min(max(cols, 2), 500)),
		Rows: uint16(min(max(rows, 1), 200)),
	}
}

// Registry holds the running sessions, one per key.
type Registry struct {
	max   int
	idle  time.Duration
	shell string
	now   func() time.Time
	stop  chan struct{}

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
}

// NewRegistry builds a registry that runs at most max shells and ends one
// nobody has been attached to for idle. Close it to end them all.
func NewRegistry(max int, idle time.Duration) *Registry {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	r := &Registry{
		max:      max,
		idle:     idle,
		shell:    shell,
		now:      time.Now,
		stop:     make(chan struct{}),
		sessions: map[string]*Session{},
	}
	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				r.Sweep()
			}
		}
	}()
	return r
}

// Open answers key's session, starting a shell in dir when it has none;
// created says which. cols and rows size a new shell's window.
func (r *Registry) Open(key, dir string, cols, rows int) (_ *Session, created bool, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false, errors.New("the board is closing")
	}
	if s, ok := r.sessions[key]; ok {
		return s, false, nil
	}
	if len(r.sessions) >= r.max {
		return nil, false, ErrTooMany
	}
	s, err := start(r.shell, dir, cols, rows, r.now())
	if err != nil {
		return nil, false, err
	}
	r.sessions[key] = s
	go s.run(func() {
		r.mu.Lock()
		if r.sessions[key] == s {
			delete(r.sessions, key)
		}
		r.mu.Unlock()
	})
	return s, true, nil
}

// End closes key's session, if it has one.
func (r *Registry) End(key string) {
	r.mu.Lock()
	s := r.sessions[key]
	r.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// Sweep ends the sessions nobody has been attached to for the idle time,
// and the ones whose directory is gone (a card cleaned or landed).
func (r *Registry) Sweep() {
	now := r.now()
	r.mu.Lock()
	var end []*Session
	for _, s := range r.sessions {
		since := s.idleSince()
		if !since.IsZero() && now.Sub(since) >= r.idle {
			end = append(end, s)
		} else if _, err := os.Stat(s.Dir); err != nil {
			end = append(end, s)
		}
	}
	r.mu.Unlock()
	for _, s := range end {
		s.Close()
	}
}

// Close ends every session and refuses new ones.
func (r *Registry) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.stop)
	all := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		all = append(all, s)
	}
	r.mu.Unlock()
	for _, s := range all {
		s.Close()
	}
}
