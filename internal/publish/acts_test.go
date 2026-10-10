package publish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

func TestPushErrorNamesWhatGitSaid(t *testing.T) {
	exit := errors.New("exit status 1")
	for said, want := range map[string]Code{
		"! [rejected] t:refs/heads/x (stale info)\nerror: failed to push some refs to 'origin'":                          CodeLeaseStale,
		"remote: error: GH006: Protected branch update failed for refs/heads/x.\n! [remote rejected] x (protected)":      CodeProtected,
		"! [remote rejected] t:refs/heads/x (pre-receive hook declined)\nerror: failed to push some refs":                CodeFailed,
		"! [rejected] t:refs/heads/x (non-fast-forward)":                                                                 CodeRemoteAhead,
		"! [rejected] t:refs/heads/x (fetch first)":                                                                      CodeRemoteAhead,
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled":                             CodeNeedsInteraction,
		"Host key verification failed.\nfatal: Could not read from remote repository.":                                   CodeNeedsInteraction,
		"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.":                  CodeAuthFailed,
		"remote: Permission to acme/w.git denied to me.\nfatal: unable to access: The requested URL returned error: 403": CodeAuthFailed,
		"fatal: unable to access 'https://github.com/': Could not resolve host":                                          CodeFailed,
		"fatal: '/tmp/Test403/gh/absent.git' does not appear to be a git repository":                                     CodeFailed,
		"": CodeFailed,
	} {
		if got := pushError(said, exit, false); got.Code != want {
			t.Errorf("pushError(%q) = %s (%s), want %s", said, got.Code, got.Text, want)
		}
	}
	// the line that names the refusal is the one a person reads
	if e := pushError("To github.com:me/w.git\n! [remote rejected] x (push declined due to repository rule violations)\nDone", exit, true); !strings.Contains(e.Text, "repository rule violations") {
		t.Errorf("text = %q, want the rejecting line", e.Text)
	}
	// a server's refusal is not the remote being ahead: no fetch fixes it
	if e := pushError("! [remote rejected] x (non-fast-forward policy)", exit, true); e.Code != CodeFailed || e.Fix != "" {
		t.Errorf("a remote rejection = %+v", e)
	}
	// git names no cause when a pre-push hook says no: what the hook printed
	// is all there is, and only where a hook ran
	hookSaid := "lint failed\nerror: failed to push some refs to '/srv/hooks/w.git'"
	if e := pushError(hookSaid, exit, true); e.Code != CodeHookRejected || !strings.Contains(e.Text, "lint failed") {
		t.Errorf("a pre-push hook's refusal = %+v", e)
	}
	if e := pushError(hookSaid, exit, false); e.Code != CodeFailed {
		t.Errorf("a failed push with no hook = %s, want failed", e.Code)
	}
	if e := pushError("! [rejected] t:refs/heads/x (fetch first)\nerror: failed to push some refs", exit, true); e.Code != CodeRemoteAhead {
		t.Errorf("a ref rejected past a hook that passed = %s, want remote-ahead", e.Code)
	}
	if e := pushError("", fail(CodeTimeout, "git did not answer", ""), false); e.Code != CodeTimeout {
		t.Errorf("a timeout = %s", e.Code)
	}
}

func TestGHErrorNamesSignInAndSSO(t *testing.T) {
	exit := errors.New("exit status 1")
	for said, want := range map[string]Code{
		"GraphQL: Resource protected by organization SAML enforcement":                                              CodeAuthFailed,
		"To get started with GitHub CLI, please run:  gh auth login":                                                CodeGHNotSignedIn,
		"HTTP 401: Bad credentials":                                                                                 CodeGHNotSignedIn,
		"GraphQL: Could not resolve to a Repository":                                                                CodeFailed,
		"pull request create failed: GraphQL: Resource not accessible by personal access token (createPullRequest)": CodeTokenRefused,
	} {
		if got := ghError([]string{"pr", "view", "7"}, said, exit); got.Code != want || !strings.HasPrefix(got.Text, "gh pr view: ") {
			t.Errorf("ghError(%q) = %s %q, want %s", said, got.Code, got.Text, want)
		}
	}
}

