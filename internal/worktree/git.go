package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	out, err := runGitRaw(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runGitRaw is runGit without the trim. TrimSpace must not touch a
// -z porcelain stream: its first record's leading status byte is often a
// space for an unstaged edit, which would strip the "X" and corrupt the
// field, and trimming the trailing bytes is wrong for a NUL-delimited
// record stream regardless.
func runGitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitError{args: full, stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
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

// runGitEnv is runGit with extra environment entries, for the few
// commands that take an identity from the environment (commit-tree's
// author). The child inherits everything else.
func runGitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitError{args: full, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// relinkGitFile rewrites the .git file of the worktree at dir so it names
// the repository's admin directory by a relative path. `worktree add`
// writes an absolute one, which resolves only where the workspace sits at
// the path it was created under: a worktree gummi adds inside a container
// that mounts the workspace at /project cannot be opened from the host,
// where that path does not exist, so nothing can be committed in it with
// the host's identity or signing key. A relative link resolves from both.
//
// `worktree add --relative-paths` would do this too, but it also sets
// extensions.relativeWorktrees in the repository's config, and git before
// 2.46 refuses to open a repository carrying an extension it does not
// know — every checkout of it, not just this one. A relative .git file on
// its own needs no extension: git has always resolved it against the
// file's directory.
//
// The admin directory's back-pointer is left absolute, as git wrote it.
func relinkGitFile(dir string) error {
	file := filepath.Join(dir, ".git")
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || !filepath.IsAbs(target) {
		return nil
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte("gitdir: "+rel+"\n"), 0o600)
}
