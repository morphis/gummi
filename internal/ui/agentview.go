package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// agentView hosts a real terminal program — the user's own coding CLI,
// resolved the same way rawattach.go resolves it (defaultAttachCommand /
// GUMMI_ATTACH_CMD) — inside a pty, and renders its screen as an
// ultraviolet-drawable surface so it can sit in a tab of gummi's own TUI
// instead of taking over the real terminal the way attachRaw does.
//
// It is deliberately Shell-agnostic: it owns the pty, the child process,
// and a headless terminal emulator, and exposes only Draw/Resize/input/
// lifecycle methods. Wiring it into a tab (spawning it on tab-enter,
// forwarding key messages, repainting on Wait()'s messages, tearing it
// down on tab-leave or shell exit) is the caller's job.
//
// The terminal emulation is github.com/charmbracelet/x/vt's Emulator,
// wrapped in its SafeEmulator for the concurrency this type needs: one
// goroutine feeds it bytes read from the pty while Draw (called from the
// main render loop) reads its cell grid at the same time. x/vt composites
// directly into this repo's ultraviolet.Screen — see
// TestEmulatorDrawsIntoRepoUltravioletScreenBuffer, which pins that
// assumption so a future bump of either module can't silently break it
// (x/vt names an older ultraviolet in its own go.mod; Go's MVS resolves
// the build to this repo's newer pin, and the two have stayed
// wire-compatible so far — but "so far" is exactly the kind of thing a
// dependency bump invalidates without a test to catch it).
type agentView struct {
	cmd  *exec.Cmd
	ptmx *os.File
	emu  *vt.SafeEmulator

	// outCh wakes Wait's listener after a chunk of pty output has been
	// folded into emu. It is capacity 1 and fed with a non-blocking send,
	// so a burst of small reads (the common case: a CLI streaming tokens)
	// coalesces into a single wakeup instead of queuing one per read(2) —
	// Wait only needs to know "there's something new to draw", not how
	// many writes produced it.
	outCh chan struct{}
	// doneCh is closed exactly once, by the read pump, the moment the pty
	// stops producing output (the child exited, or Close tore it down).
	// Closing rather than sending lets every blocked and future Wait call
	// observe it immediately and repeatedly.
	doneCh chan struct{}

	mu      sync.Mutex
	exitErr error // valid once doneCh is closed
	// closing is set by Close before it signals the child, so pumpOutput
	// can tell "the child died because Close killed it" (report nil — the
	// caller asked for this) apart from "the child died on its own"
	// (report the real exit error). It also makes Close idempotent: a
	// second caller sees it already set and just waits for the first
	// call's teardown to finish instead of repeating it.
	closing bool
}

// newAgentView spawns argv[0] (with the rest of argv as its arguments) in
// dir, attached to a new pty sized w×h, and starts copying bytes between
// the pty and a headless terminal emulator. argv/dir are expected to come
// from resolveAttach (rawattach.go) — this type does not resolve the
// backend itself, to avoid a second copy of that policy.
func newAgentView(argv []string, dir string, w, h int) (*agentView, error) {
	return newAgentViewEnv(argv, dir, w, h, nil)
}

// newAgentViewEnv is newAgentView plus extra environment entries for the
// child, in exec.Cmd's "KEY=value" form. The agent tab uses it to inject
// GUMMI_MCP_SOCK, which is what lets the hosted CLI's `gummi __mcp
// --workspace` child reach *this* process's engine rather than starting a
// second gummi that would then contend for the very card locks this board
// holds. The path is injected, never guessed, so the child needs no
// discovery logic and two boards on one workspace can't cross wires.
func newAgentViewEnv(argv []string, dir string, w, h int, extraEnv []string) (*agentView, error) {
	if len(argv) == 0 {
		return nil, errors.New("agentview: empty argv")
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is operator config, resolved by resolveAttach
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.Env = append(cmd.Env, extraEnv...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	if err != nil {
		return nil, fmt.Errorf("agentview: start %s: %w", argv[0], err)
	}

	a := &agentView{
		cmd:    cmd,
		ptmx:   ptmx,
		emu:    vt.NewSafeEmulator(w, h),
		outCh:  make(chan struct{}, 1),
		doneCh: make(chan struct{}),
	}
	go a.pumpOutput() // pty -> emulator (what the user sees)
	go a.pumpInput()  // emulator -> pty (what SendKey/Paste queue up)
	return a, nil
}

// pumpOutput is the sole reader of the pty master and the sole writer of
// a.emu's screen state. It runs until the pty stops producing output,
// which on Linux surfaces as a read error (typically EIO) rather than
// io.EOF once the child's side of the pty closes — either way, any read
// error ends the pump and reaps the child so it never lingers as a
// zombie.
func (a *agentView) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := a.ptmx.Read(buf)
		if n > 0 {
			_, _ = a.emu.Write(buf[:n]) // SafeEmulator.Write never errors on well-formed input
			select {
			case a.outCh <- struct{}{}:
			default: // a wakeup is already pending — this chunk rides along with it
			}
		}
		if err != nil {
			waitErr := a.cmd.Wait() // reap: without this the child is left a zombie
			a.mu.Lock()
			if a.closing {
				// Close's Kill is exactly what produced this exit (typically
				// an *exec.ExitError wrapping "signal: killed") — that's the
				// requested shutdown succeeding, not a failure to report.
				a.exitErr = nil
			} else {
				a.exitErr = waitErr
			}
			a.mu.Unlock()
			close(a.doneCh)
			return
		}
	}
}