// Every place a push could go that the person did not mean, or that gummi
// cannot name, is a typed refusal before anything is read on the network.
func TestResolveRefusesAPushTargetItCannotStandBehind(t *testing.T) {
	adopt := func(w *world) { w.f.BranchScheme, w.f.Branch = domain.BranchSchemeAdopted, "feat/rate-limit" }
	cases := []struct {
		name string
		mut  func(w *world)
		want Code
		says string
	}{
		{"no remote", func(w *world) { w.repo.terr = worktree.ErrNoRemote }, CodeNoRemote, "no remote"},
		{"several remotes", func(w *world) { w.repo.terr = worktree.ErrRemoteAmbiguous }, CodeRemoteAmbiguous, "gummi push --remote"},
		{"target unreadable", func(w *world) { w.repo.terr = errors.New("git config: boom") }, CodeFailed, "boom"},
		{"onto the base", func(w *world) { w.repo.target.Branch = "main" }, CodeOntoBase, "onto main"},
		{"another name", func(w *world) { w.repo.target.Branch = "other" }, CodeOntoBase, "another name than its own"},
		{"bad remote name", func(w *world) { adopt(w); w.repo.target.Branch = "bad..name" }, CodeFailed, "not valid"},
		{"remote without a URL", func(w *world) { w.repo.target.Remote = "nowhere" }, CodeNoRemote, "has no URL"},
		{"another host", func(w *world) { w.git("remote", "set-url", "origin", "git@git.corp.example:me/widget.git") }, CodeUnsupportedHost, "not a github.com repository"},
		{"fetch and push differ", func(w *world) { w.git("config", "remote.origin.pushurl", "git@github.com:other/widget.git") }, CodeRemoteAmbiguous, "fetches from me/widget and pushes to other/widget"},
		{"read only", func(w *world) {
			w.write("repo.json", `{"nameWithOwner":"me/widget","viewerPermission":"READ"}`)
		}, CodeNoWriteAccess, "permission read"},
		{"read only, adopted", func(w *world) {
			adopt(w)
			w.write("repo.json", `{"nameWithOwner":"me/widget","viewerPermission":"READ"}`)
		}, CodeForkNotYours, "cannot push to me/widget"},
		{"repo view garbled", func(w *world) { w.write("repo.json", `not json`) }, CodeFailed, "gh repo view"},
		{"no such branch", func(w *world) { w.f.Slug = "gone" }, CodeNoBranch, "does not exist"},
		{"branch name not a ref", func(w *world) { w.f.Slug = "a..b" }, CodeNoBranch, "not a valid branch"},
		{"nothing ahead", func(w *world) { w.git("branch", "-f", "feat/rate-limit", "main") }, CodeNothingToPublish, "no commits ahead of main"},
		{"gh missing", func(w *world) { w.env.GH = filepath.Join(w.fake, "absent") }, CodeGHMissing, "not on the path"},
		{"gh signed out", func(w *world) { w.write("fail-auth-status", "You are not logged into any GitHub hosts.") }, CodeGHNotSignedIn, "gh auth login"},
		{"remote unreachable", func(w *world) {
			w.git("config", "--unset-all", "url."+w.bare+".insteadOf")
			w.git("config", "url."+filepath.Join(w.fake, "absent.git")+".insteadOf", "git@github.com:me/widget.git")
		}, CodeFailed, "git push"},
		{"the card itself", func(w *world) { w.f.StackID = "S1" }, CodeStacked, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			tc.mut(w)
			_, e := Resolve(context.Background(), w.env, w.repo, w.in())
			if e == nil || e.Code != tc.want || !strings.Contains(e.Error(), tc.says) {
				t.Fatalf("refusal = %v, want %s saying %q", e, tc.want, tc.says)
			}
			if out := w.run(w.dir, "git", "--git-dir", w.bare, "branch", "--list"); out != "" {
				t.Fatalf("something was pushed: %q", out)
			}
		})
	}
}

