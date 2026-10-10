package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/repoview"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// The repositories view (GET /api/repos and its writes). A
// repository is git facts and the cards' claims on its branches; the
// board's part is knowing which cards have landed and which may be
// cleaned, so a read takes those from the loaded rows and everything
// else off the loop. Cleaning a card stays the card's own action — the
// page sends that — so nothing here removes a branch a card holds.

// repoFetchTimeout bounds one repository's fetch: a remote that never
// answers must not hold the request, or the page's button, forever.
const repoFetchTimeout = 2 * time.Minute

// webRepoHandles is what a repository read or write needs from the board.
type webRepoHandles struct {
	wt    *worktree.Pool
	store *state.Store
	root  string
	// landed and cleanable are the loaded rows' answers, by card.
	landed, cleanable map[domain.FeatureID]bool
}

func (m *Shell) webRepoHandles() (webRepoHandles, error) {
	if m.wt == nil || m.store == nil {
		return webRepoHandles{}, webErr(WebUnavailable, "this board has no repositories to read")
	}
	h := webRepoHandles{
		wt: m.wt, store: m.store, root: m.ws.Root,
		landed: map[domain.FeatureID]bool{}, cleanable: map[domain.FeatureID]bool{},
	}
	for _, r := range m.rows {
		if !r.Landed && r.F.LandedSHA == "" {
			continue
		}
		h.landed[r.F.ID] = true
		for _, a := range m.webActions(r) {
			if a.ID == "clean" {
				h.cleanable[r.F.ID] = true
			}
		}
	}
	return h, nil
}

// Repos is GET /api/repos. It rescans a discovered set first, so a clone
// made since launch is in the answer.
func (b *Bridge) Repos(ctx context.Context) (webapi.Repos, error) {
	b.RefreshRepos(ctx)
	return webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.Repos, error), error) {
		h, err := m.webRepoHandles()
		if err != nil {
			return nil, err
		}
		return h.read, nil
	})
}

func (h webRepoHandles) read(ctx context.Context) (webapi.Repos, error) {
	cards, err := h.store.ListFeatures(ctx)
	if err != nil {
		return webapi.Repos{}, webStoreErr(err)
	}
	out := webapi.Repos{Repos: []webapi.Repo{}, Discovered: h.wt.Discovering()}
	names := h.wt.Names()
	if h.wt.Known("") {
		names = append([]string{""}, names...)
	}
	for _, name := range names {
		out.Repos = append(out.Repos, h.repo(ctx, name, cards))
	}
	for name, paths := range h.wt.Ambiguous() {
		clash := webapi.RepoClash{Name: name}
		for _, p := range paths {
			clash.Paths = append(clash.Paths, h.rel(p))
		}
		out.Ambiguous = append(out.Ambiguous, clash)
	}
	sort.Slice(out.Ambiguous, func(i, j int) bool { return out.Ambiguous[i].Name < out.Ambiguous[j].Name })
	return out, nil
}

