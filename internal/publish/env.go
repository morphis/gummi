package publish

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds every git and gh call an act makes: a push that hangs on
// a network is a failure the person is told about, not a frozen face.
const Timeout = 2 * time.Minute

// Env runs the git and gh commands publishing needs. Dir is the card's
// worktree, where the git calls run (so a worktree's own config and hooks
// are the ones read); GH is the gh binary, "" for "gh" on the path.
type Env struct {
	Dir string
	GH  string
}

func (e Env) ghBin() string {
	if e.GH != "" {
		return e.GH
	}
	return "gh"
}

// GHPath is the gh binary an act would run, resolved to an absolute path,
// because GUMMI_GH_CMD or the path can replace the binary that carries the
// credential and the person is shown which one it is.
func (e Env) GHPath() (string, error) {
	p, err := exec.LookPath(e.ghBin())
	if err != nil {
		return "", err
	}
	if abs, aerr := filepath.Abs(p); aerr == nil {
		p = abs
	}
	return p, nil
}

// scrubbed is the environment every publish command runs in: the person's
// own, with every prompt disabled (there is no terminal to answer one) and
// the variables that would silently redirect gh to another repository or
// host removed — every gh call names its repository instead.
func scrubbed() []string {
	drop := map[string]bool{"GH_REPO": true, "GH_HOST": true, "GIT_ASKPASS": true, "SSH_ASKPASS": true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"SSH_ASKPASS_REQUIRE=never",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"NO_COLOR=1",
	)
}

// run runs name with args in dir: stdin from in (nil is empty), no
// controlling terminal, and the whole process group killed on timeout, so
// an ssh asking for a passphrase fails instead of waiting on /dev/tty.
func run(ctx context.Context, dir string, in []byte, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = scrubbed()
	cmd.Stdin = bytes.NewReader(in)
	detach(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), fail(CodeTimeout, name+" did not answer within "+Timeout.String(), "try again")
	}
	return out.String(), errb.String(), err
}

func (e Env) git(ctx context.Context, args ...string) (string, error) {
	out, stderr, err := run(ctx, e.Dir, nil, "git", args...)
	if err != nil {
		if pe := AsError(err); pe.Code == CodeTimeout {
			return "", pe
		}
		return "", errors.New("git " + args[0] + ": " + firstLine(stderr, err))
	}
	return strings.TrimSpace(out), nil
}

func (e Env) gitOK(ctx context.Context, args ...string) bool {
	_, err := e.git(ctx, args...)
	return err == nil
}

func (e Env) gh(ctx context.Context, in []byte, args ...string) (string, error) {
	out, stderr, err := run(ctx, e.Dir, in, e.ghBin(), args...)
	if err != nil {
		if pe := AsError(err); pe.Code == CodeTimeout {
			return "", pe
		}
		return "", ghError(args, stderr, err)
	}
	return strings.TrimSpace(out), nil
}

func firstLine(stderr string, err error) string {
	s := strings.TrimSpace(stderr)
	if s == "" {
		return err.Error()
	}
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// ghError types what gh said, so "not signed in" and "SSO" read the same on
// every face.
func ghError(args []string, stderr string, err error) *Error {
	s := strings.ToLower(stderr)
	what := "gh " + strings.Join(args[:min(2, len(args))], " ")
	switch {
	case strings.Contains(s, "saml") || strings.Contains(s, "single sign-on"):
		return fail(CodeAuthFailed, what+": the organization requires SSO for this token", "run `gh auth refresh`")
	case strings.Contains(s, "gh auth login") || strings.Contains(s, "not logged in") || strings.Contains(s, "authentication required") || strings.Contains(s, "bad credentials"):
		return fail(CodeGHNotSignedIn, what+": gh is not signed in", "run `gh auth login`")
	}
	return fail(CodeFailed, what+": "+firstLine(stderr, err), "")
}

// pushError types git push's refusal from what it printed: with --porcelain
// the per-ref reason is on stdout and the rest on stderr, so said is both.
// hooked is whether a pre-push hook ran: git names no cause when one says
// no, so a push that failed with no ref rejected and nothing fatal is the
// hook's refusal.
func pushError(said string, err error, hooked bool) *Error {
	if pe := AsError(err); pe.Code == CodeTimeout {
		return pe
	}
	s := strings.ToLower(said)
	line := firstLine(said, err)
	for l := range strings.SplitSeq(said, "\n") {
		// the line that names the refusal, where there is one
		if ll := strings.ToLower(l); strings.Contains(ll, "rejected") || strings.Contains(ll, "error:") || strings.Contains(ll, "fatal:") {
			line = strings.TrimSpace(l)
			break
		}
	}
	switch {
	case strings.Contains(s, "stale info"):
		return fail(CodeLeaseStale, "the remote branch moved since its tip was shown; nothing was overwritten", "review again")
	case strings.Contains(s, "gh006") || strings.Contains(s, "protected branch"):
		return fail(CodeProtected, "GitHub refused the push: the branch is protected", "")
	case hooked && strings.Contains(s, "failed to push some refs") && !strings.Contains(s, "rejected") && !strings.Contains(s, "fatal:"):
		return fail(CodeHookRejected, "a local hook refused the push: "+firstLine(said, err), "read what it printed, or push it yourself")
	case strings.Contains(s, "remote rejected"):
		// the server said no (a push rule, a pre-receive hook): not the
		// remote being ahead, and nothing a fetch would fix
		return fail(CodeFailed, "the remote rejected the push: "+line, "")
	case strings.Contains(s, "non-fast-forward") || strings.Contains(s, "fetch first"):
		return fail(CodeRemoteAhead, "the remote branch has commits this card does not; nothing was overwritten", "fetch them into the card first")
	case strings.Contains(s, "terminal prompts disabled") || strings.Contains(s, "passphrase") ||
		strings.Contains(s, "host key verification") || strings.Contains(s, "sign_and_send_pubkey") ||
		strings.Contains(s, "could not read username") || strings.Contains(s, "could not read password"):
		return fail(CodeNeedsInteraction, "the credential wanted a person to answer: "+line, "add your key to ssh-agent (ssh-add) or set up a credential helper, then try again")
	case strings.Contains(s, "permission denied") || strings.Contains(s, "authentication failed") || strings.Contains(s, "403"):
		return fail(CodeAuthFailed, "the remote refused the credential: "+line, "")
	}
	return fail(CodeFailed, "git push: "+line, "")
}
