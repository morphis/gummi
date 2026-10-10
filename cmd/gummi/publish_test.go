package main

import (
	"context"
	"encoding/json"
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
test -f "$GH_FAKE/fail-$1-$2" && { cat "$GH_FAKE/fail-$1-$2" >&2; exit 1; }
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") echo '{"nameWithOwner":"me/widget","viewerPermission":"WRITE","isFork":false}' ;;
"pr list") echo '[]' ;;
"pr view") cat "$GH_FAKE/view.json" ;;
"pr edit") cat > "$GH_FAKE/edit-body" ;;
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

// --repo is a choice between the repositories the PR can open in, never a
// way to open one somewhere else.
func TestPRCreateRefusesARepoThePRCannotOpenIn(t *testing.T) {
	_, fake := publishFixture(t)
	err := runCLI("pr", "create", "FD-002", "--repo", "acme/elsewhere")
	if err == nil || !strings.Contains(err.Error(), "base-unchosen") || !strings.Contains(err.Error(), "opens in me/widget") {
		t.Fatalf("--repo acme/elsewhere: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); strings.Contains(string(b), "pr create") {
		t.Fatal("gh pr create ran")
	}
	var facts error
	out := captureStdout(t, func() { facts = runCLI("pr", "create", "FD-002", "--repo", "me/widget") })
	if facts == nil || !strings.Contains(facts.Error(), "confirmation-needed") || !strings.Contains(out, "gh pr create --repo me/widget") {
		t.Fatalf("--repo me/widget: %v\n%s", facts, out)
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

// confirmed runs a publish verb the way a script does: once to read the
// facts, then again with the fingerprint it was shown.
func confirmed(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runCLI(argv...) })
	if err == nil || !strings.Contains(err.Error(), "confirmation-needed") {
		return out, err
	}
	fp := regexp.MustCompile(`--yes=([0-9a-f]{12})`).FindStringSubmatch(err.Error())
	if fp == nil {
		t.Fatalf("no fingerprint in %q", err)
	}
	out = captureStdout(t, func() { err = runCLI(append(argv, "--yes="+fp[1])...) })
	return out, err
}

func publishEvents(t *testing.T) []state.PublishPayload {
	t.Helper()
	evs, err := publishStore.Events(context.Background(), "FD-002")
	if err != nil {
		t.Fatal(err)
	}
	var out []state.PublishPayload
	for _, ev := range evs {
		if ev.Kind == state.EventPublish {
			var p state.PublishPayload
			_ = json.Unmarshal([]byte(ev.Payload), &p)
			out = append(out, p)
		}
	}
	return out
}

// linkPublished links FD-002 to PR #7 as GitHub would have it at the
// branch's tip.
func linkPublished(t *testing.T, fake string, draft bool) string {
	t.Helper()
	root, _ := os.Getwd()
	tip, err := exec.Command("git", "-C", root, "rev-parse", "gummi/FD-002-add-a-thing").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(tip))
	ref := domain.PullRequestRef{Repo: "me/widget", Number: 7, URL: "https://github.com/me/widget/pull/7", HeadSHA: sha}
	if err := publishStore.SetPullRequest(context.Background(), "FD-002", ref); err != nil {
		t.Fatal(err)
	}
	d := "false"
	if draft {
		d = "true"
	}
	view := `{"number":7,"url":"https://github.com/me/widget/pull/7","state":"OPEN","isDraft":` + d + `,"headRefOid":"` + sha + `","headRefName":"gummi/FD-002-add-a-thing","headRepositoryOwner":{"login":"me"}}`
	if err := os.WriteFile(filepath.Join(fake, "view.json"), []byte(view), 0o600); err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestPushPublishesTheBranchAndSaysWhenThereIsNothingTo(t *testing.T) {
	bare, _ := publishFixture(t)
	// --json without --yes is the read: facts, plan and fingerprint, and
	// nothing run
	var err error
	out := captureStdout(t, func() { err = runCLI("push", "FD-002", "--json") })
	var read publishJSON
	if err != nil || json.Unmarshal([]byte(out), &read) != nil || read.Fingerprint == "" || read.Result != nil || len(read.Plan.Commands) != 1 {
		t.Fatalf("push --json = %v\n%s", err, out)
	}
	if o, _ := exec.Command("git", "--git-dir", bare, "branch", "--list").Output(); len(o) != 0 {
		t.Fatalf("a read pushed %q", o)
	}
	out = captureStdout(t, func() { err = runCLI("push", "FD-002", "--json", "--yes="+read.Fingerprint) })
	var ran publishJSON
	if err != nil || json.Unmarshal([]byte(out), &ran) != nil || ran.Result == nil || ran.Result.Pushed != read.Facts.Tip {
		t.Fatalf("confirmed push --json = %v\n%s", err, out)
	}
	if o, _ := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-002-add-a-thing").Output(); strings.TrimSpace(string(o)) != read.Facts.Tip {
		t.Fatalf("the remote has %q, want %s", o, read.Facts.Tip)
	}
	if evs := publishEvents(t); len(evs) != 1 || evs[0].Act != "push" || evs[0].Pushed != read.Facts.Tip {
		t.Fatalf("the thread records %+v", evs)
	}
	if _, err := confirmed(t, "push", "FD-002"); err == nil || !strings.Contains(err.Error(), "nothing-to-publish") {
		t.Fatalf("a second push: %v", err)
	}
	for _, argv := range [][]string{{"pr", "update", "FD-002"}, {"pr", "ready", "FD-002"}, {"pr", "draft", "FD-002"}} {
		if _, err := confirmed(t, argv...); err == nil || !strings.Contains(err.Error(), "refused (no-pr)") {
			t.Errorf("%v with no PR linked: %v", argv, err)
		}
	}
	if err := runCLI("push", "FD-404"); err == nil {
		t.Fatal("a push of a card that does not exist ran")
	}
}