// rel names a path inside the workspace relative to its root.
func (h webRepoHandles) rel(p string) string {
	if rel, err := filepath.Rel(h.root, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

func (h webRepoHandles) repo(ctx context.Context, name string, cards []domain.Feature) webapi.Repo {
	out := webapi.Repo{Name: name, Branches: []webapi.RepoBranch{}}
	if root, ok := h.wt.RootForName(name); ok {
		out.Path = h.rel(root)
	}
	owners, forks := h.claims(name, cards, &out)
	mgr, err := h.wt.ManagerForName(ctx, name)
	if err != nil {
		out.Error = sanitize(err.Error())
		return out
	}
	st := mgr.Status(ctx)
	out.Base, out.Remote, out.Dirty, out.Fetched = st.Base, len(st.Remotes) > 0, st.Dirty, st.Fetched
	out.Origin, _ = worktree.MaskRemoteURL(st.Origin)
	for _, r := range mgr.RemoteList(ctx) {
		rr := webapi.RepoRemote{Name: r.Name, Tracking: r.Tracking}
		var secret, pushSecret bool
		rr.URL, secret = worktree.MaskRemoteURL(r.URL)
		rr.PushURL, pushSecret = worktree.MaskRemoteURL(r.PushURL)
		rr.Secret = secret || pushSecret
		out.Remotes = append(out.Remotes, rr)
	}
	out.RemoteBranches = mgr.RemoteBranches(ctx)
	branches, err := mgr.Branches(ctx)
	if err != nil {
		out.Error = sanitize(err.Error())
		return out
	}
	for _, r := range repoview.Rows(st.Base, branches, owners, forks) {
		br := webapi.RepoBranch{
			Name: r.Name, Group: string(r.Group), SHA: shortSHA(r.SHA), Subject: r.Subject, At: r.Committed,
			Upstream: r.Upstream, Remote: r.UpstreamRemote, Gone: r.UpstreamGone, Ahead: r.Ahead, Behind: r.Behind,
			AheadBase: r.AheadBase, BehindBase: r.BehindBase,
			Delete: string(r.Delete), Why: r.Why, Push: r.Push,
		}
		if r.Worktree != "" {
			br.Worktree = h.rel(r.Worktree)
			if r.Why != "" {
				// repoview names the absolute path; the page gets the
				// one relative to the workspace
				br.Why = "checked out at " + br.Worktree
			}
		}
		for _, id := range r.ForkedBy {
			br.ForkedBy = append(br.ForkedBy, string(id))
		}
		if o := r.Owner; o != nil {
			br.Card = &webapi.RepoBranchCard{ID: string(o.Card), Title: o.Title, Stage: string(o.Stage), Landed: o.Landed, Clean: o.Cleanable}
		}
		out.Branches = append(out.Branches, br)
	}
	return out
}

// claims are the cards' holds on repo's branches: the branch each card
// works on and the base each names. A goal is in no repository and keeps
// a branch in every one its cards are in, so its claim is made in all of
// them. It also counts repo's cards that are not done into out.
func (h webRepoHandles) claims(repo string, cards []domain.Feature, out *webapi.Repo) ([]repoview.Owner, []repoview.Fork) {
	var (
		owners []repoview.Owner
		forks  []repoview.Fork
	)
	for i := range cards {
		f := &cards[i]
		if !f.IsGoal() && f.Repo != repo {
			continue
		}
		if !f.IsGoal() && f.Stage != domain.StageDone {
			out.Cards++
		}
		if f.Base != "" && f.Stage != domain.StageDone {
			forks = append(forks, repoview.Fork{Branch: f.Base, Card: f.ID})
		}
		// a research card has no branch, and a session in the main
		// checkout works on whatever that has out
		if f.Kind == domain.KindResearch || f.MainCheckout {
			continue
		}
		owners = append(owners, repoview.Owner{
			Branch: f.BranchName(), Card: f.ID, Title: f.Title, Stage: f.Stage,
			Goal: f.IsGoal(), Adopted: f.Adopted(),
			Landed: h.landed[f.ID] || f.LandedSHA != "", Cleanable: h.cleanable[f.ID],
		})
	}
	return owners, forks
}

// repoManager resolves the repository a write named.
func (h webRepoHandles) repoManager(ctx context.Context, name string) (*worktree.Manager, error) {
	if !h.wt.Known(name) {
		return nil, webErr(WebNotFound, "no repository %s in this workspace", repoLabel(name))
	}
	mgr, err := h.wt.ManagerForName(ctx, name)
	if err != nil {
		return nil, webErr(WebConflict, "%s", sanitize(err.Error()))
	}
	return mgr, nil
}

// repoLabel names a repository in a sentence: the default has no name.
func repoLabel(name string) string {
	if name == "" {
		return "the default repository"
	}
	return name
}

// repoWrite runs one repository write off the loop and reports it on the
// board, reloading the rows: a moved base changes how far behind every
// card in the repository is.
func (b *Bridge) repoWrite(ctx context.Context, write func(context.Context, webRepoHandles) (string, error)) (WebOutcome, error) {
	var (
		h    webRepoHandles
		perr error
	)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { h, perr = m.webRepoHandles(); return nil }); err != nil {
		return WebOutcome{}, err
	}
	if perr != nil {
		return WebOutcome{}, perr
	}
	text, err := write(ctx, h)
	if err != nil {
		if _, ok := IsWebError(err); ok {
			return WebOutcome{}, err
		}
		return WebOutcome{}, webErr(WebConflict, "%s", sanitize(err.Error()))
	}
	b.RefreshRepos(ctx)
	return b.Await(ctx, func(*Shell) (tea.Cmd, error) {
		return func() tea.Msg { return noticeMsg{text: text, reload: true} }, nil
	})
}

// FetchRepos is POST /api/repos/fetch: `git fetch --all --prune` in the
// repository the request names, or in every one; naming a remote fetches
// that one alone. A read of the remotes and nothing else — no local
// branch moves, and nothing is pushed.
func (b *Bridge) FetchRepos(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.repoWrite(ctx, func(ctx context.Context, h webRepoHandles) (string, error) {
		names := []string{req.Repo}
		if req.All {
			names = h.wt.Names()
			if h.wt.Known("") {
				names = append([]string{""}, names...)
			}
		}
		var fetched, failed []string
		for _, name := range names {
			mgr, err := h.repoManager(ctx, name)
			if err != nil {
				if !req.All {
					return "", err
				}
				failed = append(failed, repoLabel(name)+": "+err.Error())
				continue
			}
			what := repoLabel(name)
			fctx, cancel := context.WithTimeout(ctx, repoFetchTimeout)
			if req.Remote != "" && !req.All {
				what = req.Remote
				err = mgr.FetchRemote(fctx, req.Remote)
			} else {
				err = mgr.Fetch(fctx)
			}
			cancel()
			switch {
			case errors.Is(err, worktree.ErrNoRemote) && req.All:
				// nothing to fetch from is not a failure of "fetch all"
			case err != nil:
				failed = append(failed, what+": "+firstLine(mgr.RedactRemotes(ctx, err.Error())))
			default:
				fetched = append(fetched, what)
			}
		}
		if len(failed) > 0 {
			return "", fmt.Errorf("fetch failed — %s", strings.Join(failed, "; "))
		}
		if len(fetched) == 0 {
			return "nothing to fetch: no repository has a remote", nil
		}
		return "fetched " + strings.Join(fetched, ", "), nil
	})
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FastForwardRepo is POST /api/repos/fastforward: move the repository's
// base up to its upstream as last fetched. Only ever a fast-forward, and
// refused over a dirty main checkout (worktree.Manager.FastForwardBase).
func (b *Bridge) FastForwardRepo(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.repoWrite(ctx, func(ctx context.Context, h webRepoHandles) (string, error) {
		mgr, err := h.repoManager(ctx, req.Repo)
		if err != nil {
			return "", err
		}
		base := mgr.Status(ctx).Base
		from, to, err := mgr.FastForwardBase(ctx)
		if err != nil {
			return "", err
		}
		if from == to {
			return fmt.Sprintf("%s is already up to date with its upstream", base), nil
		}
		return fmt.Sprintf("%s fast-forwarded %s → %s", base, shortSHA(from), shortSHA(to)), nil
	})
}

// DeleteRepoBranch is POST /api/repos/branches/delete: delete a branch
// no card holds. The claims are read again here, from the store, so a
// branch a card took since the page was drawn is refused; so is one a
// card forks from, and the repository's base.
func (b *Bridge) DeleteRepoBranch(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the branch")
	}
	return b.repoWrite(ctx, func(ctx context.Context, h webRepoHandles) (string, error) {
		mgr, err := h.repoManager(ctx, req.Repo)
		if err != nil {
			return "", err
		}
		cards, err := h.store.ListFeatures(ctx)
		if err != nil {
			return "", webStoreErr(err)
		}
		branches, err := mgr.Branches(ctx)
		if err != nil {
			return "", err
		}
		owners, forks := h.claims(req.Repo, cards, &webapi.Repo{})
		for _, r := range repoview.Rows(mgr.Status(ctx).Base, branches, owners, forks) {
			if r.Name != branch {
				continue
			}
			switch {
			case r.Group == repoview.GroupBase:
				return "", webErr(WebConflict, "%s is the repository's base", branch)
			case r.Owner != nil:
				return "", webErr(WebConflict, "%s is %s's branch — clean or delete the card instead", branch, r.Owner.Card)
			case r.Delete == repoview.DeleteNo:
				return "", webErr(WebConflict, "%s: %s", branch, r.Why)
			case r.Delete == repoview.DeleteConfirm && !req.Force:
				return "", webErr(WebConflict, "%s has %d commit(s) the base lacks", branch, r.AheadBase)
			}
			if err := mgr.DeleteBranchNamed(ctx, branch, req.Force); err != nil {
				return "", err
			}
			return fmt.Sprintf("deleted branch %s (was %s)", branch, shortSHA(r.SHA)), nil
		}
		return "", webErr(WebNotFound, "no branch %s in %s", branch, repoLabel(req.Repo))
	})
}

// remoteWrite runs one write to a repository's remotes: the request's
// repository resolved, its remote named.
func (b *Bridge) remoteWrite(ctx context.Context, req webapi.RepoRequest, write func(context.Context, *worktree.Manager, string) (string, error)) (WebOutcome, error) {
	remote := strings.TrimSpace(req.Remote)
	if remote == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the remote")
	}
	return b.repoWrite(ctx, func(ctx context.Context, h webRepoHandles) (string, error) {
		mgr, err := h.repoManager(ctx, req.Repo)
		if err != nil {
			return "", err
		}
		text, err := write(ctx, mgr, remote)
		return text, remoteErr(err)
	})
}

