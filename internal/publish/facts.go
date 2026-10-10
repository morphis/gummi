package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// PushMode is what a push of the branch would do to the remote branch.
type PushMode string

const (
	PushNew         PushMode = "new branch"
	PushFastForward PushMode = "fast-forward"
	PushUpToDate    PushMode = "up to date"
	// PushLease is a force push after a rewrite of a branch the remote has
	// exactly as gummi last saw it, pinned to that tip.
	PushLease PushMode = "force-with-lease"
)

// PR is a linked pull request as GitHub has it now.
type PR struct {
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"` // OPEN, CLOSED, MERGED
	Draft      bool   `json:"draft"`
	HeadSHA    string `json:"headSha"`
	HeadOwner  string `json:"headOwner"`
	HeadBranch string `json:"headBranch"`
}

// Open reports a PR still open on GitHub.
func (p *PR) Open() bool { return p != nil && p.State == "OPEN" }

// Facts are what a person confirms before an act, resolved rather than
// intended: every one is read again when the act runs, and a difference
// refuses it (CodeFactsChanged). Fingerprint is their digest.
type Facts struct {
	Card       domain.FeatureID `json:"card"`
	Branch     string           `json:"branch"`
	Tip        string           `json:"tip"`
	TipSubject string           `json:"tipSubject"`
	Base       string           `json:"base"`
	Ahead      int              `json:"ahead"`
	Remote     string           `json:"remote"`
	RemoteHow  string           `json:"remoteHow"`
	// RemoteBranch is the branch's name on the remote.
	RemoteBranch string `json:"remoteBranch"`
	// PushURL is where git will actually push, after pushurl and insteadOf.
	PushURL   string   `json:"pushUrl"`
	HeadRepo  string   `json:"headRepo"`
	BaseRepo  string   `json:"baseRepo"`
	RemoteTip string   `json:"remoteTip"`
	Push      PushMode `json:"push"`
	GH        string   `json:"gh"`
	// Hook names the hooks git will run during the push with the person's
	// credential loaded (pushHookNames), "" for none.
	Hook    string `json:"hook,omitempty"`
	HookSum string `json:"-"`
	// ConfigSum digests the git configuration and environment that steer
	// where a push goes and how it authenticates.
	ConfigSum string `json:"-"`
	PR        *PR    `json:"pr,omitempty"`
	// OpenPR is an open pull request GitHub already has for this head,
	// when none is linked.
	OpenPR int `json:"openPr,omitempty"`
	// ReadyWhy is why the branch may not be offered as ready ("" when it
	// may): the quality floor (Floor).
	ReadyWhy string `json:"readyWhy,omitempty"`
	// ReadyFix is what a person does about ReadyWhy.
	ReadyFix string `json:"readyFix,omitempty"`
	// Adopted cards add commits and never rewrite.
	Adopted bool `json:"adopted,omitempty"`
}

// HeadRef is the PR head as gh takes it: "owner:branch".
func (f Facts) HeadRef() string {
	owner, _, _ := strings.Cut(f.HeadRepo, "/")
	return owner + ":" + f.RemoteBranch
}

