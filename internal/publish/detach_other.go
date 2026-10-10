//go:build !unix

package publish

import "os/exec"

// detach has no session to start where there is no unix process model.
func detach(*exec.Cmd) {}
