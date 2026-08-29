package ui

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// TestEmulatorDrawsIntoRepoUltravioletScreenBuffer pins the load-bearing
// assumption behind this whole file: x/vt names an older ultraviolet in
// its own go.mod, but Go's MVS resolves the actual build to this repo's
// newer pin (go.mod), and the two have stayed wire-compatible — a
// SafeEmulator composites correctly, with 24-bit color and cursor
// position intact, into a uv.Screen built at the repo's version. If a
// future bump of either module breaks that wire compatibility (a
// renamed/reshaped Style, a Draw signature change, a cell model that no
// longer round-trips truecolor), this test fails loudly here instead of
// letting agentView silently render garbage.
func TestEmulatorDrawsIntoRepoUltravioletScreenBuffer(t *testing.T) {
	emu := vt.NewSafeEmulator(10, 2)

	// Bold + 24-bit truecolor foreground, then two printable cells. No
	// trailing newline: this only needs to prove the composite works, not
	// exercise scrolling.
	if _, err := emu.Write([]byte("\x1b[1m\x1b[38;2;10;200;30mhi")); err != nil {
		t.Fatalf("emu.Write: %v", err)
	}

	scr := uv.NewScreenBuffer(10, 2)
	emu.Draw(scr, scr.Bounds())

	h, i := scr.CellAt(0, 0), scr.CellAt(1, 0)
	if h == nil || i == nil {
		t.Fatalf("expected drawn cells at (0,0) and (1,0), got %v, %v", h, i)
	}
	if h.Content != "h" || i.Content != "i" {
		t.Fatalf("cell content = %q, %q; want \"h\", \"i\"", h.Content, i.Content)
	}
	if h.Style.Attrs&uv.AttrBold == 0 {
		t.Fatalf("expected the bold attribute to survive Write+Draw, got Attrs=%b", h.Style.Attrs)
	}
	if h.Style.Fg == nil {
		t.Fatalf("expected a foreground color to survive Write+Draw, got nil")
	}
	r, g, b, _ := h.Style.Fg.RGBA()
	// color.Color.RGBA is 16-bit-scaled (each 8-bit channel duplicated),
	// so an exact 8-bit truecolor value comes back as value*257.
	if got := [3]uint32{r >> 8, g >> 8, b >> 8}; got != [3]uint32{10, 200, 30} {
		t.Fatalf("fg rgb = %v, want [10 200 30] (truecolor lost precision across Draw)", got)
	}

	if cur := emu.CursorPosition(); cur != uv.Pos(2, 0) {
		t.Fatalf("cursor position = %v, want (2,0) after writing 2 cells", cur)
	}
}

// requireUnixShell finds a `sh` to drive the real-child tests with, or
// skips: pty hosting is a unix-only feature (see winsize_unsupported.go
// in creack/pty), and a container image missing /bin/sh would otherwise
// fail these tests for an unrelated reason.
func requireUnixShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("agentview hosts a pty, which creack/pty only supports on unix")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no `sh` on PATH to drive the child process")
	}
	return sh
}

// screenText flattens a's current emulator screen into a plain string
// (styles dropped) so tests can assert on rendered content with
// strings.Contains instead of walking cells by hand.
func screenText(a *agentView) string {
	w, h := a.emu.Width(), a.emu.Height()
	scr := uv.NewScreenBuffer(w, h)
	a.Draw(scr, scr.Bounds())
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if c := scr.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
	}
	return b.String()
}

// waitForRender drives a's Wait() pump (the same one a real Shell's
// Update loop would drive) until want shows up on screen, the child
// exits, or budget runs out — whichever comes first. It never blocks
// past budget: each Wait() call races against a timer on its own
// goroutine, so a bug that stops signaling on both outCh and doneCh
// fails the test instead of hanging it.
func waitForRender(t *testing.T, a *agentView, want string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if strings.Contains(screenText(a), want) {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for %q to render; last screen:\n%s", want, screenText(a))
		}
		msgCh := make(chan tea.Msg, 1)
		go func() { msgCh <- a.Wait()() }()
		select {
		case msg := <-msgCh:
			if em, ok := msg.(agentExitedMsg); ok {
				t.Fatalf("child exited before %q rendered: %v", want, em.err)
			}
		case <-time.After(remaining):
			t.Fatalf("timed out waiting for %q to render; last screen:\n%s", want, screenText(a))
		}
	}
}