// Fingerprint digests every fact an act depends on. A face shows it with
// the confirm and sends it back with the yes; the act refuses when the
// facts it reads again do not digest the same.
func (f Facts) Fingerprint() string {
	pr := ""
	if f.PR != nil {
		pr = fmt.Sprintf("%s#%d %s %t %s", f.PR.Repo, f.PR.Number, f.PR.State, f.PR.Draft, f.PR.HeadSHA)
	}
	parts := []string{
		string(f.Card), f.Branch, f.Tip, f.Base, f.Remote, f.RemoteBranch, f.PushURL,
		f.HeadRepo, f.BaseRepo, f.RemoteTip, string(f.Push), f.GH, f.HookSum, f.ConfigSum, pr,
		strconv.Itoa(f.OpenPR), f.ReadyWhy,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// Repo is what Resolve reads off the card's repository: a worktree.Manager.
type Repo interface {
	RepoRoot() string
	Path(f *domain.Feature) (string, error)
	PushTarget(ctx context.Context, f *domain.Feature) (worktree.PushTarget, error)
}

// Input is the card and what the face knows about it.
type Input struct {
	Card Card
	// Base is the branch the card lands on (and its PR targets).
	Base string
	// OpenComments counts unresolved review comments on its diff, and
	// OpenSpec the open threads on its spec that hold its gate: the two
	// counts a landing refuses on.
	OpenComments, OpenSpec int
}

// Detect is the cheap check a face makes before it draws the acts: gh is
// there and signed in to github.com. It runs nothing on the network beyond
// gh's own status call and is cached by the caller.
func Detect(ctx context.Context, env Env) (gh string, err *Error) {
	gh, lerr := env.GHPath()
	if lerr != nil {
		return "", fail(CodeGHMissing, "gh, the GitHub CLI, is not on the path", "install it and run `gh auth login`")
	}
	env.GH = gh
	if _, serr := env.gh(ctx, nil, "auth", "status", "--hostname", "github.com"); serr != nil {
		if pe := AsError(serr); pe.Code == CodeTimeout {
			return gh, pe
		}
		return gh, fail(CodeGHNotSignedIn, "gh is not signed in to github.com", "run `gh auth login`")
	}
	return gh, nil
}

// Resolve reads the facts for an act on the card: locally (branch, tip,
// push target, hook, configuration) and then on the network (the remote
// branch, the repositories, the PR). Every refusal it finds is typed.
func Resolve(ctx context.Context, env Env, repo Repo, in Input) (Facts, *Error) {
	f := in.Card.F
	if r := Refusal(in.Card); r != nil {
		return Facts{}, r
	}
	tree, err := repo.Path(f)
	if err != nil {
		return Facts{}, AsError(err)
	}
	env.Dir = tree
	gh, derr := Detect(ctx, env)
	if derr != nil {
		return Facts{}, derr
	}
	env.GH = gh
	fx := Facts{Card: f.ID, Branch: f.BranchName(), Base: in.Base, GH: gh, Adopted: f.Adopted()}
	if fx.Branch == "" || !validRef(ctx, env, fx.Branch) {
		return Facts{}, fail(CodeNoBranch, "the card's branch name is not a valid branch", "")
	}
	if fx.Tip, err = env.git(ctx, "rev-parse", "--verify", "refs/heads/"+fx.Branch+"^{commit}"); err != nil {
		return Facts{}, fail(CodeNoBranch, "the card's branch "+fx.Branch+" does not exist", "")
	}
	fx.TipSubject, _ = env.git(ctx, "log", "-1", "--format=%s", fx.Tip)
	if n, cerr := env.git(ctx, "rev-list", "--count", fx.Base+".."+fx.Tip); cerr == nil {
		fx.Ahead, _ = strconv.Atoi(n)
	}
	if fx.Ahead == 0 && f.PullRequest.Empty() {
		return Facts{}, fail(CodeNothingToPublish, "the branch has no commits ahead of "+fx.Base, "")
	}
	target, terr := repo.PushTarget(ctx, f)
	switch {
	case errors.Is(terr, worktree.ErrNoRemote):
		return Facts{}, fail(CodeNoRemote, "this repository has no remote to push to", "")
	case errors.Is(terr, worktree.ErrRemoteAmbiguous):
		return Facts{}, fail(CodeRemoteAmbiguous, "this repository has several remotes and none is set as the push remote", "choose one: gummi push --remote <name> remembers it as branch."+fx.Branch+".pushRemote")
	case terr != nil:
		return Facts{}, AsError(terr)
	}
	fx.Remote, fx.RemoteHow, fx.RemoteBranch = target.Remote, target.How, target.Branch
	if fx.RemoteBranch == fx.Base {
		return Facts{}, fail(CodeOntoBase, "the push would go onto "+fx.Base+", the branch this card lands on", "")
	}
	if fx.RemoteBranch != fx.Branch && !f.Adopted() {
		// only a branch gummi did not cut lives under another name. For
		// one it cut, an upstream of another name is configuration written
		// since, and following it would push the card onto that branch
		return Facts{}, fail(CodeOntoBase, "this branch tracks "+fx.Remote+"/"+fx.RemoteBranch+", another name than its own; gummi publishes a card's branch under its own name", "push it yourself, or unset branch."+fx.Branch+".merge")
	}
	if !validRef(ctx, env, fx.RemoteBranch) {
		return Facts{}, fail(CodeFailed, "the remote branch name "+strconv.Quote(fx.RemoteBranch)+" is not valid", "")
	}
	configured, _ := env.git(ctx, "config", "--get", "remote."+fx.Remote+".pushurl")
	if configured == "" {
		configured, _ = env.git(ctx, "config", "--get", "remote."+fx.Remote+".url")
	}
	// every URL git would push to: a remote may carry several pushurls
	// and git pushes to all of them, so more than one is never published
	all, err := env.git(ctx, "remote", "get-url", "--push", "--all", fx.Remote)
	if err != nil || configured == "" || all == "" {
		return Facts{}, fail(CodeNoRemote, "the push remote "+fx.Remote+" has no URL", "")
	}
	if strings.Contains(all, "\n") {
		return Facts{}, fail(CodeRemoteAmbiguous, "the push remote "+fx.Remote+" has several push URLs and git would push to every one", "leave it one pushurl, or push it yourself")
	}
	fx.PushURL = all
	if fetch, _ := env.git(ctx, "config", "--get", "remote."+fx.Remote+".url"); RepoOfURL(fetch) != "" && !strings.EqualFold(RepoOfURL(fetch), RepoOfURL(configured)) {
		// the remote branch is read from the fetch URL and written to the
		// push URL: two repositories would make the plan about the wrong one
		return Facts{}, fail(CodeRemoteAmbiguous, "the remote "+fx.Remote+" fetches from "+RepoOfURL(fetch)+" and pushes to "+RepoOfURL(configured), "give the fork a remote of its own and set it as the branch's push remote")
	}
	if fx.HeadRepo = RepoOfURL(configured); fx.HeadRepo == "" {
		return Facts{}, fail(CodeUnsupportedHost, "the push remote "+fx.Remote+" ("+configured+") is not a github.com repository", "publishing is github.com only for now; push it yourself")
	}
	if RepoOfURL(fx.PushURL) != fx.HeadRepo && !RewriteAllowed(fx.PushURL) {
		// an insteadOf/pushInsteadOf rule sends the push somewhere other
		// than the repository the remote names: never published unseen
		return Facts{}, fail(CodeUnsupportedHost, "git rewrites the push to "+fx.Remote+" ("+configured+") to "+fx.PushURL, "remove the url.*.insteadOf rule, or push it yourself")
	}
	fx.Hook, fx.HookSum = pushHooks(ctx, env)
	fx.ConfigSum = configSum(ctx, env, fx.Branch)
	if r := resolveRemote(ctx, env, &fx, f); r != nil {
		return Facts{}, r
	}
	if r := resolvePR(ctx, env, &fx, f); r != nil {
		return Facts{}, r
	}
	if fl := Floor(f, fx.Tip, in.OpenComments+in.OpenSpec); fl != nil {
		fx.ReadyWhy, fx.ReadyFix = fl.Text, fl.Fix
	}
	return fx, nil
}

// resolveRemote reads the repositories and the remote branch: write access
// to the head repository, the PR's base (the head's parent for a fork that
// this repository also has as a remote, else the head itself), and what a
// push would do.
func resolveRemote(ctx context.Context, env Env, fx *Facts, f *domain.Feature) *Error {
	out, err := env.gh(ctx, nil, "repo", "view", fx.HeadRepo, "--json", "nameWithOwner,viewerPermission,isFork,parent")
	if err != nil {
		return AsError(err)
	}
	var view struct {
		NameWithOwner    string `json:"nameWithOwner"`
		ViewerPermission string `json:"viewerPermission"`
		IsFork           bool   `json:"isFork"`
		Parent           *struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"parent"`
	}
	if jerr := json.Unmarshal([]byte(out), &view); jerr != nil {
		return fail(CodeFailed, "gh repo view: "+jerr.Error(), "")
	}
	switch view.ViewerPermission {
	case "ADMIN", "MAINTAIN", "WRITE":
	default:
		code := CodeNoWriteAccess
		if f.Adopted() {
			code = CodeForkNotYours
		}
		return fail(code, "you cannot push to "+fx.HeadRepo+" (permission "+strings.ToLower(view.ViewerPermission)+")", "add a fork you own as a remote and set it as the branch's push remote")
	}
	fx.BaseRepo = fx.HeadRepo
	if view.IsFork && view.Parent != nil {
		parent := view.Parent.Owner.Login + "/" + view.Parent.Name
		if remoteRepos(ctx, env)[strings.ToLower(parent)] {
			fx.BaseRepo = parent
		}
	}
	if !f.PullRequest.Empty() {
		fx.BaseRepo = f.PullRequest.Repo
	}
	ls, err := env.git(ctx, "ls-remote", fx.Remote, "refs/heads/"+fx.RemoteBranch)
	if err != nil {
		return pushError(err.Error(), err)
	}
	fx.RemoteTip, _, _ = strings.Cut(ls, "\t")
	fx.RemoteTip = strings.TrimSpace(fx.RemoteTip)
	switch {
	case fx.RemoteTip == "":
		fx.Push = PushNew
	case fx.RemoteTip == fx.Tip:
		fx.Push = PushUpToDate
	case env.gitOK(ctx, "cat-file", "-e", fx.RemoteTip+"^{commit}") && env.gitOK(ctx, "merge-base", "--is-ancestor", fx.RemoteTip, fx.Tip):
		fx.Push = PushFastForward
	default:
		known := env.gitOK(ctx, "cat-file", "-e", fx.RemoteTip+"^{commit}")
		switch {
		case !known || env.gitOK(ctx, "merge-base", "--is-ancestor", fx.Tip, fx.RemoteTip):
			return fail(CodeRemoteAhead, fx.Remote+"/"+fx.RemoteBranch+" has commits this card does not; nothing was overwritten", "fetch them into the card first")
		case !f.Adopted() && wasBranchTip(ctx, env, fx.Branch, fx.RemoteTip):
			// the remote holds a tip this very branch once had and was
			// rewritten away from: a force, pinned to that tip. A fetch
			// alone never earns it: commits someone else put there were
			// never this branch's tip
			fx.Push = PushLease
		case f.Adopted():
			return fail(CodeRemoteAhead, "the remote branch and this adopted branch have diverged; gummi never rewrites an adopted branch", "")
		default:
			return fail(CodeNameTaken, fx.Remote+" already has a branch "+fx.RemoteBranch+" with commits this card's branch never had; gummi never overwrites it", "fetch them into the card, rename the card's branch, or remove the remote one yourself")
		}
	}
	return nil
}

// resolvePR reads the linked PR as GitHub has it now, or looks for an open
// one for this head when none is linked.
func resolvePR(ctx context.Context, env Env, fx *Facts, f *domain.Feature) *Error {
	if f.PullRequest.Empty() {
		owner, _, _ := strings.Cut(fx.HeadRepo, "/")
		out, err := env.gh(ctx, nil, "pr", "list", "--repo", fx.BaseRepo, "--head", fx.RemoteBranch, "--state", "open", "--json", "number,headRepositoryOwner")
		if err != nil {
			return AsError(err)
		}
		var list []struct {
			Number int `json:"number"`
			Owner  struct {
				Login string `json:"login"`
			} `json:"headRepositoryOwner"`
		}
		if json.Unmarshal([]byte(out), &list) == nil {
			for _, p := range list {
				if strings.EqualFold(p.Owner.Login, owner) {
					fx.OpenPR = p.Number
				}
			}
		}
		return nil
	}
	p, err := ViewPR(ctx, env, f.PullRequest.Repo, f.PullRequest.Number)
	if err != nil {
		return AsError(err)
	}
	fx.PR = p
	owner, _, _ := strings.Cut(fx.HeadRepo, "/")
	if !strings.EqualFold(p.HeadOwner, owner) || p.HeadBranch != fx.RemoteBranch {
		return fail(CodeForkNotYours, fmt.Sprintf("PR #%d's head is %s:%s, not where this card pushes (%s)", p.Number, p.HeadOwner, p.HeadBranch, fx.HeadRef()),
			"set the branch's push remote to the PR's head repository, if it is yours")
	}
	return nil
}

// ViewPR reads one pull request's live state.
func ViewPR(ctx context.Context, env Env, repo string, number int) (*PR, error) {
	out, err := env.gh(ctx, nil, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", "number,url,state,isDraft,headRefOid,headRefName,headRepositoryOwner")
	if err != nil {
		return nil, err
	}
	var v struct {
		Number     int    `json:"number"`
		URL        string `json:"url"`
		State      string `json:"state"`
		IsDraft    bool   `json:"isDraft"`
		HeadRefOid string `json:"headRefOid"`
		HeadRef    string `json:"headRefName"`
		Owner      struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
	}
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		return nil, fail(CodeFailed, "gh pr view: "+jerr.Error(), "")
	}
	return &PR{
		Repo: repo, Number: v.Number, URL: v.URL, State: strings.ToUpper(v.State), Draft: v.IsDraft,
		HeadSHA: v.HeadRefOid, HeadOwner: v.Owner.Login, HeadBranch: v.HeadRef,
	}, nil
}

// RewriteAllowed lets a test push to a local bare repository standing in
// for GitHub through an insteadOf rule. Production never sets it; it is a
// variable, not an environment switch, so nothing outside the process can.
var RewriteAllowed = func(string) bool { return false }

var ghURL = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com(?::22)?/|git@github\.com:)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// RepoOfURL is the "owner/name" of a github.com remote URL, "" for any
// other host or shape.
func RepoOfURL(u string) string {
	m := ghURL.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// remoteRepos is the set of github repositories this repository's remotes
// fetch from.
func remoteRepos(ctx context.Context, env Env) map[string]bool {
	out, _ := env.git(ctx, "remote")
	set := map[string]bool{}
	for _, r := range strings.Fields(out) {
		if u, err := env.git(ctx, "remote", "get-url", r); err == nil {
			if repo := RepoOfURL(u); repo != "" {
				set[strings.ToLower(repo)] = true
			}
		}
	}
	return set
}

func validRef(ctx context.Context, env Env, name string) bool {
	return !strings.HasPrefix(name, "-") && env.gitOK(ctx, "check-ref-format", "refs/heads/"+name)
}

// wasBranchTip reports whether the local branch once pointed at sha: its
// reflog has it. That is what tells a branch rewritten after it was pushed
// from one whose remote name holds somebody else's commits.
func wasBranchTip(ctx context.Context, env Env, branch, sha string) bool {
	out, err := env.git(ctx, "log", "-g", "--format=%H", "refs/heads/"+branch, "--")
	if err != nil {
		return false
	}
	return slices.Contains(strings.Fields(out), sha)
}

// pushHookNames are the client-side hooks a `git push` runs: pre-push, and
// reference-transaction when it moves the remote-tracking ref.
var pushHookNames = []string{"pre-push", "reference-transaction"}

// pushHooks are the hooks git would run during the push, and a digest of
// them: an agent can write one into the repository, and it runs with the
// person's credential loaded, so the person is shown it and a change after
// the confirm refuses the act.
func pushHooks(ctx context.Context, env Env) (paths, sum string) {
	var found []string
	h := sha256.New()
	for _, name := range pushHookNames {
		p, err := env.git(ctx, "rev-parse", "--git-path", "hooks/"+name)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(env.Dir, p)
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		found = append(found, p)
		b, err := os.ReadFile(p)
		if err != nil {
			b = []byte("unreadable")
		}
		h.Write([]byte(p + "\x00"))
		h.Write(b)
	}
	if len(found) == 0 {
		return "", ""
	}
	return strings.Join(found, ", "), hex.EncodeToString(h.Sum(nil)[:8])
}

// steering is the configuration that decides where a push goes and how it
// authenticates; a change to any of it between the confirm and the act
// refuses the act.
var steering = []string{"remote.", "url.", "core.sshcommand", "core.hookspath", "core.askpass", "credential", "include", "http.", "push.", "gpg."}

func configSum(ctx context.Context, env Env, branch string) string {
	out, _ := env.git(ctx, "config", "--list", "--includes")
	var keep []string
	for line := range strings.SplitSeq(out, "\n") {
		k := strings.ToLower(line)
		for _, p := range steering {
			if strings.HasPrefix(k, p) || strings.HasPrefix(k, "branch."+strings.ToLower(branch)+".") {
				keep = append(keep, line)
				break
			}
		}
	}
	for _, v := range []string{"GIT_SSH", "GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_COUNT", "GUMMI_GH_CMD", "GH_CONFIG_DIR"} {
		keep = append(keep, v+"="+os.Getenv(v))
	}
	sort.Strings(keep)
	s := sha256.Sum256([]byte(strings.Join(keep, "\n")))
	return hex.EncodeToString(s[:8])
}
