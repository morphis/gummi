package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unchosen is a fork whose PR target nobody has recorded.
func unchosen(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.git("config", "--unset-all", "remote.upstream.gh-resolved")
	return w
}

// A fork's PR can open in the fork or in its parent. Which remotes the
// repository happens to have says nothing about which the person means, so
// until they choose nothing is opened anywhere.
func TestAForksPRTargetIsAskedNeverGuessed(t *testing.T) {
	w := unchosen(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	if fx.BaseRepo != "" || strings.Join(fx.BaseRepos, " ") != "me/widget acme/widget" {
		t.Fatalf("base = %q of %v, want none chosen of the fork and its parent", fx.BaseRepo, fx.BaseRepos)
	}
	_, e = PlanFor(fx, Request{Act: ActCreate, Title: "t"})
	if e == nil || e.Code != CodeBaseUnchosen || !strings.Contains(e.Error(), "me/widget or acme/widget") {
		t.Fatalf("create with no target = %v, want base-unchosen naming both", e)
	}
	// a push opens nothing, so it asks nothing
	if _, e := PlanFor(fx, Request{Act: ActPush}); e != nil {
		t.Fatalf("push with no target chosen: %v", e)
	}
	// without the parent as a remote the answer is the same question
	w.git("remote", "remove", "upstream")
	if fx, e = Resolve(ctx, w.env, w.repo, w.in()); e != nil || fx.BaseRepo != "" || len(fx.BaseRepos) != 2 {
		t.Fatalf("with no remote for the parent: base %q of %v (%v)", fx.BaseRepo, fx.BaseRepos, e)
	}
	in := w.in()
	in.BaseRepo = "evil/elsewhere"
	if _, e := Resolve(ctx, w.env, w.repo, in); e == nil || e.Code != CodeBaseUnchosen {
		t.Fatalf("a target that is neither = %v, want base-unchosen", e)
	}
	if log, _ := os.ReadFile(filepath.Join(w.fake, "log")); strings.Contains(string(log), "pr create") {
		t.Fatalf("a PR was opened with no target chosen:\n%s", log)
	}
}

func TestTheChosenTargetIsRememberedOnceThePROpens(t *testing.T) {
	for _, tc := range []struct{ name, choice, key, val string }{
		{"the fork", "me/widget", "remote.origin.gh-resolved", "base"},
		{"the parent", "acme/widget", "remote.upstream.gh-resolved", "base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := unchosen(t)
			ctx := context.Background()
			in := w.in()
			in.BaseRepo = tc.choice
			fx, e := Resolve(ctx, w.env, w.repo, in)
			if e != nil || fx.BaseRepo != tc.choice {
				t.Fatalf("base = %q (%v), want %s", fx.BaseRepo, e, tc.choice)
			}
			// reading the facts records nothing: only the act does
			if got := w.git("config", "--default", "", "--get", tc.key); got != "" {
				t.Fatalf("%s = %q before any PR opened", tc.key, got)
			}
			// the yes is for the repository that was shown
			if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link); e == nil {
				t.Fatal("a confirm for one target ran with none chosen")
			}
			if _, e := Do(ctx, w.env, w.repo, in, Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link); e != nil {
				t.Fatal(e)
			}
			log, _ := os.ReadFile(filepath.Join(w.fake, "log"))
			if !strings.Contains(string(log), "pr create --repo "+tc.choice+" --head me:feat/rate-limit") {
				t.Fatalf("gh was not asked to open the PR in %s:\n%s", tc.choice, log)
			}
			if got := w.git("config", "--get", tc.key); got != tc.val {
				t.Fatalf("%s = %q, want %q", tc.key, got, tc.val)
			}
			// asked once: the next card's facts carry the answer, and a
			// remote added since does not move it
			w.git("remote", "add", "mirror", "https://github.com/else/widget.git")
			w.f.PullRequest.Number = 0
			if next, e := Resolve(ctx, w.env, w.repo, w.in()); e != nil || next.BaseRepo != tc.choice {
				t.Fatalf("the next resolve: base %q (%v), want the remembered %s", next.BaseRepo, e, tc.choice)
			}
		})
	}
}

