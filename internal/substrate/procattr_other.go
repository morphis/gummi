//go:build !linux

package substrate

import "syscall"

// phaseProcAttr puts a command in its own process group, so a timeout can
// take its whole tree. Platforms without a parent-death signal cannot do
// more: a runner killed outright leaves its command running.
func phaseProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// ProcessStart cannot be answered without /proc.
func ProcessStart(int) (uint64, bool) { return 0, false }

// KillGroup does nothing where a process cannot be identified beyond its
// pid: killing a group that may have been reused is worse than leaving it.
func KillGroup(int, uint64) bool { return false }
