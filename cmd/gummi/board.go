package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// boardHost is a board built and ready to run: the store, worktree pool,
// engine and the TUI's model wired together, and the workspace's instance
// lock held. The TUI runs Shell under a terminal; `gummi web` runs the same
// Shell with no renderer (DESIGN §20.2). Building both through openBoard is
// what keeps "exactly as the TUI builds it" true rather than aspirational.
type boardHost struct {
	ws     state.Workspace
	store  *state.Store
	pool   *worktree.Pool
	engine *engine.Engine // nil when no agent backend could start
	shell  *ui.Shell

	closers []func()
}

// Close releases everything openBoard took, newest first: the engine and
// its agents, the hooks dispatcher, the store, and last the instance lock.
func (h *boardHost) Close() {
	for i := len(h.closers) - 1; i >= 0; i-- {
		h.closers[i]()
	}
	h.closers = nil
}

func (h *boardHost) onClose(fn func()) { h.closers = append(h.closers, fn) }

// boardOpts is what differs between the board's two hosts.
type boardOpts struct {
	// holder is recorded beside the instance lock (state.AcquireInstance):
	// which host this is and, for the web, where it serves.
	holder state.InstanceHolder
	// notifyDefault is the needs-attention signal when GUMMI_NOTIFY is
	// unset, and notifyOut where it is written.
	notifyDefault notify.Mode
	notifyOut     io.Writer
}

// openBoard builds the board in the current directory's workspace,
// creating the workspace lazily on first run. On error nothing is left
// held; on success the caller owns the host and must Close it.
func openBoard(o boardOpts) (_ *boardHost, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return nil, err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return nil, err
	}
	h := &boardHost{ws: ws}
	defer func() {
		if err != nil {
			h.Close()
		}
	}()
	// Hold the workspace's exclusive lock for the host's lifetime so a
	// second interactive board — a TUI or a web host — refuses to open
	// while one is up, naming the one that is. It does NOT block headless
	// run/resume/verify/merge/clean: those hold a per-card lock for the
	// card they drive, so independent cards run while the board is open.
	release, err := state.AcquireInstance(ws, o.holder)
	if err != nil {
		return nil, err
	}
	h.onClose(release)
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, err
	}
	h.store = store
	h.onClose(func() { _ = store.Close() })
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		return nil, err
	}
	h.pool = pool
	hookd := wireHooks(store, pool, ws)
	h.onClose(func() { hookd.Close() })
	// GUMMI_THEME selects the palette (dark|light|neon); default dark.
	th, _ := theme.ByName(cmp.Or(os.Getenv("GUMMI_THEME"), "dark"))
	shell := ui.NewShell(th, version())
	h.shell = shell
	shell.Attach(store, pool, ws)
	// One registry of per-card locks for the whole board, shared by the
	// engine (which holds a card while it drives it) and the board's own
	// git verbs (merge, rebase, clean, …). Sharing it is what lets a merge
	// on a card this board is already driving join the lock instead of
	// deadlocking against this very process.
	locks := state.NewCardLocks(ws)
	shell.AttachCardLocks(locks)

	// Profile names for the new-feature/bug/ingest dialogs come purely
	// from .gummi/profiles.yaml and are available whether or not any agent
	// backend can start — surface them unconditionally so the dialogs show
	// the real profiles even on a static board.
	shell.SetProfileNames(profileNames(ws))
	// The configured managed repositories feed the new-card forms' repo
	// selector; the default is always implicit, so only named repos here.
	shell.SetRepoNames(pool.Names())
	// Wire the agent engine best-effort: a missing/unstartable CLI just
	// leaves the board static (chat reports "no agent configured").
	if eng, _, cleanup, why := buildEngine(store, pool, ws, locks); eng != nil {
		h.engine = eng
		shell.AttachEngine(eng)
		h.onClose(cleanup)
	} else {
		shell.SetEngineUnavailable(why)
	}
	// layer-3 budget: new features get this credit envelope, drawn on by
	// every stage until it runs dry and a human gate offers a top-up.
	if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if float64(n) < domain.TurnReserveCredits {
				fmt.Fprintf(os.Stderr, "gummi: GUMMI_ENVELOPE=%d is below one agent turn (~%d credits); "+
					"stage budgets will be floored at a turn and overshoot the cap\n", n, int(domain.TurnReserveCredits))
			}
			shell.SetEnvelope(n)
		}
	}
	// needs-attention notification hook: GUMMI_NOTIFY=bell|desktop|off,
	// defaulting to the host's own choice when unset.
	notifyMode := o.notifyDefault
	if v := os.Getenv("GUMMI_NOTIFY"); v != "" {
		notifyMode = notify.ParseMode(v)
	}
	shell.SetNotifier(notify.New(notifyMode, o.notifyOut))
	// prlink/prpull need a real answer from GitHub, not the local
	// approximation openReviewThreads' nil fallback uses for the
	// warn-before-squash check — wire them to the same internal/pr
	// functions cmd/gummi/pr.go's link/comments verbs already call.
	shell.SetPRResolver(func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error) {
		return pr.Resolve(ctx, pr.GHBinary(), spec, repoDir, branch)
	})
	shell.SetPRThreadFetcher(func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return pr.FetchReviewThreads(ctx, pr.GHBinary(), ref)
	})
	shell.SetPRSquashMergeChecker(func(ctx context.Context, repo string) (bool, error) {
		return pr.RepoAllowsSquashMerge(ctx, pr.GHBinary(), repo)
	})
	// GUMMI_COPILOT_HINT=off hides the status-bar Copilot quota pill
	// (on by default; it needs an authenticated gh CLI to show anything).
	if strings.EqualFold(os.Getenv("GUMMI_COPILOT_HINT"), "off") {
		shell.SetCopilotHint(false)
	}
	// GUMMI_MOTION=off freezes every activity glyph in the UI to its
	// static first frame and stops the shared clock's tick loop from
	// ever starting (on by default).
	if strings.EqualFold(os.Getenv("GUMMI_MOTION"), "off") {
		shell.SetMotion(false)
	}
	return h, nil
}
