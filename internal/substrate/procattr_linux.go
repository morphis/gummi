//go:build linux

package substrate

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// phaseProcAttr puts a command in its own process group, so a timeout can
// take its whole tree, and asks the kernel to kill it if the process that
// started it dies. The second half is what keeps a lease honest: the lease
// goes the moment its holder dies, and a deploy that outlived its runner
// would go on working a substrate the next holder believes it has to
// itself.
//
// The parent-death signal reaches ONLY the shell. Everything it started —
// which for an ordinary operator command is everything that does the work —
// is reparented and carries on. That is what the process group is for a
// second time: the run record names it, and whoever reads the run back and
// finds its runner gone kills the group (KillGroup). The signal is the
// fast path; the record is the one that holds.
func phaseProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// ProcessStart reads a process's start time — the 22nd field of
// /proc/<pid>/stat, in clock ticks since boot. A pid alone does not name a
// process for long (the kernel reuses them); a pid and the moment it
// started does, for as long as the machine is up.
func ProcessStart(pid int) (uint64, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// field 2 is the command in parentheses and may hold spaces and
	// parentheses of its own, so everything is counted from the last one.
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 20 {
		return 0, false
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

// KillGroup kills the process group led by pgid, and reports whether it
// did. It kills nothing unless the leader is still the process that
// started at start: a pid that has been reused belongs to somebody else.
func KillGroup(pgid int, start uint64) bool {
	if pgid <= 1 || start == 0 {
		return false
	}
	if now, ok := ProcessStart(pgid); !ok || now != start {
		return false
	}
	return syscall.Kill(-pgid, syscall.SIGKILL) == nil
}
