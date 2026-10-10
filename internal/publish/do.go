package publish

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
)

// Request is one act as a person asked for it.
type Request struct {
	Act Act
	// Fingerprint is Facts.Fingerprint of the facts the person was shown.
	// An act runs only when the facts read again digest the same.
	Fingerprint string
	Title, Body string
	// Draft opens the PR as a draft (create), or lets a push of an
	// unverified tip turn a ready PR back into a draft first (push, update).
	Draft bool
}

// Plan says whether req may run on fx and what it will do: the draft state
// a create ends in, and the exact commands. It is pure; the faces show its
// lines in the confirm and Do runs exactly them.
type Plan struct {
	Act Act
	// Push is false when the remote already has the tip.
	Push bool
	// Draft is the state a created PR opens in; ToDraft turns a ready PR
	// back into a draft before an unverified push.
	Draft, ToDraft bool
	// Edit is a create's or update's title/body; EditTitle and EditBody
	// say which an update carries.
	Edit, EditTitle, EditBody bool
	Commands                  []string
	// Summary is the one sentence a confirm leads with.
	Summary string
}

// PlanFor checks req against fx.
func PlanFor(fx Facts, req Request) (Plan, *Error) {
	p := Plan{Act: req.Act, Push: fx.Push != PushUpToDate}
	needPR := func() *Error {
		switch {
		case fx.PR == nil:
			return fail(CodeNoPR, "this card has no linked pull request", "open one first")
		case fx.PR.State == "MERGED":
			return fail(CodePRMerged, fmt.Sprintf("PR #%d was merged on GitHub", fx.PR.Number), "mark the card landed or unlink the PR")
		case fx.PR.State == "CLOSED":
			return fail(CodePRClosed, fmt.Sprintf("PR #%d was closed without merging", fx.PR.Number), "unlink it to open a new one")
		}
		return nil
	}
	// a ready PR never silently gains commits no check ran on
	guardReady := func() *Error {
		if fx.PR.Open() && !fx.PR.Draft && fx.ReadyWhy != "" && p.Push {
			if !req.Draft {
				return fail(CodeNotVerified, fmt.Sprintf("PR #%d is ready for review and %s", fx.PR.Number, fx.ReadyWhy), "verify first, or push and return it to draft")
			}
			p.ToDraft = true
		}
		return nil
	}
	switch req.Act {
	case ActPush:
		if !p.Push {
			return p, fail(CodeNothingToPublish, fx.Remote+"/"+fx.RemoteBranch+" already has "+domain.ShortRev(fx.Tip), "")
		}
		if fx.PR != nil {
			if e := needPR(); e != nil {
				return p, e
			}
			if e := guardReady(); e != nil {
				return p, e
			}
		} else if fx.OpenPR > 0 {
			// an open PR this card does not link would gain the commits
			// with no floor asked of it
			e := fail(CodePRExists, fmt.Sprintf("PR #%d is open for %s and not linked to this card; a push would add to it unchecked", fx.OpenPR, fx.HeadRef()), "link it first")
			e.PR = fx.OpenPR
			return p, e
		}
		p.Summary = fmt.Sprintf("Push %s (%s) to %s.", fx.Branch, plural(fx.Ahead, "commit"), fx.HeadRepo)
	case ActCreate:
		switch {
		case fx.PR != nil && !fx.PR.Open():
			return p, needPR()
		case fx.PR != nil:
			return p, fail(CodePRExists, fmt.Sprintf("this card is already linked to PR #%d", fx.PR.Number), "")
		case fx.OpenPR > 0:
			e := fail(CodePRExists, fmt.Sprintf("PR #%d is already open for %s", fx.OpenPR, fx.HeadRef()), "link it instead")
			e.PR = fx.OpenPR
			return p, e
		case strings.TrimSpace(req.Title) == "":
			return p, fail(CodeConfirmationNeeded, "a pull request needs a title", "")
		}
		p.Draft = req.Draft || fx.ReadyWhy != ""
		p.Edit = true
		state := "a PR"
		if p.Draft {
			state = "a draft PR"
		}
		p.Summary = fmt.Sprintf("Push %s (%s) to %s and open %s into %s %s.", fx.Branch, plural(fx.Ahead, "commit"), fx.HeadRepo, state, fx.BaseRepo, fx.Base)
	case ActUpdate:
		if e := needPR(); e != nil {
			return p, e
		}
		p.EditTitle, p.EditBody = strings.TrimSpace(req.Title) != "", strings.TrimSpace(req.Body) != ""
		p.Edit = p.EditTitle || p.EditBody
		if !p.Push && !p.Edit {
			return p, fail(CodeNothingToPublish, fmt.Sprintf("PR #%d already has %s and there is no new title or body", fx.PR.Number, domain.ShortRev(fx.Tip)), "")
		}
		if e := guardReady(); e != nil {
			return p, e
		}
		p.Summary = fmt.Sprintf("Update PR #%d in %s.", fx.PR.Number, fx.PR.Repo)
	case ActReady:
		if e := needPR(); e != nil {
			return p, e
		}
		switch {
		case !fx.PR.Draft:
			return p, fail(CodeAlreadyReady, fmt.Sprintf("PR #%d is already ready for review", fx.PR.Number), "")
		case fx.ReadyWhy != "":
			return p, fail(CodeNotVerified, fx.ReadyWhy, fx.ReadyFix)
		case fx.PR.HeadSHA != fx.Tip:
			return p, fail(CodeHeadElsewhere, fmt.Sprintf("GitHub's head for PR #%d is %s, not the verified tip %s", fx.PR.Number, domain.ShortRev(fx.PR.HeadSHA), domain.ShortRev(fx.Tip)), "push the card's branch first, or fetch what was pushed and verify it")
		}
		p.Push = false
		p.Summary = fmt.Sprintf("Mark PR #%d ready for review.", fx.PR.Number)
	case ActDraft:
		if e := needPR(); e != nil {
			return p, e
		}
		if fx.PR.Draft {
			return p, fail(CodeAlreadyDraft, fmt.Sprintf("PR #%d is already a draft", fx.PR.Number), "")
		}
		p.Push = false
		p.Summary = fmt.Sprintf("Turn PR #%d back into a draft.", fx.PR.Number)
	default:
		return p, fail(CodeFailed, "unknown act "+strconv.Quote(string(req.Act)), "")
	}
	if p.ToDraft {
		p.Summary += fmt.Sprintf(" PR #%d returns to draft first: %s.", fx.PR.Number, fx.ReadyWhy)
	}
	p.Commands = commands(fx, p)
	return p, nil
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return strconv.Itoa(n) + " " + w + "s"
}

