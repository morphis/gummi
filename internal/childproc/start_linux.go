//go:build linux

package childproc

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// setDeathSignal asks the kernel to SIGKILL the child when its parent dies.
func setDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}

// Start starts cmd from the spawner thread.
//
// The parent-death signal is not tied to the parent process: the kernel
// sends it when the *thread* that forked the child exits. Go usually keeps
// its threads for the life of the process, but not always — a goroutine
// that exits while locked to its thread (runtime.LockOSThread without the
// unlock) takes the thread with it, and a later goroutine can land on the
// thread a child was forked from. When that thread went, the child would
// be killed with gummi still running and its session would die mid-turn
// for no reason anyone could see.
//
// So every child is forked from one goroutine that locks itself to its
// thread and never returns: that thread is the process's until the process
// ends, so the signal fires on gummi's death and on nothing else. Forks are
// fast, so funnelling them through one goroutine costs nothing worth
// measuring.
func Start(cmd *exec.Cmd) error {
	spawnerOnce.Do(func() { go spawner() })
	done := make(chan error, 1)
	spawns <- spawnReq{cmd: cmd, done: done}
	return <-done
}

type spawnReq struct {
	cmd  *exec.Cmd
	done chan<- error
}

var (
	spawnerOnce sync.Once
	spawns      = make(chan spawnReq)
)

func spawner() {
	runtime.LockOSThread() // never unlocked: see Start
	for r := range spawns {
		r.done <- r.cmd.Start()
	}
}

// awaitGroupGone waits, at most timeout, until no live process is left in
// process group pgid. A zombie — the group leader itself, until Wait reaps
// it — no longer runs, so it does not count.
func awaitGroupGone(pgid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for groupAlive(pgid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// groupAlive reports whether any process in group pgid still runs, read
// from /proc/<pid>/stat: "pid (comm) state ppid pgrp ...", where comm may
// itself hold spaces and parentheses, so the fields are taken after its
// last ")".
func groupAlive(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	want := strconv.Itoa(pgid)
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) >= 3 && f[2] == want && f[0] != "Z" && f[0] != "X" {
			return true
		}
	}
	return false
}
