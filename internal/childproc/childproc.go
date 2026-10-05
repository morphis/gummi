// Package childproc starts the processes gummi supervises — agent
// backends, their model-catalog probes, a freeform session's watches — so
// that they never outlive gummi.
//
// Two mechanisms cover two ways of ending. A clean shutdown cancels the
// command, and Group's Cancel kills its whole process group: the child and
// every tool subprocess it spawned (bash, editors, an MCP child such as
// `gummi __mcp`). A gummi that is killed outright runs no Cancel at all, and
// that is what Start is for: on Linux it asks the kernel to SIGKILL the
// child the moment gummi dies.
//
// That signal reaches only the direct child. Its own children are
// reparented and outlive it unless they notice it go: a `gummi __mcp`
// shim does (its stdio pipe to the dead agent closes), a tool command the
// agent had running does not and runs to its own end. Only the group kill
// reaches those, and nothing is left to send it — the cost of being
// killed outright, bounded by how long one tool command runs.
//
// Without the signal, an `opencode serve` (which reads no stdin and so
// never sees gummi go) would sit on its port for as long as the machine
// stays up.
package childproc

import (
	"bytes"
	"os/exec"
	"syscall"
	"time"
)

// waitDelay bounds how long Wait waits for the pipes once the child is gone:
// a lingering grandchild can hold them open, and Wait must not hang on it.
const waitDelay = 2 * time.Second

// Group puts cmd in a process group of its own and makes cancelling it kill
// the whole group, with WaitDelay force-closing the pipes if a grandchild
// lingers. cmd must come from exec.CommandContext — exec refuses a Cancel
// on any other. Start the command with Start, not cmd.Start, so it also
// dies with gummi. Fields of an existing SysProcAttr are kept.
func Group(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setDeathSignal(cmd.SysProcAttr)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			// the kill is delivered asynchronously: wait for the group to
			// be gone, so that once Wait returns nothing of it still runs
			// — and nothing still writes into a directory the caller is
			// about to remove
			awaitGroupGone(cmd.Process.Pid, waitDelay)
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
}

// Output is cmd.Output for a command set up with Group: it starts cmd with
// Start and returns its stdout once it exits.
func Output(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := Start(cmd); err != nil {
		return nil, err
	}
	err := cmd.Wait()
	return out.Bytes(), err
}
