package publish

import (
	"context"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

func verified(tip string) *domain.Feature {
	return &domain.Feature{ID: "FD-208", Title: "Rate-limit pairing", Slug: "rate-limit", BranchScheme: domain.BranchSchemeKind,
		Stage: domain.StageVerify, VerifiedAt: time.Unix(1, 0), VerifiedRev: tip}
}

func TestRefusalNamesEveryCardThatIsNotPublishable(t *testing.T) {
	base := func() *domain.Feature { f := verified("x"); return f }
	cases := []struct {
		name string
		mut  func(*Card)
		want Code
	}{
		{"research", func(c *Card) { c.F.Kind = domain.KindResearch }, CodeNoBranch},
		{"goal", func(c *Card) { c.F.Kind = domain.KindGoal }, CodeNoBranch},
		{"main checkout", func(c *Card) { c.F.MainCheckout = true }, CodeNoBranch},
		{"todo", func(c *Card) { c.F.Stage = domain.StageTodo }, CodeNoBranch},
		{"stacked", func(c *Card) { c.F.StackID = "S1" }, CodeStacked},
		{"goal member", func(c *Card) { c.F.GoalID = "GL-1" }, CodeGoalMember},
		{"landed", func(c *Card) { c.Landed = true }, CodeLanded},
		{"continued", func(c *Card) { c.F.ContinuedAs = "FD-9" }, CodeContinued},
		{"busy", func(c *Card) { c.Busy = true }, CodeBusy},
		{"rebasing", func(c *Card) { c.Rebasing = true }, CodeRebasing},
		{"dirty", func(c *Card) { c.Dirty = true }, CodeDirty},
	}
	for _, tc := range cases {
		c := Card{F: base()}
		tc.mut(&c)
		if got := Refusal(c); got == nil || got.Code != tc.want {
			t.Errorf("%s: refusal = %v, want %s", tc.name, got, tc.want)
		}
	}
	if r := Refusal(Card{F: base()}); r != nil {
		t.Fatalf("a verified card is refused: %v", r)
	}
	dropped := base()
	dropped.GoalID, dropped.GoalDroppedAt = "GL-1", time.Unix(2, 0)
	if r := Refusal(Card{F: dropped}); r != nil {
		t.Fatalf("a card its goal dropped is refused: %v", r)
	}
}

func TestTheFloorIsTheLandingFloor(t *testing.T) {
	if Floor(verified("abc"), "abc", 0) != nil {
		t.Fatal("verified at the tip should be ready")
	}
	if e := Floor(verified("abc"), "def", 0); e == nil || e.Code != CodeNotVerified {
		t.Fatalf("a tip past the verified rev = %v, want not-verified", e)
	}
	ff := &domain.Feature{ID: "FF-1", Kind: domain.KindFreeform, Stage: domain.StageOpen}
	if Floor(ff, "abc", 0) != nil {
		t.Fatal("a freeform card with no open comments should be ready")
	}
	if e := Floor(ff, "abc", 2); e == nil || e.Code != CodeUnresolved {
		t.Fatalf("a freeform card with open comments = %v, want unresolved", e)
	}
	handed := verified("abc")
	handed.Stage, handed.HandedOffAt = domain.StageDone, time.Unix(3, 0)
	if Floor(handed, "abc", 0) != nil {
		t.Fatal("a handed-off card at its verified rev should be ready")
	}
	if Floor(handed, "def", 0) == nil {
		t.Fatal("a handed-off card past its verified rev must not be ready")
	}
}

func TestPlanKeepsUnverifiedWorkOffAReadyPR(t *testing.T) {
	fx := Facts{Branch: "feat/x", Tip: "t2", Base: "main", Remote: "origin", RemoteBranch: "feat/x", HeadRepo: "me/w", BaseRepo: "acme/w",
		Push: PushFastForward, Ahead: 2, ReadyWhy: "not verified",
		PR: &PR{Repo: "acme/w", Number: 5, State: "OPEN", HeadSHA: "t1", HeadOwner: "me", HeadBranch: "feat/x"}}
	if _, e := PlanFor(fx, Request{Act: ActPush}); e == nil || e.Code != CodeNotVerified {
		t.Fatalf("push of an unverified tip to a ready PR = %v, want not-verified", e)
	}
	p, e := PlanFor(fx, Request{Act: ActPush, Draft: true})
	if e != nil || !p.ToDraft || !strings.HasPrefix(p.Commands[0], "gh pr ready 5 --repo acme/w --undo") {
		t.Fatalf("push with draft = %+v %v, want the PR turned back into a draft first", p, e)
	}
	fx.PR.Draft = true
	if _, e := PlanFor(fx, Request{Act: ActReady}); e == nil || e.Code != CodeNotVerified {
		t.Fatalf("ready over an unverified tip = %v", e)
	}
	fx.ReadyWhy = ""
	if _, e := PlanFor(fx, Request{Act: ActReady}); e == nil || e.Code != CodeHeadElsewhere {
		t.Fatalf("ready while GitHub has another head = %v, want %s", e, CodeHeadElsewhere)
	}
	fx.PR.HeadSHA = fx.Tip
	if _, e := PlanFor(fx, Request{Act: ActReady}); e != nil {
		t.Fatalf("ready at the verified tip GitHub has: %v", e)
	}
}

func TestCreateOpensADraftBelowTheFloor(t *testing.T) {
	fx := Facts{Branch: "feat/x", Tip: "t", Base: "main", Remote: "origin", RemoteBranch: "feat/x", HeadRepo: "me/w", BaseRepo: "acme/w", Push: PushNew, Ahead: 1, ReadyWhy: "no"}
	p, e := PlanFor(fx, Request{Act: ActCreate, Title: "x"})
	if e != nil || !p.Draft || !strings.Contains(p.Commands[1], "--head me:feat/x") || !strings.HasSuffix(p.Commands[1], "--draft") {
		t.Fatalf("plan = %+v %v, want a draft PR from me:feat/x", p, e)
	}
	fx.OpenPR = 9
	if _, e := PlanFor(fx, Request{Act: ActCreate, Title: "x"}); e == nil || e.Code != CodePRExists || e.PR != 9 {
		t.Fatalf("create with an open PR for the head = %v", e)
	}
}

func TestRepoOfURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:me/widget.git":           "me/widget",
		"https://github.com/acme/widget":         "acme/widget",
		"https://x@github.com/acme/widget.git":   "acme/widget",
		"ssh://git@github.com/acme/w.x.git":      "acme/w.x",
		"git@git.corp.example:acme/widget.git":   "",
		"https://github.com.evil.example/a/b":    "",
		"https://github.com/acme/widget/pull/12": "",
	} {
		if got := RepoOfURL(in); got != want {
			t.Errorf("RepoOfURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Publishing is reached only from the faces a person uses: the engine,
// the driver, the agent's MCP shim and every loop that runs without one
// must not import it (DESIGN §22.7).
func TestOnlyPersonFacingCodeImportsPublish(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, pkg := range []string{"engine", "driver", "mcp", "goalpolicy", "schedule", "agent", "verify", "spec", "workflow", "state", "worktree"} {
		p, err := build.ImportDir(filepath.Join(root, "internal", pkg), 0)
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
		for _, imp := range p.Imports {
			if imp == "github.com/morphis/gummi/internal/publish" {
				t.Errorf("internal/%s imports publish; only the person-facing faces may", pkg)
			}
		}
	}
}

// --- end to end, against a local bare repository standing in for GitHub ---

type fakeRepo struct {
	dir    string
	target worktree.PushTarget
}

func (r fakeRepo) RepoRoot() string                        { return r.dir }
func (r fakeRepo) Path(*domain.Feature) (string, error) { return r.dir, nil }
func (r fakeRepo) PushTarget(context.Context, *domain.Feature) (worktree.PushTarget, error) {
	return r.target, nil
}

const ghShim = `#!/bin/sh
echo "$*" >> "$GH_FAKE/log"
case "$1 $2" in
"auth status") exit 0 ;;
"repo view") cat "$GH_FAKE/repo.json" ;;
"pr list") cat "$GH_FAKE/list.json" 2>/dev/null || echo '[]' ;;
"pr view") cat "$GH_FAKE/view.json" ;;
"pr create") cat > "$GH_FAKE/body"; echo "https://github.com/acme/widget/pull/512" ;;
*) exit 0 ;;
esac
`

type world struct {
	t                  *testing.T
	dir, bare, fake    string
	env                Env
	f                  *domain.Feature
	repo               fakeRepo
	linked             []domain.PullRequestRef
}

func newWorld(t *testing.T) *world {
	t.Helper()
	tmp := t.TempDir()
	w := &world{t: t, dir: filepath.Join(tmp, "w"), bare: filepath.Join(tmp, "remote.git"), fake: filepath.Join(tmp, "gh")}
	if err := os.MkdirAll(w.fake, 0o750); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(w.fake, "gh")
	if err := os.WriteFile(gh, []byte(ghShim), 0o700); err != nil { //nolint:gosec // test shim
		t.Fatal(err)
	}
	t.Setenv("GH_FAKE", w.fake)
	w.write("repo.json", `{"nameWithOwner":"me/widget","viewerPermission":"WRITE","isFork":true,"parent":{"name":"widget","owner":{"login":"acme"}}}`)
	w.run(tmp, "git", "init", "-q", "--bare", w.bare)
	w.run(tmp, "git", "init", "-q", "-b", "main", w.dir)
	w.git("config", "user.email", "t@example.com")
	w.git("config", "user.name", "T")
	w.git("commit", "-q", "--allow-empty", "-m", "base")
	w.git("remote", "add", "origin", "git@github.com:me/widget.git")
	w.git("remote", "add", "upstream", "https://github.com/acme/widget.git")
	w.git("config", "url."+w.bare+".insteadOf", "git@github.com:me/widget.git")
	w.git("checkout", "-q", "-b", "feat/rate-limit")
	w.git("commit", "-q", "--allow-empty", "-m", "feat: limit")
	w.git("checkout", "-q", "main")
	RewriteAllowed = func(string) bool { return true }
	t.Cleanup(func() { RewriteAllowed = func(string) bool { return false } })
	w.f = verified(w.git("rev-parse", "feat/rate-limit"))
	w.env = Env{GH: gh}
	w.repo = fakeRepo{dir: w.dir, target: worktree.PushTarget{Remote: "origin", Branch: "feat/rate-limit", How: "origin"}}
	return w
}

func (w *world) write(name, body string) {
	if err := os.WriteFile(filepath.Join(w.fake, name), []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) run(dir, name string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) git(args ...string) string { return w.run(w.dir, "git", args...) }

func (w *world) in() Input { return Input{Card: Card{F: w.f}, Base: "main"} }

func (w *world) link(_ context.Context, ref domain.PullRequestRef) error {
	w.linked = append(w.linked, ref)
	return nil
}

func TestCreatePushesToTheForkAndOpensThePRUpstream(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	if fx.HeadRepo != "me/widget" || fx.BaseRepo != "acme/widget" || fx.Push != PushNew || fx.HeadRef() != "me:feat/rate-limit" || fx.ReadyWhy != "" {
		t.Fatalf("facts = %+v", fx)
	}
	req := Request{Act: ActCreate, Title: "feat: limit", Body: "why\n", Fingerprint: fx.Fingerprint()}
	res, e := Do(ctx, w.env, w.repo, w.in(), req, w.link)
	if e != nil {
		t.Fatal(e)
	}
	if res.Pushed != fx.Tip || res.PR.Number != 512 || res.Draft || len(w.linked) != 1 || w.linked[0].HeadSHA != fx.Tip {
		t.Fatalf("result = %+v linked %v", res, w.linked)
	}
	if got := w.run(w.dir, "git", "--git-dir", w.bare, "rev-parse", "feat/rate-limit"); got != fx.Tip {
		t.Fatalf("remote has %s, want %s", got, fx.Tip)
	}
	log, _ := os.ReadFile(filepath.Join(w.fake, "log"))
	if !strings.Contains(string(log), "pr create --repo acme/widget --head me:feat/rate-limit --base main --title=feat: limit --body-file -") {
		t.Fatalf("gh was called as:\n%s", log)
	}
	if up := w.git("config", "branch.feat/rate-limit.remote"); up != "origin" {
		t.Fatalf("upstream = %q, want origin recorded after the first push", up)
	}
}

func TestAnActRefusesWhenTheFactsChangedAfterTheConfirm(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil {
		t.Fatal(e)
	}
	// an agent writes a pre-push hook between the confirm and the act
	hook := filepath.Join(w.dir, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test hook
		t.Fatal(err)
	}
	_, e = Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link)
	if e == nil || e.Code != CodeFactsChanged {
		t.Fatalf("act after a hook appeared = %v, want facts-changed", e)
	}
	if out := w.run(w.dir, "git", "--git-dir", w.bare, "branch", "--list"); out != "" {
		t.Fatalf("something was pushed: %q", out)
	}
	fx2, _ := Resolve(ctx, w.env, w.repo, w.in())
	if fx2.Hook != hook {
		t.Fatalf("hook = %q, want it shown", fx2.Hook)
	}
}

func TestARewriteOfAPushedBranchIsPushedWithAPinnedLease(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	fx, _ := Resolve(ctx, w.env, w.repo, w.in())
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	w.git("checkout", "-q", "feat/rate-limit")
	w.git("commit", "-q", "--amend", "--allow-empty", "-m", "feat: limit, reworded")
	w.git("checkout", "-q", "main")
	w.f.VerifiedRev = w.git("rev-parse", "feat/rate-limit")
	fx, e := Resolve(ctx, w.env, w.repo, w.in())
	if e != nil || fx.Push != PushLease {
		t.Fatalf("after a rewrite: %+v %v, want a lease push", fx.Push, e)
	}
	if _, e := Do(ctx, w.env, w.repo, w.in(), Request{Act: ActPush, Fingerprint: fx.Fingerprint()}, w.link); e != nil {
		t.Fatal(e)
	}
	// someone else pushes: the remote has commits the card lacks
	other := w.run(w.dir, "git", "commit-tree", fx.Tip+"^{tree}", "-p", fx.Tip, "-m", "theirs")
	w.run(w.dir, "git", "push", "-q", "-f", w.bare, other+":refs/heads/feat/rate-limit")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeRemoteAhead {
		t.Fatalf("with the remote ahead: %v, want remote-ahead", e)
	}
}

func TestAnOccupiedRemoteNameIsNeverOverwritten(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	unrelated := w.run(w.dir, "git", "commit-tree", "-m", "someone else's", w.git("rev-parse", "main^{tree}"))
	w.run(w.dir, "git", "push", "-q", w.bare, unrelated+":refs/heads/feat/rate-limit")
	if _, e := Resolve(ctx, w.env, w.repo, w.in()); e == nil || e.Code != CodeNameTaken {
		t.Fatalf("an unrelated branch of the same name: %v, want name-taken", e)
	}
}
