//go:build !linux

package substrate

import "syscall"

// phaseProcAttr puts a command in its own process group, so a timeout can
// take its whole tree. Platforms without a parent-death signal cannot do
// more: a runner killed outright leaves its command running.
func phaseProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