// An adopted branch may live under another name on its remote, and is never
// rewritten there.
func TestAnAdoptedBranchPublishesUnderItsUpstreamNameAndIsNeverForced(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.f.BranchScheme, w.f.Branch = domain.BranchSchemeAdopted, "feat/rate-limit"
	w.repo.target.Branch = "their-name"
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil || fx.HeadRef() != "me:their-name" || fx.Push != PushNew || !fx.Adopted {
		t.Fatalf("facts = %+v %v", fx, e)
	}
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	if got := w.run(w.dir, "git", "--git-dir", w.bare, "rev-parse", "their-name"); got != fx.Tip {
		t.Fatalf("remote their-name = %s, want %s", got, fx.Tip)
	}
	w.git("checkout", "-q", "feat/rate-limit")
	w.git("commit", "-q", "--amend", "--allow-empty", "-m", "reworded")
	w.git("checkout", "-q", "main")
	w.f.VerifiedRev = w.git("rev-parse", "feat/rate-limit")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeRemoteAhead || !strings.Contains(e.Text, "never rewrites an adopted branch") {
		t.Fatalf("a rewritten adopted branch: %v, want remote-ahead and no force", e)
	}
}

func TestPlanRefusesAnActThePRCannotTake(t *testing.T) {
	base := Facts{
		Branch: "feat/x", Tip: "t2", Base: "main", Remote: "origin", RemoteBranch: "feat/x", HeadRepo: "me/w", BaseRepo: "acme/w",
		Push: PushFastForward, Ahead: 2,
	}
	pr := func(state string, draft bool) *PR {
		return &PR{Repo: "acme/w", Number: 5, State: state, Draft: draft, HeadSHA: "t2", HeadOwner: "me", HeadBranch: "feat/x"}
	}
	cases := []struct {
		name string
		mut  func(*Facts)
		req  Request
		want Code
	}{
		{"update without a PR", nil, Request{Act: ActUpdate}, CodeNoPR},
		{"ready without a PR", nil, Request{Act: ActReady}, CodeNoPR},
		{"draft without a PR", nil, Request{Act: ActDraft}, CodeNoPR},
		{"push to a closed PR", func(fx *Facts) { fx.PR = pr("CLOSED", false) }, Request{Act: ActPush}, CodePRClosed},
		{"update a closed PR", func(fx *Facts) { fx.PR = pr("CLOSED", false) }, Request{Act: ActUpdate, Title: "t"}, CodePRClosed},
		{"create over a closed PR", func(fx *Facts) { fx.PR = pr("CLOSED", false) }, Request{Act: ActCreate, Title: "t"}, CodePRClosed},
		{"create when linked", func(fx *Facts) { fx.PR = pr("OPEN", true) }, Request{Act: ActCreate, Title: "t"}, CodePRExists},
		{"create without a title", nil, Request{Act: ActCreate, Title: "  "}, CodeConfirmationNeeded},
		{"push under an unlinked PR", func(fx *Facts) { fx.OpenPR = 9 }, Request{Act: ActPush}, CodePRExists},
		{"push with nothing new", func(fx *Facts) { fx.Push = PushUpToDate }, Request{Act: ActPush}, CodeNothingToPublish},
		{"update with nothing new", func(fx *Facts) { fx.Push, fx.PR = PushUpToDate, pr("OPEN", true) }, Request{Act: ActUpdate}, CodeNothingToPublish},
		{"update a ready PR, unverified", func(fx *Facts) { fx.PR, fx.ReadyWhy = pr("OPEN", false), "not verified" }, Request{Act: ActUpdate}, CodeNotVerified},
		{"ready when ready", func(fx *Facts) { fx.PR = pr("OPEN", false) }, Request{Act: ActReady}, CodeAlreadyReady},
		{"draft when a draft", func(fx *Facts) { fx.PR = pr("OPEN", true) }, Request{Act: ActDraft}, CodeAlreadyDraft},
		{"an act nobody defined", nil, Request{Act: "merge"}, CodeFailed},
	}
	for _, tc := range cases {
		fx := base
		if tc.mut != nil {
			tc.mut(&fx)
		}
		p, e := PlanFor(fx, tc.req)
		if e == nil || e.Code != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, e, tc.want)
		}
		if len(p.Commands) != 0 {
			t.Errorf("%s: a refused plan carries commands %v", tc.name, p.Commands)
		}
	}
	if _, e := PlanFor(func() Facts { fx := base; fx.OpenPR = 9; return fx }(), Request{Act: ActPush}); e == nil || e.PR != 9 {
		t.Errorf("the unlinked PR is not named for linking: %+v", e)
	}
}