// pumpInput drains the emulator's input pipe — the bytes SendKey, Paste,
// and SendText encode — into the pty master, so a keystroke fed to the
// emulator actually reaches the child. It is parked rather than stopped
// at teardown — see Close for why the emulator is deliberately not
// closed, and why leaving this goroutine blocked is the lesser evil.
func (a *agentView) pumpInput() {
	_, _ = io.Copy(a.ptmx, a.emu)
}

// Draw composites the emulator's current screen into scr at area, letting
// the child's own colors, styles, and cursor glyph land on gummi's shared
// ultraviolet render surface exactly as they would in a native terminal.
// It is safe to call concurrently with the output pump — that's what
// SafeEmulator's RWMutex is for — and it renders the last frame even
// after the child has exited (Alive() == false): a dead agent's final
// screen stays visible until the tab is closed, rather than blanking.
func (a *agentView) Draw(scr uv.Screen, area uv.Rectangle) {
	a.emu.Draw(scr, area)
}

// SendKey forwards a key press to the hosted program, encoded the same
// way a real terminal would encode it (arrow keys, function keys, ctrl
// combinations, and so on) by x/vt's own key table. Every key reaches the
// child this way — including tab, esc, and ctrl+c — which is the whole
// point of hosting a full pty rather than a line-oriented chat box: the
// hosted CLI gets its own keybindings back.
func (a *agentView) SendKey(k tea.KeyPressMsg) {
	key := k.Key()
	// Built up field-by-field rather than by struct-converting tea.Key to
	// uv.KeyPressEvent: the two happen to be layout-identical today, but a
	// field added to one and not the other would make that conversion
	// silently wrong instead of a compile error.
	a.emu.SendKey(uv.KeyPressEvent{
		Text:        key.Text,
		Mod:         key.Mod,
		Code:        key.Code,
		ShiftedCode: key.ShiftedCode,
		BaseCode:    key.BaseCode,
	})
}

// SendMouse forwards a mouse event to the hosted program, pane-relative,
// encoded for whichever mouse tracking mode the child actually asked for
// — x/vt tracks the DECSET modes (X10, normal, button-event, any-event)
// and picks X10 or SGR encoding itself.
//
// A child that never enabled tracking gets nothing: x/vt drops the event
// rather than writing bytes nobody asked for. That is what makes it safe
// to forward unconditionally instead of second-guessing the child's mode
// from out here, which would mean parsing the pty stream twice.
func (a *agentView) SendMouse(m uv.MouseEvent) {
	a.emu.SendMouse(m)
}

// Paste forwards pasted text to the hosted program, bracketed per the
// child's own bracketed-paste mode setting (x/vt tracks that mode itself
// and only wraps the text when the child asked for it).
func (a *agentView) Paste(s string) {
	a.emu.Paste(s)
}

// Resize changes both halves of the pty pair that need to agree on
// terminal size: the emulator's own cell grid (so Draw produces the right
// shape) and the pty's kernel-side winsize (so the child gets SIGWINCH
// and reads back the new size via TIOCGWINSZ). Doing only one leaves the
// child believing it has a different size than what's actually rendered.
func (a *agentView) Resize(w, h int) error {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	a.emu.Resize(w, h)
	if err := pty.Setsize(a.ptmx, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)}); err != nil {
		return fmt.Errorf("agentview: resize pty: %w", err)
	}
	return nil
}