func (fx Facts) pushArgs() []string {
	args := []string{"push", "--porcelain"}
	if fx.Push == PushLease {
		args = append(args, "--force-with-lease=refs/heads/"+fx.RemoteBranch+":"+fx.RemoteTip)
	}
	return append(args, fx.Remote, fx.Tip+":refs/heads/"+fx.RemoteBranch)
}

func commands(fx Facts, p Plan) []string {
	var out []string
	num := ""
	if fx.PR != nil {
		num = strconv.Itoa(fx.PR.Number)
	}
	if p.ToDraft {
		out = append(out, "gh pr ready "+num+" --repo "+fx.PR.Repo+" --undo")
	}
	if p.Push {
		out = append(out, "git "+strings.Join(fx.pushArgs(), " "))
	}
	switch p.Act {
	case ActCreate:
		c := "gh pr create --repo " + fx.BaseRepo + " --head " + fx.HeadRef() + " --base " + fx.Base + " --title=<title> --body-file -"
		if p.Draft {
			c += " --draft"
		}
		out = append(out, c)
	case ActUpdate:
		if p.Edit {
			c := "gh pr edit " + num + " --repo " + fx.PR.Repo
			if p.EditTitle {
				c += " --title=<title>"
			}
			if p.EditBody {
				c += " --body-file -"
			}
			out = append(out, c)
		}
	case ActReady:
		out = append(out, "gh pr ready "+num+" --repo "+fx.PR.Repo)
	case ActDraft:
		out = append(out, "gh pr ready "+num+" --repo "+fx.PR.Repo+" --undo")
	}
	return out
}