// A parent the repository has no remote for is remembered as gh spells it.
func TestAParentWithNoRemoteIsRememberedByName(t *testing.T) {
	w := unchosen(t)
	w.git("remote", "remove", "upstream")
	ctx := context.Background()
	in := w.in()
	in.BaseRepo = "acme/widget"
	fx, e := Resolve(ctx, w.env, w.repo, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Do(ctx, w.env, w.repo, in, Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	if got := w.git("config", "--get", "remote.origin.gh-resolved"); got != "acme/widget" {
		t.Fatalf("remote.origin.gh-resolved = %q", got)
	}
	if next, e := Resolve(ctx, w.env, w.repo, w.in()); e != nil || next.BaseRepo != "acme/widget" {
		t.Fatalf("the next resolve: base %q (%v)", next.BaseRepo, e)
	}
}

// A choice made in the confirm replaces the recorded one.
func TestAnotherChoiceReplacesTheRememberedTarget(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.in()
	in.BaseRepo = "me/widget"
	fx, e := Resolve(ctx, w.env, w.repo, in)
	if e != nil || fx.BaseRepo != "me/widget" {
		t.Fatalf("base = %q (%v)", fx.BaseRepo, e)
	}
	if _, e := Do(ctx, w.env, w.repo, in, Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	if out := w.git("config", "--get-regexp", `gh-resolved$`); out != "remote.origin.gh-resolved base" {
		t.Fatalf("recorded = %q, want only origin", out)
	}
}

// An open PR is looked for wherever it could be open, not only in the
// repository the next one would go to.
func TestAnOpenPRIsLookedForInTheForkAndItsParent(t *testing.T) {
	w := unchosen(t)
	w.write("list.json", `[{"number":9,"headRepositoryOwner":{"login":"me"}}]`)
	fx, e := Resolve(context.Background(), w.env, w.repo, w.in())
	if e != nil || fx.OpenPR != 9 || fx.OpenPRRepo == "" {
		t.Fatalf("open PR = #%d in %q (%v)", fx.OpenPR, fx.OpenPRRepo, e)
	}
	log, _ := os.ReadFile(filepath.Join(w.fake, "log"))
	for _, repo := range []string{"me/widget", "acme/widget"} {
		if !strings.Contains(string(log), "pr list --repo "+repo+" ") {
			t.Errorf("%s was not searched:\n%s", repo, log)
		}
	}
	if _, e := PlanFor(fx, Request{Act: ActCreate, Title: "t"}); e == nil || e.Code != CodePRExists || !strings.Contains(e.Text, fx.OpenPRRepo) {
		t.Fatalf("create = %v, want pr-exists naming the repository", e)
	}
}

// GitHub's refusal of a token names neither the repository nor a way out.
func TestATokenThatMayNotOpenThePRIsNamedWithItsRepository(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	w.write("fail-pr-create", "pull request create failed: GraphQL: Resource not accessible by personal access token (createPullRequest)")
	_, e = Do(ctx, w.env, w.repo, w.in(), Request{Act: ActCreate, Title: "t", Fingerprint: fx.Fingerprint()}, w.link)
	if e == nil || e.Code != CodeTokenRefused {
		t.Fatalf("refusal = %v, want token-refused", e)
	}
	if !strings.Contains(e.Text, "in acme/widget") || !strings.Contains(e.Fix, "open the pull request in me/widget") {
		t.Fatalf("refusal = %q — %q, want the repository and the other target", e.Text, e.Fix)
	}
	// a PR that did not open is no choice made
	if got := w.git("config", "--get", "remote.upstream.gh-resolved"); got != "base" {
		t.Fatalf("the recorded target moved to %q", got)
	}
}
