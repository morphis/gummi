package term

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRegistry(t *testing.T, max int) *Registry {
	t.Helper()
	r := NewRegistry(max, time.Hour)
	r.shell = "/bin/sh"
	t.Cleanup(r.Close)
	return r
}

// readUntil collects a subscription's output until it holds want.
func readUntil(t *testing.T, sub *Sub, have []byte, want string) string {
	t.Helper()
	out := bytes.Clone(have)
	deadline := time.After(10 * time.Second)
	for !strings.Contains(string(out), want) {
		select {
		case chunk, ok := <-sub.C:
			if !ok {
				t.Fatalf("the shell ended before printing %q; got %q", want, out)
			}
			out = append(out, chunk...)
		case <-deadline:
			t.Fatalf("no %q within 10s; got %q", want, out)
		}
	}
	return string(out)
}

func TestAShellRunsInItsDirectoryAndEchoesWhatIsTyped(t *testing.T) {
	dir := t.TempDir()
	r := testRegistry(t, 2)
	s, created, err := r.Open("FF-1", dir, 80, 24)
	if err != nil || !created {
		t.Fatalf("Open = created %v, err %v", created, err)
	}
	tail, sub := s.Attach()
	// the marker is assembled by the shell, so the echo of the typed line
	// cannot be mistaken for its output
	if err := s.Write([]byte("echo at:$(pwd -P):end; stty size\n")); err != nil {
		t.Fatal(err)
	}
	out := readUntil(t, sub, tail, "24 80")
	if real, err := filepath.EvalSymlinks(dir); err != nil || !strings.Contains(out, "at:"+real+":end") {
		t.Errorf("the shell did not start in %s: %q", dir, out)
	}
}

func TestAPageThatComesBackIsReplayedWhatItMissed(t *testing.T) {
	r := testRegistry(t, 2)
	s, _, err := r.Open("FF-1", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	tail, sub := s.Attach()
	_ = s.Write([]byte("echo first-$((40+2))\n"))
	readUntil(t, sub, tail, "first-42")
	sub.Detach(time.Now())

	again, created, err := r.Open("FF-1", t.TempDir(), 80, 24)
	if err != nil || created || again != s {
		t.Fatalf("a second Open = %p created %v err %v, want the same session %p", again, created, err, s)
	}
	tail, sub = again.Attach()
	if !strings.Contains(string(tail), "first-42") {
		t.Errorf("the replay lacks what the shell printed before: %q", tail)
	}
	s.Resize(100, 30)
	_ = s.Write([]byte("stty size\n"))
	readUntil(t, sub, nil, "30 100")
}

func TestExitEndsTheSessionAndFreesItsSlot(t *testing.T) {
	r := testRegistry(t, 1)
	s, _, err := r.Open("FF-1", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Open("FF-2", t.TempDir(), 80, 24); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a second shell past the cap = %v, want ErrTooMany", err)
	}
	_, sub := s.Attach()
	_ = s.Write([]byte("exit 3\n"))
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end on exit")
	}
	if s.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", s.ExitCode())
	}
	for range sub.C { // drained, then closed
	}
	if _, sub := s.Attach(); func() bool { _, ok := <-sub.C; return ok }() {
		t.Error("attaching to an ended session left its channel open")
	}
	waitFor(t, func() bool {
		_, created, err := r.Open("FF-2", t.TempDir(), 80, 24)
		return err == nil && created
	})
}

func TestSweepEndsIdleShellsAndThoseWhoseDirectoryIsGone(t *testing.T) {
	r := testRegistry(t, 4)
	now := time.Now()
	r.now = func() time.Time { return now }

	gone := t.TempDir()
	lost, _, _ := r.Open("gone", gone, 80, 24)
	idle, _, _ := r.Open("idle", t.TempDir(), 80, 24)
	held, _, _ := r.Open("held", t.TempDir(), 80, 24)
	_, sub := held.Attach()
	defer sub.Detach(now)

	r.Sweep()
	for _, s := range []*Session{lost, idle, held} {
		select {
		case <-s.Done():
			t.Fatal("a fresh session was swept")
		default:
		}
	}

	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	r.Sweep()
	for name, s := range map[string]*Session{"gone": lost, "idle": idle} {
		select {
		case <-s.Done():
		default:
			t.Errorf("the %s session survived the sweep", name)
		}
	}
	select {
	case <-held.Done():
		t.Error("a session with a page attached was swept")
	default:
	}
}

func TestCloseEndsEveryShell(t *testing.T) {
	r := testRegistry(t, 2)
	s, _, err := r.Open("FF-1", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// a shell that ignores the hangup is killed, and so is what it runs,
	// which holds the pty and is in a process group of its own
	_, sub := s.Attach()
	_ = s.Write([]byte("trap '' HUP INT; echo trap-$((1+1)); sleep 300\n"))
	readUntil(t, sub, nil, "trap-2")
	began := time.Now()
	r.Close()
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Close took %s", took)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Close returned with a shell still running")
	}
	if _, _, err := r.Open("FF-2", t.TempDir(), 80, 24); err == nil {
		t.Error("a closed registry started a shell")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