// Result is what an act did.
type Result struct {
	Act    Act
	Pushed string // the SHA pushed, "" when nothing was
	// Repo is the repository Pushed went to.
	Repo string
	PR   domain.PullRequestRef
	// Draft is a PR opened as a draft; ToDraft a ready PR returned to
	// draft ahead of an unverified push.
	Draft, ToDraft bool
	// LinkErr is a PR opened that could not be linked: the PR exists, and
	// the face says so rather than reporting the act failed.
	LinkErr string
}

// Link records a PR as the card's (state.Store.SetPullRequest).
type Link func(ctx context.Context, ref domain.PullRequestRef) error

// Do runs req: it resolves the facts again under the caller's card lock,
// refuses when they no longer digest as req.Fingerprint, and runs exactly
// the plan's commands. A face calls it only for an act a person confirmed.
func Do(ctx context.Context, env Env, repo Repo, in Input, req Request, link Link) (Result, *Error) {
	fx, ferr := Resolve(ctx, env, repo, in)
	if ferr != nil {
		return Result{}, ferr
	}
	switch {
	case req.Fingerprint == "":
		return Result{}, fail(CodeConfirmationNeeded, "publishing needs the confirmed facts' fingerprint", "review the facts and confirm them")
	case req.Fingerprint != fx.Fingerprint():
		return Result{}, fail(CodeFactsChanged, "something changed since you confirmed (the branch, the remote, its configuration, a hook or the PR); nothing was published", "review again")
	}
	p, perr := PlanFor(fx, req)
	if perr != nil {
		return Result{}, perr
	}
	tree, err := repo.Path(in.Card.F)
	if err != nil {
		return Result{}, AsError(err)
	}
	env.Dir, env.GH = tree, fx.GH
	res := Result{Act: req.Act, Draft: p.Draft}
	if p.ToDraft {
		if _, err := env.gh(ctx, nil, "pr", "ready", strconv.Itoa(fx.PR.Number), "--repo", fx.PR.Repo, "--undo"); err != nil {
			return res, AsError(err)
		}
		res.ToDraft = true
	}
	if p.Push {
		// the branch must still be the tip that was shown: the push names
		// the SHA, so a commit made since is never published unseen
		if now, _ := env.git(ctx, "rev-parse", "refs/heads/"+fx.Branch); now != fx.Tip {
			return res, fail(CodeBranchMoved, "the branch moved from "+domain.ShortRev(fx.Tip)+" to "+domain.ShortRev(now)+" since you confirmed", "review again")
		}
		if stdout, stderr, err := run(ctx, tree, nil, "git", fx.pushArgs()...); err != nil {
			return res, after(res, fx, pushError(stderr+"\n"+stdout, err, strings.Contains(fx.Hook, "pre-push")))
		}
		res.Pushed, res.Repo = fx.Tip, fx.HeadRepo
		trackIfUntracked(ctx, env, fx)
		if fx.PR != nil {
			// the link's head follows what was pushed (§22.5), whatever
			// becomes of the rest of the act
			res.PR = domain.PullRequestRef{Repo: fx.PR.Repo, Number: fx.PR.Number, URL: fx.PR.URL, HeadSHA: fx.Tip}
			if lerr := link(ctx, res.PR); lerr != nil {
				res.LinkErr = "the push succeeded but the link's head could not be updated: " + lerr.Error()
			}
		}
	}
	switch req.Act {
	case ActCreate:
		args := []string{"pr", "create", "--repo", fx.BaseRepo, "--head", fx.HeadRef(), "--base", fx.Base, "--title=" + strings.TrimSpace(req.Title), "--body-file", "-"}
		if p.Draft {
			args = append(args, "--draft")
		}
		out, err := env.gh(ctx, []byte(req.Body), args...)
		if err != nil {
			return res, after(res, fx, AsError(err))
		}
		ref, rerr := refFromCreate(out, fx)
		if rerr != nil {
			return res, after(res, fx, rerr)
		}
		res.PR = ref
		if lerr := link(ctx, ref); lerr != nil {
			res.LinkErr = fmt.Sprintf("PR #%d was opened (%s) but could not be linked to %s: %v — link it with `gummi pr link %s %d`", ref.Number, ref.URL, fx.Card, lerr, fx.Card, ref.Number)
		}
	case ActUpdate:
		if p.Edit {
			args := []string{"pr", "edit", strconv.Itoa(fx.PR.Number), "--repo", fx.PR.Repo}
			if t := strings.TrimSpace(req.Title); t != "" {
				args = append(args, "--title="+t)
			}
			var body []byte
			if strings.TrimSpace(req.Body) != "" {
				args, body = append(args, "--body-file", "-"), []byte(req.Body)
			}
			if _, err := env.gh(ctx, body, args...); err != nil {
				return res, after(res, fx, AsError(err))
			}
		}
	case ActReady:
		if _, err := env.gh(ctx, nil, "pr", "ready", strconv.Itoa(fx.PR.Number), "--repo", fx.PR.Repo); err != nil {
			return res, after(res, fx, AsError(err))
		}
	case ActDraft:
		if _, err := env.gh(ctx, nil, "pr", "ready", strconv.Itoa(fx.PR.Number), "--repo", fx.PR.Repo, "--undo"); err != nil {
			return res, after(res, fx, AsError(err))
		}
	}
	if fx.PR != nil && res.Pushed == "" {
		res.PR = domain.PullRequestRef{Repo: fx.PR.Repo, Number: fx.PR.Number, URL: fx.PR.URL, HeadSHA: fx.PR.HeadSHA}
	}
	return res, nil
}

