//go:build unix

package publish

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, with no controlling terminal
// (ssh cannot prompt on /dev/tty, which would also corrupt the TUI), and
// makes cancelling it kill the whole session's group, ssh included.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
