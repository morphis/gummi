//go:build linux

package substrate

import "syscall"

// phaseProcAttr puts a command in its own process group, so a timeout can
// take its whole tree, and asks the kernel to kill it if the process that
// started it dies. The second half is what keeps a lease honest: the lease
// goes the moment its holder dies, and a deploy that outlived its runner
// would go on working a substrate the next holder believes it has to
// itself. Only the shell is signalled — a grandchild that has detached
// itself can still escape — but that is the command an operator wrote, and
// it is the common case by a long way.
func phaseProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