func TestPlanShowsExactlyTheCommandsAnActRuns(t *testing.T) {
	fx := Facts{
		Branch: "feat/x", Tip: "t2", Base: "main", Remote: "origin", RemoteBranch: "feat/x", HeadRepo: "me/w", BaseRepo: "acme/w",
		Push: PushUpToDate, Ahead: 2,
		PR: &PR{Repo: "acme/w", Number: 5, State: "OPEN", HeadSHA: "t2", HeadOwner: "me", HeadBranch: "feat/x"},
	}
	want := func(req Request, cmds ...string) {
		t.Helper()
		p, e := PlanFor(fx, req)
		if e != nil || strings.Join(p.Commands, "\n") != strings.Join(cmds, "\n") {
			t.Errorf("%s: commands %q %v, want %q", req.Act, p.Commands, e, cmds)
		}
	}
	want(Request{Act: ActUpdate, Title: "new"}, "gh pr edit 5 --repo acme/w --title=<title>")
	want(Request{Act: ActUpdate, Body: "why"}, "gh pr edit 5 --repo acme/w --body-file -")
	want(Request{Act: ActDraft}, "gh pr ready 5 --repo acme/w --undo")
	fx.PR.Draft = true
	want(Request{Act: ActReady}, "gh pr ready 5 --repo acme/w")
	fx.Push, fx.RemoteTip = PushLease, "t1"
	want(Request{Act: ActUpdate, Title: "new", Body: "why"},
		"git push --porcelain --force-with-lease=refs/heads/feat/x:t1 origin t2:refs/heads/feat/x",
		"gh pr edit 5 --repo acme/w --title=<title> --body-file -")
	// a ready PR that would gain unverified commits says it returns to draft
	fx.PR.Draft, fx.ReadyWhy = false, "not verified"
	if p, e := PlanFor(fx, Request{Act: ActUpdate, Draft: true}); e != nil || !p.ToDraft || !strings.Contains(p.Summary, "returns to draft first: not verified") {
		t.Errorf("update with draft = %+v %v", p, e)
	}
}

// linkPR makes the card's branch pushed and linked to PR #7 as GitHub has it.
func (w *world) linkPR(draft bool) Facts {
	w.t.Helper()
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		w.t.Fatal(e)
	}
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		w.t.Fatal(e)
	}
	w.f.PullRequest = domain.PullRequestRef{Repo: "acme/widget", Number: 7, URL: "https://github.com/acme/widget/pull/7", HeadSHA: fx.Tip}
	w.viewPR(draft, fx.Tip, "me")
	if fx, e = Resolve(ctx, w.env, w.repo, w.in()); e != nil {
		w.t.Fatal(e)
	}
	return fx
}

func (w *world) viewPR(draft bool, head, owner string) {
	d := "false"
	if draft {
		d = "true"
	}
	w.write("view.json", `{"number":7,"url":"https://github.com/acme/widget/pull/7","state":"OPEN","isDraft":`+d+`,"headRefOid":"`+head+`","headRefName":"feat/rate-limit","headRepositoryOwner":{"login":"`+owner+`"}}`)
}

