//go:build linux

package childproc

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv makes the test binary act as a stand-in gummi: it starts a
// long sleep through Start, prints the sleep's pid, and waits to be killed.
const helperEnv = "GUMMI_CHILDPROC_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		cmd := exec.CommandContext(context.Background(), "sleep", "300")
		Group(cmd)
		if err := Start(cmd); err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		fmt.Println(cmd.Process.Pid)
		select {}
	}
	os.Exit(m.Run())
}

// alive reports whether pid names a running process — a zombie is dead,
// whoever has yet to reap it.
func alive(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAChildDiesWithAKilledParent: a parent killed outright runs no
// cleanup of its own, and its child still goes.
func TestAChildDiesWithAKilledParent(t *testing.T) {
	helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), helperEnv+"=1")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = helper.Process.Kill()
		t.Fatalf("reading the child's pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		_ = helper.Process.Kill()
		t.Fatalf("helper said %q", line)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if !alive(child) {
		t.Fatal("the child was not running before its parent died")
	}

	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	waitFor(t, "the child to die with its parent", func() bool { return !alive(child) })
}

// TestAChildOutlivesARetiredThread: the parent-death signal follows the
// thread that forked the child, and a goroutine that exits locked to its
// thread retires that thread. Start is called here from exactly such a
// goroutine: a plain cmd.Start in its place forks from the doomed thread,
// and the child is killed the moment the goroutine returns, with its
// parent still running.
func TestAChildOutlivesARetiredThread(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sleep", "300")
	Group(cmd)
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread exits with us
		// The main thread is wedged rather than retired; hop off it.
		for syscall.Gettid() == syscall.Getpid() {
			runtime.UnlockOSThread()
			runtime.Gosched()
			runtime.LockOSThread()
		}
		done <- Start(cmd)
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	time.Sleep(300 * time.Millisecond)
	if !alive(cmd.Process.Pid) {
		t.Fatal("the child died with a retired thread while its parent ran on")
	}
}

// TestGroupKeepsTheCallersAttributes: Group adds to a SysProcAttr, it
// does not replace one.
func TestGroupKeepsTheCallersAttributes(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Noctty: true}
	Group(cmd)
	a := cmd.SysProcAttr
	if !a.Noctty || !a.Setpgid || a.Pdeathsig != syscall.SIGKILL {
		t.Errorf("SysProcAttr = %+v", a)
	}
	if cmd.Cancel == nil || cmd.WaitDelay == 0 {
		t.Error("Group set no group kill")
	}
}

// TestCancelWaitsForTheGroupToGo: cancelling a grouped command kills its
// whole group, and Wait does not return while any member still runs — a
// grandchild included — so nothing of it outlives the call that ended it.
func TestCancelWaitsForTheGroupToGo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// the shell leaves a grandchild behind it in the group, as an agent's
	// tool command or a snap launcher's real binary would
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & sleep 300")
	Group(cmd)
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for groupMembers(pgid) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := groupMembers(pgid); n < 2 {
		t.Fatalf("the group never had its grandchild (%d members)", n)
	}
	cancel()
	_ = cmd.Wait()
	if groupAlive(pgid) {
		t.Errorf("a member of group %d still runs after Wait returned", pgid)
	}
}

// groupMembers counts the live processes in group pgid.
func groupMembers(pgid int) int {
	entries, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range entries {
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(raw)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) >= 3 && f[2] == strconv.Itoa(pgid) && f[0] != "Z" {
			n++
		}
	}
	return n
}