// CursorPosition reports the child's cursor, pane-relative (0,0 is the
// top-left cell of the hosted screen, not of wherever Draw placed it) —
// callers translate into absolute terms themselves, the same way every
// other cursor-owning view in this package does.
func (a *agentView) CursorPosition() uv.Position {
	return a.emu.CursorPosition()
}

// Alive reports whether the child is still running. It flips to false the
// instant the output pump observes the pty close, which is at most one
// read(2) after the child actually exits.
func (a *agentView) Alive() bool {
	select {
	case <-a.doneCh:
		return false
	default:
		return true
	}
}

// Wait returns a command, in this repo's usual pump idiom (see
// pump_test.go / ingestview.go's listenIngestSteps), that blocks until
// either new output has arrived or the child has exited, then delivers
// the corresponding message. Callers re-issue Wait after every
// agentOutputMsg to keep listening; an agentExitedMsg means the child is
// gone and there is nothing left to wait for — a later Wait call still
// returns immediately with the same agentExitedMsg (doneCh stays closed),
// it just has nothing new to report.
func (a *agentView) Wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-a.outCh:
			return agentOutputMsg{view: a}
		case <-a.doneCh:
			a.mu.Lock()
			err := a.exitErr
			a.mu.Unlock()
			return agentExitedMsg{view: a, err: err}
		}
	}
}

// Close tears the hosted program down: it kills the child (if it hasn't
// already exited on its own), closes the pty master, and blocks until the
// output pump has reaped the process, so Close never returns while a
// zombie is still pending. It returns nil for that deliberate teardown —
// the child dying because Close killed it is success, not an error worth
// surfacing to the user — but still returns the real exit error for a
// child that had already died on its own before Close ran. It is
// idempotent: calling it more than once (or concurrently) just waits for
// the first call's teardown and returns the same result, without a
// second Kill/ptmx.Close/emu.Close.
func (a *agentView) Close() error {
	a.mu.Lock()
	alreadyClosing := a.closing
	a.closing = true
	a.mu.Unlock()

	if !alreadyClosing {
		if a.cmd.Process != nil {
			// pty.StartWithSize sets Setsid, making the child a session
			// (and process group) leader with pgid == pid, so signaling
			// -pid reaches it and everything it forked — a hosted CLI's
			// own subprocesses, or (in tests) a shell script's un-exec'd
			// child. Killing only cmd.Process.Pid leaves such descendants
			// running and still holding the pty's slave fd open, which
			// keeps pumpOutput's Read blocked — and this Close call
			// waiting on doneCh below — until they exit on their own.
			// Every other subprocess owner in this repo (envprobe,
			// verify, the agent adapters) kills by process group for the
			// same reason.
			_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL) // best-effort: a race with natural exit just errors here, harmlessly
		}
		_ = a.ptmx.Close()
		// Deliberately NOT a.emu.Close(). x/vt's SafeEmulator guards
		// Write, Draw and Resize with its mutex but leaves Close and
		// Read unguarded, and both touch Emulator.closed — so closing
		// the emulator while pumpInput sits in its Read is a data
		// race, which `go test -race` reports against emulator.go's
		// Close and Read. Closing it is also the only way to make that
		// Read return, so the choice is between a real race and
		// parking one goroutine.
		//
		// We park it. pumpInput is left blocked on an io.Pipe read that
		// will never be satisfied: it holds no lock, burns no CPU, and
		// dies with the process. A shell hosts one agent at a time, so
		// this is bounded by hosted sessions rather than by anything
		// that grows. If x/vt ever guards Close, this goes back to
		// being a plain a.emu.Close().
	}
	<-a.doneCh // pumpOutput's cmd.Wait() is the actual reap; wait for it to finish (idempotent: already closed if a second caller races in)

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exitErr
}

// agentOutputMsg is delivered by (*agentView).Wait whenever new output
// from the hosted program has been folded into its emulator. It carries
// no data of its own — the terminal state lives in the emulator, which
// Draw reads directly — so handling it is just "repaint, then call Wait
// again".
type agentOutputMsg struct {
	view *agentView
}

// agentExitedMsg is delivered once (though a later Wait keeps returning
// it) when the hosted program's pty has closed. err is the child's real
// exit error when it died on its own (nil for a clean exit), but is
// always nil when Close initiated the shutdown — a caller that reacts to
// this by surfacing err as a notice would otherwise show a spurious
// "signal: killed" every time the user leaves the agent tab.
type agentExitedMsg struct {
	view *agentView
	err  error
}