func (w *world) ghLog() string {
	b, _ := os.ReadFile(filepath.Join(w.fake, "log"))
	return string(b)
}

func TestUpdateEditsThePRsTitleAndBody(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx := w.linkPR(false)
	res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActUpdate, Title: " new title ", Body: "the why\n", Fingerprint: fx.Fingerprint()}, w.link)
	if e != nil || res.Pushed != "" || res.PR.Number != 7 || res.PR.HeadSHA != fx.Tip {
		t.Fatalf("update = %+v %v", res, e)
	}
	if !strings.Contains(w.ghLog(), "pr edit 7 --repo acme/widget --title=new title --body-file -") {
		t.Fatalf("gh was called as:\n%s", w.ghLog())
	}
	if b, _ := os.ReadFile(filepath.Join(w.fake, "edit-body")); string(b) != "the why\n" {
		t.Fatalf("body sent = %q", b)
	}
	// a title alone sends no body: the PR's own stays
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActUpdate, Title: "again", Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(strings.TrimSpace(w.ghLog()), "pr edit 7 --repo acme/widget --title=again") {
		t.Fatalf("gh was called as:\n%s", w.ghLog())
	}
	w.write("fail-pr-edit", "GraphQL: Resource protected by organization SAML enforcement")
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActUpdate, Title: "x", Fingerprint: fx.Fingerprint()}, w.link); e == nil || e.Code != CodeAuthFailed {
		t.Fatalf("an edit the token may not make: %v, want auth-failed", e)
	}
}

func TestReadyAndDraftAreTheirOwnActs(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx := w.linkPR(true)
	res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActReady, Fingerprint: fx.Fingerprint()}, w.link)
	if e != nil || res.Pushed != "" || res.PR.Number != 7 || !strings.HasSuffix(strings.TrimSpace(w.ghLog()), "pr ready 7 --repo acme/widget") {
		t.Fatalf("ready = %+v %v\n%s", res, e, w.ghLog())
	}
	// the confirm was for a draft; GitHub now says ready, so the old
	// fingerprint no longer runs anything
	w.viewPR(false, fx.Tip, "me")
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActDraft, Fingerprint: fx.Fingerprint()}, w.link); e == nil || e.Code != CodeFactsChanged {
		t.Fatalf("an act on a PR that changed on GitHub: %v, want facts-changed", e)
	}
	fx, _ = Resolve(ctx, w.env, w.repo, w.in())
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActDraft, Fingerprint: fx.Fingerprint()}, w.link); e != nil || !strings.HasSuffix(strings.TrimSpace(w.ghLog()), "pr ready 7 --repo acme/widget --undo") {
		t.Fatalf("draft = %v\n%s", e, w.ghLog())
	}
	if len(w.linked) != 0 {
		t.Fatalf("an act that pushed nothing moved the link: %+v", w.linked)
	}
	w.write("fail-pr-ready", "HTTP 401: Bad credentials")
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActDraft, Fingerprint: fx.Fingerprint()}, w.link); e == nil || e.Code != CodeGHNotSignedIn {
		t.Fatalf("draft with gh signed out: %v", e)
	}
	w.viewPR(true, fx.Tip, "me")
	fx, _ = Resolve(ctx, w.env, w.repo, w.in())
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActReady, Fingerprint: fx.Fingerprint()}, w.link); e == nil || e.Code != CodeGHNotSignedIn {
		t.Fatalf("ready with gh signed out: %v", e)
	}
}