// remoteErr sorts a remote write's refusal into the status it answers
// with: a name or URL git would not take is the request's fault.
func remoteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worktree.ErrRemoteName), errors.Is(err, worktree.ErrRemoteURL):
		return webErr(WebBadRequest, "%s", sanitize(err.Error()))
	case errors.Is(err, worktree.ErrRemoteUnknown):
		return webErr(WebNotFound, "%s", sanitize(err.Error()))
	}
	return err
}

// AddRepoRemote is POST /api/repos/remotes/add: record a new remote in
// the repository's own config. Nothing is fetched until the person asks.
func (b *Bridge) AddRepoRemote(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.remoteWrite(ctx, req, func(ctx context.Context, mgr *worktree.Manager, remote string) (string, error) {
		url := strings.TrimSpace(req.URL)
		if err := mgr.AddRemote(ctx, remote, url); err != nil {
			return "", err
		}
		masked, _ := worktree.MaskRemoteURL(url)
		return fmt.Sprintf("added remote %s → %s", remote, masked), nil
	})
}

// RenameRepoRemote is POST /api/repos/remotes/rename. Branches tracking
// the remote keep tracking it under its new name.
func (b *Bridge) RenameRepoRemote(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.remoteWrite(ctx, req, func(ctx context.Context, mgr *worktree.Manager, remote string) (string, error) {
		to := strings.TrimSpace(req.NewName)
		if err := mgr.RenameRemote(ctx, remote, to); err != nil {
			return "", err
		}
		return fmt.Sprintf("renamed remote %s to %s", remote, to), nil
	})
}

