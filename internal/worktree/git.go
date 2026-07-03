package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// gitError carries the command and its stderr so failures are
// diagnosable without gummi ever interpolating anything into a shell.
type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = e.err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), msg)
}

func (e *gitError) Unwrap() error { return e.err }

// runGit executes git with an argument array (never a shell) in dir.
// It returns trimmed stdout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitError{args: full, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitOK runs git and reports only success/failure (for predicates such
// as merge-base --is-ancestor, where exit status is the answer).
func gitOK(ctx context.Context, dir string, args ...string) (bool, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, &gitError{args: full, stderr: stderr.String(), err: err}
}