// after words a failure that came after part of the act had already
// happened: the person is told what did, since GitHub already has it. The
// Result returned beside the error says the same to the face, which
// records it.
func after(res Result, fx Facts, e *Error) *Error {
	var done []string
	if res.ToDraft {
		done = append(done, fmt.Sprintf("PR #%d was returned to draft", fx.PR.Number))
	}
	if res.Pushed != "" {
		done = append(done, domain.ShortRev(res.Pushed)+" was pushed to "+fx.HeadRepo)
	}
	if len(done) == 0 {
		return e
	}
	out := *e
	out.Text = e.Text + " (before that, " + strings.Join(done, " and ") + ")"
	return &out
}

// Partial reports a Result that came back beside an error and still
// changed something on GitHub: the face records it.
func (r Result) Partial() bool { return r.Pushed != "" || r.ToDraft }

// trackIfUntracked records the push target as the branch's upstream when
// it tracks nothing yet, so git's own tools see where it lives. An existing
// upstream (a triangular workflow's upstream/main) is never overwritten.
func trackIfUntracked(ctx context.Context, env Env, fx Facts) {
	if env.gitOK(ctx, "config", "--get", "branch."+fx.Branch+".remote") {
		return
	}
	if env.gitOK(ctx, "config", "branch."+fx.Branch+".remote", fx.Remote) {
		_, _ = env.git(ctx, "config", "branch."+fx.Branch+".merge", "refs/heads/"+fx.RemoteBranch)
	}
}

func refFromCreate(out string, fx Facts) (domain.PullRequestRef, *Error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	u := strings.TrimSpace(lines[len(lines)-1])
	repo, n, err := pr.ParsePullRefURL(u)
	if err != nil {
		return domain.PullRequestRef{}, fail(CodeLinkFailed, "gh pr create answered "+strconv.Quote(u)+", not a pull request URL; look for the PR on GitHub and link it", "")
	}
	return domain.PullRequestRef{Repo: repo, Number: n, URL: u, HeadSHA: fx.Tip}, nil
}

// DefaultText is the title and body a PR starts from: the card's title and
// the branch's commit subjects, shown and editable, never sent unread.
func DefaultText(f *domain.Feature, subjects []string) (title, body string) {
	title = f.Title
	if len(subjects) == 1 {
		title = subjects[0]
	}
	var b strings.Builder
	if len(subjects) > 1 {
		for _, s := range subjects {
			b.WriteString("- " + s + "\n")
		}
	}
	return title, b.String()
}

// Text is the title and body a new PR starts from for fx (DefaultText over
// the branch's own commit subjects).
func Text(ctx context.Context, env Env, repo Repo, f *domain.Feature, fx Facts) (title, body string) {
	var subjects []string
	if tree, err := repo.Path(f); err == nil {
		env.Dir = tree
		if out, err := env.git(ctx, "log", "--reverse", "--format=%s", fx.Base+".."+fx.Tip); err == nil && out != "" {
			subjects = strings.Split(out, "\n")
		}
	}
	return DefaultText(f, subjects)
}