// TestNewAgentViewRendersChildOutput drives a real child through a real
// pty end to end: spawn, observe it's alive, watch its styled output
// land in the emulator via the Wait() pump, then tear it down. The child
// sleeps after printing so Alive() has something to observe before it
// exits on its own — the test ends it early with Close rather than
// waiting out the sleep.
func TestNewAgentViewRendersChildOutput(t *testing.T) {
	sh := requireUnixShell(t)
	dir := t.TempDir()

	a, err := newAgentView([]string{sh, "-c", `printf '\033[1;32mhi\033[m\r\n'; sleep 5`}, dir, 20, 5)
	if err != nil {
		t.Fatalf("newAgentView: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if !a.Alive() {
		t.Fatalf("expected a freshly spawned child to be alive")
	}

	waitForRender(t, a, "hi", 5*time.Second)

	scr := uv.NewScreenBuffer(20, 5)
	a.Draw(scr, scr.Bounds())
	cell := scr.CellAt(0, 0)
	if cell == nil || cell.Style.Fg == nil {
		t.Fatalf("expected a styled cell at (0,0), got %v", cell)
	}
	r, g, b, _ := cell.Style.Fg.RGBA()
	if g <= r || g <= b {
		t.Fatalf("expected the child's green SGR to survive to Draw, got rgb=(%d,%d,%d)", r>>8, g>>8, b>>8)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close on a deliberate teardown should report nil, got %v", err)
	}
	if a.Alive() {
		t.Fatalf("expected Close to end the child")
	}
	if a.cmd.ProcessState == nil {
		t.Fatalf("expected Close to reap the child (ProcessState unset — a zombie process)")
	}
}

// TestAgentViewResizeUpdatesEmulatorAndPty proves Resize hits both halves
// it must: the emulator's cell grid (what Draw renders) and the pty's
// kernel winsize (what the child's TIOCGWINSZ/SIGWINCH sees).
func TestAgentViewResizeUpdatesEmulatorAndPty(t *testing.T) {
	sh := requireUnixShell(t)
	dir := t.TempDir()

	a, err := newAgentView([]string{sh, "-c", "sleep 5"}, dir, 10, 4)
	if err != nil {
		t.Fatalf("newAgentView: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	if err := a.Resize(30, 12); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if w, h := a.emu.Width(), a.emu.Height(); w != 30 || h != 12 {
		t.Fatalf("emulator size = %dx%d, want 30x12", w, h)
	}

	ws, err := pty.GetsizeFull(a.ptmx)
	if err != nil {
		t.Fatalf("pty.GetsizeFull: %v", err)
	}
	if int(ws.Cols) != 30 || int(ws.Rows) != 12 {
		t.Fatalf("pty size = %dx%d, want 30x12", ws.Cols, ws.Rows)
	}
}

// TestAgentViewSendKeyForwardsToChild proves keystrokes make the round
// trip: SendKey encodes into the emulator's input pipe, pumpInput copies
// that into the pty master, and the child reads it back. `stty -echo` on
// the child's own tty keeps the assertion to just the child's output,
// rather than also matching the pty line discipline's local echo of the
// typed characters.
func TestAgentViewSendKeyForwardsToChild(t *testing.T) {
	sh := requireUnixShell(t)
	dir := t.TempDir()

	a, err := newAgentView([]string{sh, "-c", `stty -echo; read line; printf 'got:%s\n' "$line"`}, dir, 40, 5)
	if err != nil {
		t.Fatalf("newAgentView: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	for _, r := range "hi" {
		a.SendKey(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	a.SendKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	waitForRender(t, a, "got:hi", 5*time.Second)
}

// TestAgentViewCloseIsIdempotentAndSuppressesTheKillItCaused is the
// regression test for the review finding: Close kills the child with
// Process.Kill(), which makes cmd.Wait() come back with a
// "signal: killed" *exec.ExitError — that's the deliberate teardown
// succeeding, not a failure, so Close (and any agentExitedMsg still in
// flight) must report nil for it. A second, concurrent-or-later Close
// call must also come back nil without repeating the kill/close/reap.
func TestAgentViewCloseIsIdempotentAndSuppressesTheKillItCaused(t *testing.T) {
	sh := requireUnixShell(t)
	dir := t.TempDir()

	a, err := newAgentView([]string{sh, "-c", "sleep 30"}, dir, 20, 5)
	if err != nil {
		t.Fatalf("newAgentView: %v", err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close on a deliberate teardown of a live child should report nil, got %v", err)
	}
	if a.Alive() {
		t.Fatalf("expected Close to end the child")
	}
	if a.cmd.ProcessState == nil {
		t.Fatalf("expected Close to reap the child (ProcessState unset — a zombie process)")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("a second, idempotent Close call should also report nil, got %v", err)
	}
}

// TestAgentViewSurfacesANaturalNonZeroExit proves the other half of the
// same fix: Close suppresses only the exit error it itself causes. A
// child that dies on its own, with nothing calling Close, still reports
// its real exit error — both from Wait's agentExitedMsg and from a
// later Close call — so a real failure is never swallowed along with the
// expected "signal: killed" noise.
func TestAgentViewSurfacesANaturalNonZeroExit(t *testing.T) {
	sh := requireUnixShell(t)
	dir := t.TempDir()

	a, err := newAgentView([]string{sh, "-c", "exit 3"}, dir, 20, 5)
	if err != nil {
		t.Fatalf("newAgentView: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	deadline := time.Now().Add(5 * time.Second)
	var exitErr error
	var sawExit bool
	for !sawExit {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for the child's own exit to be reported")
		}
		msgCh := make(chan tea.Msg, 1)
		go func() { msgCh <- a.Wait()() }()
		select {
		case msg := <-msgCh:
			if em, ok := msg.(agentExitedMsg); ok {
				sawExit = true
				exitErr = em.err
			}
		case <-time.After(remaining):
			t.Fatalf("timed out waiting for the child's own exit to be reported")
		}
	}

	if exitErr == nil {
		t.Fatalf("expected a non-nil exit error for a child that exited 3 on its own, got nil")
	}
	var exitError *exec.ExitError
	if !errors.As(exitErr, &exitError) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", exitErr, exitErr)
	}
	if exitError.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", exitError.ExitCode())
	}

	// A later Close must not clobber this into nil just because Close
	// always sends a Kill: the child was already gone by the time Close
	// ran, so nothing Close did caused this exit.
	if err := a.Close(); err == nil {
		t.Fatalf("expected Close to still surface the child's own exit error, got nil")
	}
}
