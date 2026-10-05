//go:build linux

package childproc

import (
	"os/exec"
	"runtime"
	"sync"
	"syscall"
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
