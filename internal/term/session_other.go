//go:build !linux

package term

// killSession is the shell's process group only where a session's members
// cannot be listed: Close kills that next.
func killSession(int) {}
