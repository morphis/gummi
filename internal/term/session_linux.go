//go:build linux

package term

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

// killSession kills every process in session sid, read from
// /proc/<pid>/stat: "pid (comm) state ppid pgrp session ...", where comm
// may itself hold spaces and parentheses, so the fields are taken after
// its last ")".
func killSession(sid int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	want := []byte(strconv.Itoa(sid))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
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
		if f := bytes.Fields(stat[i+1:]); len(f) > 3 && bytes.Equal(f[3], want) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
