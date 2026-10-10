package worktree

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A repository's remotes, and the branch-to-remote wiring that hangs off
// them. Every write here is to the repository's own .git/config — which
// remotes it knows, and which remote branch a local one tracks — and none
// of them talks to a remote or moves a commit.

// ErrRemoteName refuses a remote name git would not take as one, or
// would read as an option.
var ErrRemoteName = errors.New("not a usable remote name")

// ErrRemoteURL refuses a remote URL that is empty, reads as an option, or
// names a transport helper (`ext::…`) that runs a command to connect.
var ErrRemoteURL = errors.New("not a usable remote URL")

// ErrRemoteExists refuses adding, or renaming onto, a name already taken.
var ErrRemoteExists = errors.New("a remote with that name already exists")

// ErrRemoteUnknown refuses a write to a remote the repository lacks.
var ErrRemoteUnknown = errors.New("no such remote")

// RemoteInfo is one configured remote.
type RemoteInfo struct {
	Name string
	// URL is where it fetches from; PushURL is where it pushes to, set
	// only when that differs. Both are as configured — a URL may carry a
	// credential, which MaskRemoteURL hides before one is shown.
	URL, PushURL string
	// Tracking counts the local branches whose upstream is on it.
	Tracking int
}

var remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

func checkRemoteName(name string) error {
	if !remoteNameRe.MatchString(name) || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("%q: %w", name, ErrRemoteName)
	}
	return nil
}

func checkRemoteURL(u string) error {
	switch {
	case u == "", strings.HasPrefix(u, "-"), strings.ContainsAny(u, "\n\r\x00"):
		return ErrRemoteURL
	case strings.Contains(u, "::"):
		// <transport>::<address> hands the address to git-remote-<transport>;
		// ext:: runs it as a command
		return fmt.Errorf("%w: transport helpers are not accepted", ErrRemoteURL)
	}
	return nil
}

// MaskRemoteURL hides the credential a remote URL carries. An http(s)
// URL loses its whole userinfo — a bare user there is as often a token —
// and any other scheme keeps the user and loses the password. secret
// reports that something was hidden, so a caller knows the masked text is
// not the URL.
func MaskRemoteURL(raw string) (masked string, secret bool) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw, false
	}
	authority, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return raw, false
	}
	user, _, hasPass := strings.Cut(authority[:at], ":")
	switch {
	case scheme == "http" || scheme == "https":
		user = ""
	case hasPass:
		user += ":"
	default:
		return raw, false
	}
	return scheme + "://" + user + "•••" + authority[at:] + path, true
}

// redactURL keeps a URL's credential out of a failed command's error,
// which quotes its arguments.
func redactURL(err error, url string) error {
	if err == nil {
		return nil
	}
	masked, secret := MaskRemoteURL(url)
	if !secret {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), url, masked))
}

// RedactRemotes hides, in text git printed, the credential of any
// configured remote URL it quotes.
func (m *Manager) RedactRemotes(ctx context.Context, text string) string {
	for _, r := range m.RemoteList(ctx) {
		for _, u := range []string{r.URL, r.PushURL} {
			if masked, secret := MaskRemoteURL(u); secret {
				text = strings.ReplaceAll(text, u, masked)
			}
		}
	}
	return text
}

// RemoteList reads the repository's remotes: origin first, the rest by
// name. It never touches the network.
func (m *Manager) RemoteList(ctx context.Context) []RemoteInfo {
	names := m.Remotes(ctx)
	if len(names) == 0 {
		return nil
	}
	sort.SliceStable(names, func(i, j int) bool { return names[i] == "origin" && names[j] != "origin" })
	tracking := map[string]int{}
	if out, err := runGit(ctx, m.repo, "for-each-ref", "--format=%(upstream:remotename)", "refs/heads"); err == nil {
		for _, r := range strings.Fields(out) {
			tracking[r]++
		}
	}
	remotes := make([]RemoteInfo, 0, len(names))
	for _, name := range names {
		r := RemoteInfo{Name: name, Tracking: tracking[name]}
		r.URL, _ = runGit(ctx, m.repo, "remote", "get-url", "--", name)
		if push, err := runGit(ctx, m.repo, "remote", "get-url", "--push", "--", name); err == nil && push != r.URL {
			r.PushURL = push
		}
		remotes = append(remotes, r)
	}
	return remotes
}

