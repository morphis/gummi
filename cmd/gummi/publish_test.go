package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/state"
)

var publishStore *state.Store

const publishGHShim = `#!/bin/sh
echo "$*" >> "$GH_FAKE/log"
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") echo '{"nameWithOwner":"me/widget","viewerPermission":"WRITE","isFork":false}' ;;
"pr list") echo '[]' ;;
"pr create") cat > "$GH_FAKE/body"; echo "https://github.com/me/widget/pull/7" ;;
*) exit 0 ;;
esac
`

// publishFixture is prFixture with the card started and verified on a
// branch one commit ahead of main, an origin naming a github.com repository
// that a local bare repository stands in for, and a fake gh.
func publishFixture(t *testing.T) (bare, fake string) {
	t.Helper()
	store := prFixture(t)
	publishStore = store
	root, _ := os.Getwd()
	fake = t.TempDir()
	bare = filepath.Join(fake, "remote.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(fake, "init", "-q", "--bare", bare)
	git(root, "remote", "add", "origin", "git@github.com:me/widget.git")
	git(root, "config", "url."+bare+".insteadOf", "git@github.com:me/widget.git")
	f := domain.Feature{
		ID: "FD-002", Num: 2, Kind: domain.KindFeature, Title: "Add a thing", Slug: "add-a-thing",
		Stage: domain.StageVerify, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	tree := filepath.Join(root, ".gummi", "worktrees", "FD-002")
	git(root, "worktree", "add", "-q", "-b", f.BranchName(), tree)
	git(tree, "commit", "-q", "--allow-empty", "-m", "feat: add a thing")
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerifiedAt(context.Background(), f.ID, time.Now(), git(tree, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(fake, "gh")
	if err := os.WriteFile(gh, []byte(publishGHShim), 0o700); err != nil {
		t.Fatal(err)
	}
	setFakeGHEnv(t, gh)
	t.Setenv("GH_FAKE", fake)
	// no person answers a prompt in a test, whatever stdin the run has
	wasTTY := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = wasTTY })
	publish.RewriteAllowed = func(string) bool { return true }
	t.Cleanup(func() { publish.RewriteAllowed = func(string) bool { return false } })
	return bare, fake
}

// With no terminal to confirm at, a publish verb prints the facts and runs
// nothing; the fingerprint of those facts is what --yes takes, and then the
// branch is pushed, the PR opened, and linked to the card.
func TestPRCreateRunsOnlyOnTheConfirmedFacts(t *testing.T) {
	bare, fake := publishFixture(t)
	var err error
	out := captureStdout(t, func() { err = runCLI("pr", "create", "FD-002") })
	if err == nil || !strings.Contains(err.Error(), "confirmation-needed") {
		t.Fatalf("without a confirm: %v", err)
	}
	if !strings.Contains(out, "head        me:gummi/FD-002-add-a-thing") || !strings.Contains(out, "gh pr create --repo me/widget") {
		t.Fatalf("the facts were not shown:\n%s", out)
	}
	fp := regexp.MustCompile(`--yes=([0-9a-f]{12})`).FindStringSubmatch(err.Error())
	if fp == nil {
		t.Fatalf("no fingerprint in %q", err)
	}
	if err := runCLI("pr", "create", "FD-002", "--yes=000000000000"); err == nil || !strings.Contains(err.Error(), "facts-changed") {
		t.Fatalf("a wrong fingerprint: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); strings.Contains(string(b), "pr create") {
		t.Fatal("gh pr create ran before the facts were confirmed")
	}
	out = captureStdout(t, func() { err = runCLI("pr", "create", "FD-002", "--yes="+fp[1]) })
	if err != nil {
		t.Fatalf("confirmed create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "opened https://github.com/me/widget/pull/7 (ready)") {
		t.Fatalf("output:\n%s", out)
	}
	if o, err := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-002-add-a-thing").CombinedOutput(); err != nil {
		t.Fatalf("the branch was not pushed: %s", o)
	}
	f, _ := publishStore.GetFeature(context.Background(), "FD-002")
	if f.PullRequest.Number != 7 || f.PullRequest.Repo != "me/widget" {
		t.Fatalf("linked = %+v", f.PullRequest)
	}
}

// Inside a gummi session — anything gummi started, an agent's shell above
// all — the publish verbs refuse before they read anything.
func TestPublishVerbsRefuseInsideASession(t *testing.T) {
	publishFixture(t)
	underGummi = true
	t.Cleanup(func() { underGummi = false })
	for _, argv := range [][]string{{"push", "FD-002"}, {"pr", "create", "FD-002"}, {"pr", "ready", "FD-002"}} {
		if err := runCLI(argv...); err == nil || !strings.Contains(err.Error(), "agent-session") {
			t.Errorf("%v: %v, want agent-session", argv, err)
		}
	}
}

// The publish verbs are a person's: the bundle an agent is handed to drive
// gummi with must not teach them.
func TestTheSkillBundleDoesNotTeachPublishing(t *testing.T) {
	for _, f := range skillBundle() {
		for _, verb := range []string{"gummi push", "pr create", "pr update", "pr ready", "pr draft"} {
			if strings.Contains(f.body, verb) {
				t.Errorf("%s names %q", f.path, verb)
			}
		}
	}
}