func TestAnActNeedsTheFingerprintOfTheFactsThatWereRead(t *testing.T) {
	w := newWorld(t)
	if _, e := Do(context.Background(), w.env, w.repo, w.in(), Request{Act: ActPush}, w.link); e == nil || e.Code != CodeConfirmationNeeded {
		t.Fatalf("an act with no fingerprint: %v", e)
	}
	w.f.StackID = "S1"
	if _, e := Do(context.Background(), w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: "x"}, w.link); e == nil || e.Code != CodeStacked {
		t.Fatalf("an act on a card that may not publish: %v", e)
	}
}

// A commit that lands on the branch after the facts were read for the act
// is never pushed: the push names the SHA that was shown.
func TestABranchThatMovesUnderTheActIsNotPushed(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	unseen := w.run(w.dir, "git", "commit-tree", fx.Tip+"^{tree}", "-p", fx.Tip, "-m", "unseen")
	// the fake gh moves the branch during the act's own read of the facts,
	// after the tip was taken
	w.write("move", unseen)
	_, e = Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link)
	if e == nil || e.Code != CodeBranchMoved {
		t.Fatalf("a branch moved under the act: %v, want branch-moved", e)
	}
	if out := w.run(w.dir, "git", "--git-dir", w.bare, "branch", "--list"); out != "" {
		t.Fatalf("something was pushed: %q", out)
	}
}

func TestAPushTheRemoteOrAHookRefusesIsNamed(t *testing.T) {
	ctx := context.Background()
	hook := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("protected branch", func(t *testing.T) {
		w := newWorld(t)
		hook(filepath.Join(w.bare, "hooks", "pre-receive"), `echo "error: GH006: Protected branch update failed for refs/heads/feat/rate-limit." >&2; exit 1`)
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link)
		if e == nil || e.Code != CodeProtected || res.Partial() {
			t.Fatalf("push to a protected branch = %+v %v", res, e)
		}
		if strings.Contains(w.ghLog(), "pr create") {
			t.Fatal("a PR was opened for a branch that was not pushed")
		}
	})
	t.Run("pre-push says no", func(t *testing.T) {
		w := newWorld(t)
		hook(filepath.Join(w.dir, ".git", "hooks", "pre-push"), `echo "lint failed" >&2; exit 1`)
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link); e == nil || e.Code != CodeHookRejected || !strings.Contains(e.Text, "lint failed") {
			t.Fatalf("a pre-push hook that says no: %v, want hook-rejected", e)
		}
	})
	t.Run("returned to draft, then refused", func(t *testing.T) {
		w := newWorld(t)
		w.linkPR(false)
		w.git("checkout", "-q", "feat/rate-limit")
		w.git("commit", "-q", "--allow-empty", "-m", "more")
		w.git("checkout", "-q", "main")
		hook(filepath.Join(w.bare, "hooks", "pre-receive"), `echo "declined" >&2; exit 1`)
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Draft: true, Fingerprint: fx.Fingerprint()}, w.link)
		if e == nil || !res.Partial() || res.Pushed != "" || !strings.Contains(e.Text, "before that, PR #7 was returned to draft") {
			t.Fatalf("a refused push after the PR went to draft = %+v %v", res, e)
		}
	})
}

func TestAPRThatOpenedIsNeverReportedAsNotOpened(t *testing.T) {
	ctx := context.Background()
	t.Run("the link fails", func(t *testing.T) {
		w := newWorld(t)
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()},
			func(context.Context, domain.PullRequestRef) error { return errors.New("database is locked") })
		if e != nil || res.PR.Number != 512 || !strings.Contains(res.LinkErr, "gummi pr link FD-208 512") {
			t.Fatalf("create whose link failed = %+v %v", res, e)
		}
	})
	t.Run("gh answers something else", func(t *testing.T) {
		w := newWorld(t)
		w.write("create-says", "Creating pull request for me:feat/rate-limit\n")
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link)
		if e == nil || e.Code != CodeLinkFailed || res.Pushed != fx.Tip || !strings.Contains(e.Text, "look for the PR on GitHub") {
			t.Fatalf("create with no URL back = %+v %v", res, e)
		}
	})
	t.Run("the link's head cannot follow a push", func(t *testing.T) {
		w := newWorld(t)
		w.linkPR(true)
		w.git("checkout", "-q", "feat/rate-limit")
		w.git("commit", "-q", "--allow-empty", "-m", "more")
		w.git("checkout", "-q", "main")
		fx, _ := Resolve(ctx, w.env, w.repo, w.in())
		res, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()},
			func(context.Context, domain.PullRequestRef) error { return errors.New("database is locked") })
		if e != nil || res.Pushed != fx.Tip || !strings.Contains(res.LinkErr, "the push succeeded") {
			t.Fatalf("push whose link failed = %+v %v", res, e)
		}
	})
}