// RemoteBranches lists the remote-tracking branches as last fetched
// ("origin/main"), by name: what a local branch may be set to track.
func (m *Manager) RemoteBranches(ctx context.Context) []string {
	out, err := runGit(ctx, m.repo, "for-each-ref", "--format=%(refname:short)%00%(symref)", "refs/remotes")
	if err != nil {
		return nil
	}
	var names []string
	for line := range strings.Lines(out) {
		name, symref, _ := strings.Cut(strings.TrimRight(line, "\n"), "\x00")
		// origin/HEAD points at a branch; it is not one
		if name == "" || symref != "" || !strings.Contains(name, "/") {
			continue
		}
		names = append(names, name)
	}
	return names
}

func (m *Manager) hasRemote(ctx context.Context, name string) bool {
	for _, r := range m.Remotes(ctx) {
		if r == name {
			return true
		}
	}
	return false
}

// knownRemote checks name is a remote this repository has.
func (m *Manager) knownRemote(ctx context.Context, name string) error {
	if err := checkRemoteName(name); err != nil {
		return err
	}
	if !m.hasRemote(ctx, name) {
		return fmt.Errorf("%s: %w", name, ErrRemoteUnknown)
	}
	return nil
}

// AddRemote records a new remote. Nothing is fetched from it.
func (m *Manager) AddRemote(ctx context.Context, name, url string) error {
	if err := checkRemoteName(name); err != nil {
		return err
	}
	if err := checkRemoteURL(url); err != nil {
		return err
	}
	if m.hasRemote(ctx, name) {
		return fmt.Errorf("%s: %w", name, ErrRemoteExists)
	}
	_, err := runGit(ctx, m.repo, "remote", "add", "--", name, url)
	return redactURL(err, url)
}

// RenameRemote renames a remote; git moves its tracking refs and the
// upstream of every branch on it along.
func (m *Manager) RenameRemote(ctx context.Context, name, to string) error {
	if err := m.knownRemote(ctx, name); err != nil {
		return err
	}
	if err := checkRemoteName(to); err != nil {
		return err
	}
	if m.hasRemote(ctx, to) {
		return fmt.Errorf("%s: %w", to, ErrRemoteExists)
	}
	_, err := runGit(ctx, m.repo, "remote", "rename", "--", name, to)
	return err
}

// SetRemoteURL points a remote somewhere else. A separate push URL, when
// one is configured, is left as it is.
func (m *Manager) SetRemoteURL(ctx context.Context, name, url string) error {
	if err := m.knownRemote(ctx, name); err != nil {
		return err
	}
	if err := checkRemoteURL(url); err != nil {
		return err
	}
	_, err := runGit(ctx, m.repo, "remote", "set-url", "--", name, url)
	return redactURL(err, url)
}

// RemoveRemote forgets a remote: its tracking refs go, and every branch
// that tracked one of them tracks nothing afterwards. No local branch and
// no commit a local branch reaches is lost.
func (m *Manager) RemoveRemote(ctx context.Context, name string) error {
	if err := m.knownRemote(ctx, name); err != nil {
		return err
	}
	_, err := runGit(ctx, m.repo, "remote", "remove", "--", name)
	return err
}

// FetchRemote is Fetch for one remote.
func (m *Manager) FetchRemote(ctx context.Context, name string) error {
	if err := m.knownRemote(ctx, name); err != nil {
		return err
	}
	_, err := runGitEnv(ctx, m.repo, []string{"GIT_TERMINAL_PROMPT=0"}, "fetch", "--prune", "--", name)
	return err
}

// SetUpstream makes a local branch track a remote-tracking branch
// ("origin/topic"), or nothing when upstream is empty. It changes what
// the branch is compared with and where a plain push would go; the
// branch itself does not move.
func (m *Manager) SetUpstream(ctx context.Context, branch, upstream string) error {
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("no branch %s in %s", branch, m.repo)
	}
	if upstream == "" {
		if _, err := runGit(ctx, m.repo, "config", "--get", "branch."+branch+".merge"); err != nil {
			return nil
		}
		_, err := runGit(ctx, m.repo, "branch", "--unset-upstream", "--", branch)
		return err
	}
	// a remote-tracking branch only: `--set-upstream-to` would take a
	// local branch too, and "tracks main" is not what this offers
	if _, err := runGit(ctx, m.repo, "rev-parse", "--verify", "--quiet", "refs/remotes/"+upstream); err != nil {
		return fmt.Errorf("%s: %w", upstream, ErrNoUpstream)
	}
	_, err := runGit(ctx, m.repo, "branch", "--set-upstream-to=refs/remotes/"+upstream, "--", branch)
	return err
}
