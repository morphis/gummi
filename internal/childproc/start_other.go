//go:build !linux

package childproc

import (
	"os/exec"
	"syscall"
)

// setDeathSignal does nothing: only Linux has a parent-death signal. A
// gummi killed outright on another platform leaves its children running
// until they notice it gone on their own.
func setDeathSignal(*syscall.SysProcAttr) {}

// Start is cmd.Start where there is no parent-death signal to tie to a
// thread.
func Start(cmd *exec.Cmd) error { return cmd.Start() }