func TestTheLinkedPRMustBeTheOneThisCardPushesTo(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.linkPR(true)
	w.viewPR(true, "abc", "someone-else")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeForkNotYours || !strings.Contains(e.Text, "someone-else:feat/rate-limit") {
		t.Fatalf("a PR whose head is another fork: %v", e)
	}
	w.write("view.json", "not json")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeFailed {
		t.Fatalf("a garbled gh pr view: %v", e)
	}
	w.write("fail-pr-view", "GraphQL: Could not resolve to a PullRequest")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeFailed {
		t.Fatalf("a PR gh cannot find: %v", e)
	}
}

// An open PR for this head that the card does not link is found, named, and
// holds both a create and a bare push; another owner's PR of the same branch
// name is not this card's.
func TestAnUnlinkedOpenPRForTheHeadIsFound(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.write("list.json", `[{"number":4,"headRepositoryOwner":{"login":"stranger"}}]`)
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil || fx.OpenPR != 0 {
		t.Fatalf("a stranger's PR of the same branch name: %+v %v", fx.OpenPR, e)
	}
	w.write("list.json", `[{"number":4,"headRepositoryOwner":{"login":"stranger"}},{"number":9,"headRepositoryOwner":{"login":"Me"}}]`)
	fx, e = Resolve(ctx, w.env, w.repo, w.in())
	if e != nil || fx.OpenPR != 9 {
		t.Fatalf("OpenPR = %d %v, want 9", fx.OpenPR, e)
	}
	for _, req := range []Request{{Act: ActPush}, {Act: ActCreate, Title: "t"}} {
		req.Fingerprint = fx.Fingerprint()
		if _, e := Do(ctx, w.env, w.repo, w.in(), req, w.link); e == nil || e.Code != CodePRExists || e.PR != 9 {
			t.Errorf("%s under an unlinked PR: %v", req.Act, e)
		}
	}
	w.write("fail-pr-list", "HTTP 502")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeFailed {
		t.Fatalf("a failing gh pr list: %v", e)
	}
}