func TestPRReadyDraftAndUpdateActOnTheLinkedPR(t *testing.T) {
	_, fake := publishFixture(t)
	if _, err := confirmed(t, "push", "FD-002"); err != nil {
		t.Fatal(err)
	}
	linkPublished(t, fake, true)
	ghLog := func() string { b, _ := os.ReadFile(filepath.Join(fake, "log")); return string(b) }

	var err error
	out := captureStdout(t, func() { err = runCLI("pr", "ready", "FD-002") })
	if err == nil || !strings.Contains(out, "#7 open, draft") || !strings.Contains(out, "gh pr ready 7 --repo me/widget") {
		t.Fatalf("the facts of a ready: %v\n%s", err, out)
	}
	if strings.Contains(ghLog(), "pr ready") {
		t.Fatal("gh pr ready ran before the confirm")
	}
	out, err = confirmed(t, "pr", "ready", "FD-002")
	if err != nil || !strings.Contains(out, "https://github.com/me/widget/pull/7 is ready for review") || !strings.HasSuffix(strings.TrimSpace(ghLog()), "pr ready 7 --repo me/widget") {
		t.Fatalf("pr ready: %v\n%s\n%s", err, out, ghLog())
	}

	linkPublished(t, fake, false)
	if _, err := confirmed(t, "pr", "ready", "FD-002"); err == nil || !strings.Contains(err.Error(), "already-ready") {
		t.Fatalf("ready twice: %v", err)
	}
	out, err = confirmed(t, "pr", "draft", "FD-002")
	if err != nil || !strings.Contains(out, "is a draft again") || !strings.HasSuffix(strings.TrimSpace(ghLog()), "pr ready 7 --repo me/widget --undo") {
		t.Fatalf("pr draft: %v\n%s", err, out)
	}

	// update: nothing new to push, so it needs a title or a body
	if _, err := confirmed(t, "pr", "update", "FD-002"); err == nil || !strings.Contains(err.Error(), "nothing-to-publish") {
		t.Fatalf("an update with nothing new: %v", err)
	}
	body := filepath.Join(fake, "body.md")
	if err := os.WriteFile(body, []byte("the why\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = confirmed(t, "pr", "update", "FD-002", "--title", "A better title", "--body-file", body)
	if err != nil || !strings.Contains(out, "updated https://github.com/me/widget/pull/7") {
		t.Fatalf("pr update: %v\n%s", err, out)
	}
	if !strings.Contains(ghLog(), "pr edit 7 --repo me/widget --title=A better title --body-file -") {
		t.Fatalf("gh was called as:\n%s", ghLog())
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "edit-body")); string(b) != "the why\n" {
		t.Fatalf("body sent = %q", b)
	}
	if _, err := confirmed(t, "pr", "update", "FD-002", "--body-file", filepath.Join(fake, "absent.md")); err == nil {
		t.Fatal("a body file that does not exist was accepted")
	}
	if evs := publishEvents(t); len(evs) != 4 || evs[1].Act != "ready" || evs[2].Act != "draft" || evs[3].Act != "update" || evs[3].Number != 7 {
		t.Fatalf("the thread records %+v", evs)
	}
}

// A body read from stdin leaves no terminal to confirm at, so it runs only
// with --yes; the description shown is the one that is sent.
func TestPRCreateReadsItsBodyFromStdin(t *testing.T) {
	_, fake := publishFixture(t)
	stdin := func(text string) {
		t.Helper()
		f := filepath.Join(fake, "stdin")
		if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		t.Cleanup(func() { os.Stdin = old; _ = in.Close() })
	}
	stdinIsTerminal = func() bool { return true }
	stdin("from a pipe\n")
	var err error
	out := captureStdout(t, func() { err = runCLI("pr", "create", "FD-002", "--draft", "--body-file", "-") })
	if err == nil || !strings.Contains(err.Error(), "confirmation-needed") || !strings.Contains(out, "from a pipe") || !strings.Contains(out, "draft — asked for") {
		t.Fatalf("a create with its body on stdin: %v\n%s", err, out)
	}
	fp := regexp.MustCompile(`--yes=([0-9a-f]{12})`).FindStringSubmatch(err.Error())
	stdin("from a pipe\n")
	out = captureStdout(t, func() { err = runCLI("pr", "create", "FD-002", "--draft", "--body-file", "-", "--yes="+fp[1]) })
	if err != nil || !strings.Contains(out, "(draft), linked to FD-002") {
		t.Fatalf("confirmed: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "body")); string(b) != "from a pipe\n" {
		t.Fatalf("body sent = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "log")); !strings.Contains(string(b), "--draft") {
		t.Fatalf("gh was called as:\n%s", b)
	}
}

func TestAPersonAtTheTerminalConfirmsWithY(t *testing.T) {
	bare, fake := publishFixture(t)
	stdinIsTerminal = func() bool { return true }
	answer := func(text string) {
		t.Helper()
		f := filepath.Join(fake, "stdin")
		if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = in
		t.Cleanup(func() { os.Stdin = old; _ = in.Close() })
	}
	questions := map[string]string{"push": "Push? [y/N]", "pr create": "Push and open the pull request? [y/N]"}
	for verb, q := range questions {
		answer("\n")
		var err error
		out := captureStdout(t, func() { err = runCLI(append(strings.Fields(verb), "FD-002")...) })
		if err == nil || !strings.Contains(err.Error(), "not confirmed") || !strings.Contains(out, q) {
			t.Fatalf("%s with enter alone: %v\n%s", verb, err, out)
		}
	}
	if o, _ := exec.Command("git", "--git-dir", bare, "branch", "--list").Output(); len(o) != 0 {
		t.Fatalf("an unconfirmed act pushed %q", o)
	}
	answer("Y\n")
	var err error
	out := captureStdout(t, func() { err = runCLI("push", "FD-002") })
	if err != nil || !strings.Contains(out, "✓ pushed") {
		t.Fatalf("push answered y: %v\n%s", err, out)
	}
	linkPublished(t, fake, true)
	for verb, q := range map[string]string{"pr ready": "Mark it ready for review?", "pr update": "Update the pull request?"} {
		answer("no\n")
		argv := append(strings.Fields(verb), "FD-002")
		if verb == "pr update" {
			argv = append(argv, "--title", "x")
		}
		out := captureStdout(t, func() { err = runCLI(argv...) })
		if err == nil || !strings.Contains(out, q) {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
	}
	linkPublished(t, fake, false)
	answer("n\n")
	if out := captureStdout(t, func() { err = runCLI("pr", "draft", "FD-002") }); !strings.Contains(out, "Turn it back into a draft?") {
		t.Fatalf("pr draft asks:\n%s", out)
	}
}

// --remote steers where the facts say the push goes; the choice is kept as
// the branch's pushRemote only once the act it was made for has run.
func TestPushRemoteIsRememberedOnlyOnceTheActRan(t *testing.T) {
	_, fake := publishFixture(t)
	root, _ := os.Getwd()
	git := func(args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	forkBare := filepath.Join(fake, "fork.git")
	git("init", "-q", "--bare", forkBare)
	git("remote", "add", "fork", "git@github.com:me/fork.git")
	git("config", "url."+forkBare+".insteadOf", "git@github.com:me/fork.git")
	const key = "branch.gummi/FD-002-add-a-thing.pushRemote"

	if err := runCLI("push", "FD-002", "--remote", "nowhere"); err == nil || !strings.Contains(err.Error(), "not a configured remote") {
		t.Fatalf("a remote that does not exist: %v", err)
	}
	var err error
	out := captureStdout(t, func() { err = runCLI("push", "FD-002", "--remote", "fork") })
	if err == nil || !strings.Contains(out, "me:gummi/FD-002-add-a-thing") || !strings.Contains(out, "from pushRemote") || !strings.Contains(out, "fork → ") {
		t.Fatalf("the facts of a push to the fork: %v\n%s", err, out)
	}
	if got := git("config", "--get", key); got != "" {
		t.Fatalf("an unconfirmed --remote was kept: %q", got)
	}
	// a choice that was already there is put back, not unset
	git("config", key, "origin")
	captureStdout(t, func() { err = runCLI("push", "FD-002", "--remote", "fork") })
	if got := git("config", "--get", key); got != "origin" {
		t.Fatalf("the earlier pushRemote = %q after an unconfirmed --remote, want origin", got)
	}
	git("config", "--unset", key)

	if out, err := confirmed(t, "push", "FD-002", "--remote", "fork"); err != nil {
		t.Fatalf("confirmed push to the fork: %v\n%s", err, out)
	}
	if got := git("--git-dir", forkBare, "rev-parse", "gummi/FD-002-add-a-thing"); got != git("rev-parse", "gummi/FD-002-add-a-thing") {
		t.Fatalf("the fork has %q", got)
	}
	if got := git("config", "--get", key); got != "fork" {
		t.Fatalf("pushRemote = %q after the push, want fork", got)
	}
}

func TestPublishRefusesACardAnAgentHolds(t *testing.T) {
	publishFixture(t)
	root, _ := os.Getwd()
	ws, err := state.Open(root, root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := state.AcquireLock(ws.CardLockFile("FD-002"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := runCLI("push", "FD-002"); err == nil || !strings.Contains(err.Error(), "refused (agent-busy)") {
		t.Fatalf("a push of a held card: %v", err)
	}
}

// What reached GitHub before an act failed is said and recorded, and the
// card's refusals read the same here as on the board.
func TestAPublishThatFailsPartWayIsRecordedAsWhatItDid(t *testing.T) {
	bare, fake := publishFixture(t)
	if err := os.WriteFile(filepath.Join(fake, "fail-pr-create"), []byte("HTTP 502"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := confirmed(t, "pr", "create", "FD-002")
	if err == nil || !strings.Contains(err.Error(), "was pushed to me/widget") {
		t.Fatalf("a create whose gh call failed: %v", err)
	}
	if o, err := exec.Command("git", "--git-dir", bare, "rev-parse", "gummi/FD-002-add-a-thing").CombinedOutput(); err != nil {
		t.Fatalf("the branch was not pushed: %s", o)
	}
	evs := publishEvents(t)
	if len(evs) != 1 || evs[0].Act != "push" || evs[0].Number != 0 || evs[0].Pushed == "" {
		t.Fatalf("the thread records %+v, want the push alone", evs)
	}
	f, _ := publishStore.GetFeature(context.Background(), "FD-002")
	if !f.PullRequest.Empty() {
		t.Fatalf("a PR that was not opened is linked: %+v", f.PullRequest)
	}

	root, _ := os.Getwd()
	tree := filepath.Join(root, ".gummi", "worktrees", "FD-002")
	if err := os.WriteFile(filepath.Join(tree, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCLI("push", "FD-002"); err == nil || !strings.Contains(err.Error(), "refused (dirty)") {
		t.Fatalf("a push over uncommitted work: %v", err)
	}
}

// Every command marks what it starts: the marker is in this process's
// environment before any backend or shell is spawned from it.
func TestEveryRunMarksItsChildren(t *testing.T) {
	t.Setenv(spawnedMarker, "")
	_ = runCLI("version")
	if os.Getenv(spawnedMarker) == "" {
		t.Fatal("a gummi run leaves its children without the session marker")
	}
	if spawnedMarker != "GUMMI_SPAWNED" {
		t.Fatal("the marker's name changed: internal/agent's test of who inherits it names it too")
	}
}