// SetRepoRemoteURL is POST /api/repos/remotes/seturl.
func (b *Bridge) SetRepoRemoteURL(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.remoteWrite(ctx, req, func(ctx context.Context, mgr *worktree.Manager, remote string) (string, error) {
		url := strings.TrimSpace(req.URL)
		if err := mgr.SetRemoteURL(ctx, remote, url); err != nil {
			return "", err
		}
		masked, _ := worktree.MaskRemoteURL(url)
		return fmt.Sprintf("remote %s now points at %s", remote, masked), nil
	})
}

// RemoveRepoRemote is POST /api/repos/remotes/remove: forget a remote.
// Its tracking refs go and the branches on it track nothing afterwards;
// no local branch is touched.
func (b *Bridge) RemoveRepoRemote(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	return b.remoteWrite(ctx, req, func(ctx context.Context, mgr *worktree.Manager, remote string) (string, error) {
		if err := mgr.RemoveRemote(ctx, remote); err != nil {
			return "", err
		}
		return "removed remote " + remote, nil
	})
}

// SetRepoBranchUpstream is POST /api/repos/branches/upstream: which
// remote branch a local one tracks, or none. Any branch may be asked —
// a card's too: tracking is what the branch is compared with and where
// the person's own push goes, and no commit moves.
func (b *Bridge) SetRepoBranchUpstream(ctx context.Context, req webapi.RepoRequest) (WebOutcome, error) {
	branch, upstream := strings.TrimSpace(req.Branch), strings.TrimSpace(req.Upstream)
	if branch == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the branch")
	}
	return b.repoWrite(ctx, func(ctx context.Context, h webRepoHandles) (string, error) {
		mgr, err := h.repoManager(ctx, req.Repo)
		if err != nil {
			return "", err
		}
		if err := mgr.SetUpstream(ctx, branch, upstream); err != nil {
			return "", err
		}
		if upstream == "" {
			return branch + " no longer tracks a remote branch", nil
		}
		return fmt.Sprintf("%s now tracks %s", branch, upstream), nil
	})
}