func TestAGHThatDoesNotAnswerIsATimeoutNotAFrozenFace(t *testing.T) {
	w := newWorld(t)
	w.write("hang", "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := Detect(ctx, w.env)
	if e == nil || e.Code != CodeTimeout {
		t.Fatalf("a gh that hangs: %v, want timeout", e)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the hung gh was waited on for %s", d)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, e := Resolve(ctx2, w.env, w.repo, w.in()); e == nil || e.Code != CodeTimeout {
		t.Fatalf("facts read through a gh that hangs: %v, want timeout", e)
	}
}

type fakeTree struct {
	dirty, rebasing, landed bool
	err                     error
}

func (t fakeTree) Dirty(context.Context, *domain.Feature) (bool, error) { return t.dirty, t.err }
func (t fakeTree) RebaseInProgress(context.Context, *domain.Feature) (bool, error) {
	return t.rebasing, t.err
}
func (t fakeTree) Landed(context.Context, *domain.Feature) (bool, error) { return t.landed, nil }
func (fakeTree) BaseBranch(context.Context) string                       { return "trunk" }

func TestInputForReadsTheCardTheSameForEveryFace(t *testing.T) {
	ctx := context.Background()
	anns := []domain.DiffAnnotation{{Resolved: true}, {}, {}}
	in := InputFor(ctx, fakeTree{}, verified("x"), true, anns, 3)
	if in.Base != "trunk" || in.OpenComments != 2 || in.OpenSpec != 3 || !in.Card.Busy || in.Card.Dirty || in.Card.Landed {
		t.Fatalf("input = %+v", in)
	}
	// a tree that cannot be read is not known clean
	in = InputFor(ctx, fakeTree{err: errors.New("no worktree")}, verified("x"), false, nil, 0)
	if !in.Card.Dirty || !in.Card.Rebasing {
		t.Fatalf("an unreadable tree = %+v, want it refused as dirty", in.Card)
	}
	f := verified("x")
	f.Base, f.LandedSHA = "release", "abc"
	if in = InputFor(ctx, fakeTree{}, f, false, nil, 0); in.Base != "release" || !in.Card.Landed {
		t.Fatalf("a landed card on its own base = %+v", in)
	}
	done := verified("x")
	done.Stage = domain.StageDone
	if in = InputFor(ctx, fakeTree{landed: true}, done, false, nil, 0); !in.Card.Landed {
		t.Fatal("a done card whose branch is in the base is not seen as landed")
	}
	done.HandedOffAt = time.Unix(3, 0)
	if in = InputFor(ctx, fakeTree{landed: true}, done, false, nil, 0); in.Card.Landed {
		t.Fatal("a handed-off card is read as landed before anyone merged it")
	}
}

func TestDefaultTextStartsFromTheCommits(t *testing.T) {
	f := verified("x")
	if title, body := DefaultText(f, nil); title != f.Title || body != "" {
		t.Fatalf("no commits: %q %q", title, body)
	}
	if title, body := DefaultText(f, []string{"fix: one"}); title != "fix: one" || body != "" {
		t.Fatalf("one commit: %q %q", title, body)
	}
	if title, body := DefaultText(f, []string{"a", "b"}); title != f.Title || body != "- a\n- b\n" {
		t.Fatalf("two commits: %q %q", title, body)
	}
	e := fail(CodeDirty, "uncommitted work", "commit it")
	if e.Error() != "uncommitted work — commit it" || fail(CodeDirty, "x", "").Error() != "x" || AsError(nil) != nil {
		t.Fatal("an error's words")
	}
}

// An act says each step as it starts and how it ended, in the order the
// plan listed them, so a face can draw the plan as the act's progress.
func TestAnActReportsThePlansStepsAsItRunsThem(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	req := Request{Act: ActCreate, Title: "t", Draft: true, Fingerprint: fx.Fingerprint()}
	plan, e := PlanFor(fx, req)
	if e != nil {
		t.Fatal(e)
	}
	var want, got []string
	for _, s := range plan.Steps {
		want = append(want, string(s.ID)+" run", string(s.ID)+" done")
	}
	if len(plan.Steps) != 3 || plan.Steps[0].ID != StepCheck || plan.Steps[1].ID != StepPush || plan.Steps[2].ID != StepCreate {
		t.Fatalf("a create's steps = %+v", plan.Steps)
	}
	env := w.env
	env.Progress = func(s Step, st StepState) {
		if s.Text == "" || s.Text == string(s.ID) {
			t.Errorf("step %s is reported without the plan's words", s.ID)
		}
		got = append(got, string(s.ID)+" "+string(st))
	}
	if _, e := Do(ctx, env, w.repo, w.in(), req, w.link); e != nil {
		t.Fatal(e)
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("reported %v, want %v", got, want)
	}

	// the step that failed is the one named, and nothing after it starts
	got = nil
	if _, e := Do(ctx, env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: "stale"}, w.link); e == nil {
		t.Fatal("an act on stale facts ran")
	}
	if strings.Join(got, ", ") != "check run, check fail" {
		t.Fatalf("a refused act reported %v", got)
	}
}
