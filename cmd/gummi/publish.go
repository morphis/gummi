package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/state"
	"golang.org/x/term"
)

// spawnedMarker is set in the environment of everything a gummi process
// starts — agent backends and their shells included — so `gummi push` and
// `gummi pr create|update|ready|draft` can refuse when run from inside a
// session: publishing is a person's act (decision 25). underGummi is what
// this process inherited, read before run sets the marker for its own
// children.
const spawnedMarker = "GUMMI_SPAWNED"

var underGummi = os.Getenv(spawnedMarker) != ""

// stdinIsTerminal reports a person at the keyboard to answer the confirm.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) } //nolint:gosec // a file descriptor fits an int

// publishError is a publish refusal in the CLI's words: the code first, so
// a script can match it.
type publishError struct{ e *publish.Error }

func (p publishError) Error() string { return "refused (" + string(p.e.Code) + "): " + p.e.Error() }

// runPublish runs one publish act on a card: it resolves and prints the
// facts and the exact commands, then runs them once the person confirms —
// at the terminal, or by passing --yes=<fingerprint> of the facts they read.
func runPublish(fl cliFlags, act publish.Act, args []string) error {
	verb := "push"
	if act != publish.ActPush {
		verb = "pr " + string(act)
	}
	if underGummi {
		return publishError{&publish.Error{Code: publish.CodeAgentSession,
			Text: "publishing is a person's act and this runs inside a gummi session",
			Fix:  "run it from your own terminal, the board or the web page"}}
	}
	idArg, err := oneID(verb, args)
	if err != nil {
		return err
	}
	// each verb declares only the flags it takes (bindPublishFlags)
	str := func(name string) string {
		if fl.fs.Lookup(name) == nil {
			return ""
		}
		return fl.String(name)
	}
	req := publish.Request{Act: act, Title: str("title"), Draft: fl.fs.Lookup("draft") != nil && fl.Bool("draft")}
	if path := str("body-file"); path != "" {
		b, err := readBody(path)
		if err != nil {
			return err
		}
		req.Body = string(b)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	pool, err := newPool(ctx, wsRoot, defaultRoot, named, store, true)
	if err != nil {
		return err
	}
	f, err := resolveFeatureID(ctx, store, idArg)
	if err != nil {
		return err
	}
	// holding the card's lock is what keeps an agent from moving the
	// branch between the facts and the push; a card an agent holds is
	// refused here
	release, err := state.AcquireLock(ws.CardLockFile(f.ID))
	if err != nil {
		return publishError{&publish.Error{Code: publish.CodeBusy, Text: err.Error(), Fix: "wait for its turn to end"}}
	}
	defer release()
	mgr, err := pool.ManagerFor(ctx, &f)
	if err != nil {
		return err
	}
	if r := str("remote"); r != "" {
		if err := mgr.SetPushRemote(ctx, &f, r); err != nil {
			return err
		}
	}
	anns, err := store.ListDiffAnnotations(ctx, f.ID)
	if err != nil {
		return err
	}
	in := publish.InputFor(ctx, mgr, &f, false, anns)
	env := publish.Env{GH: pr.GHBinary()}
	fx, perr := publish.Resolve(ctx, env, mgr, in)
	if perr != nil {
		return publishError{perr}
	}
	if act == publish.ActCreate && strings.TrimSpace(req.Title) == "" {
		t, b := publish.Text(ctx, env, mgr, &f, fx)
		req.Title = t
		if req.Body == "" {
			req.Body = b
		}
	}
	plan, perr := publish.PlanFor(fx, req)
	if perr != nil {
		return publishError{perr}
	}
	jsonOut := fl.Bool("json")
	if !jsonOut {
		printFacts(os.Stdout, &f, fx, plan, req)
	}
	yes := fl.String("yes")
	switch {
	case yes != "":
		req.Fingerprint = yes
	case !jsonOut && str("body-file") != "-" && stdinIsTerminal():
		fmt.Print("\n" + confirmQuestion(act) + " [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return fmt.Errorf("not confirmed; nothing was published")
		}
		req.Fingerprint = fx.Fingerprint()
	default:
		if jsonOut {
			return json.NewEncoder(os.Stdout).Encode(publishJSON{Facts: fx, Plan: plan, Fingerprint: fx.Fingerprint()})
		}
		return publishError{&publish.Error{Code: publish.CodeConfirmationNeeded,
			Text: "there is no terminal to confirm at",
			Fix:  "read the facts above and run again with --yes=" + fx.Fingerprint()}}
	}
	res, perr := publish.Do(ctx, env, mgr, in, req, func(ctx context.Context, ref domain.PullRequestRef) error {
		return store.SetPullRequest(ctx, f.ID, ref)
	})
	if perr != nil {
		return publishError{perr}
	}
	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(publishJSON{Facts: fx, Plan: plan, Fingerprint: fx.Fingerprint(), Result: &res})
	}
	printResult(os.Stdout, fx, res)
	if res.LinkErr != "" {
		return fmt.Errorf("%s", res.LinkErr)
	}
	return nil
}

type publishJSON struct {
	Facts       publish.Facts   `json:"facts"`
	Plan        publish.Plan    `json:"plan"`
	Fingerprint string          `json:"fingerprint"`
	Result      *publish.Result `json:"result,omitempty"`
}

func readBody(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path) //nolint:gosec // a path the person named
}

func confirmQuestion(act publish.Act) string {
	switch act {
	case publish.ActPush:
		return "Push?"
	case publish.ActCreate:
		return "Push and open the pull request?"
	case publish.ActUpdate:
		return "Update the pull request?"
	case publish.ActReady:
		return "Mark it ready for review?"
	}
	return "Turn it back into a draft?"
}

func printFacts(w io.Writer, f *domain.Feature, fx publish.Facts, p publish.Plan, req publish.Request) {
	fmt.Fprintf(w, "%s  %s\n", f.ID, clean(f.Title))
	fmt.Fprintf(w, "  %s\n\n", p.Summary)
	row := func(k, v string) { fmt.Fprintf(w, "  %-11s %s\n", k, v) }
	row("tip", domain.ShortRev(fx.Tip)+"  "+clean(fx.TipSubject)+fmt.Sprintf("  (%d ahead of %s)", fx.Ahead, fx.Base))
	row("push", fx.Remote+" → "+fx.PushURL+"  ("+string(fx.Push)+", from "+fx.RemoteHow+")")
	row("head", fx.HeadRef())
	row("base", fx.BaseRepo+":"+fx.Base)
	if fx.PR != nil {
		state := strings.ToLower(fx.PR.State)
		if fx.PR.Draft {
			state += ", draft"
		}
		row("pr", fmt.Sprintf("#%d %s  (GitHub head %s)", fx.PR.Number, state, domain.ShortRev(fx.PR.HeadSHA)))
	}
	if p.Act == publish.ActCreate {
		if p.Draft {
			why := "asked for"
			if fx.ReadyWhy != "" {
				why = fx.ReadyWhy
			}
			row("state", "draft — "+why)
		} else {
			row("state", "ready for review")
		}
		row("title", clean(req.Title))
	}
	row("gh", fx.GH)
	if fx.Hook != "" {
		row("pre-push", fx.Hook+"  — runs with your credential; read it first")
	} else {
		row("pre-push", "none")
	}
	row("facts", fx.Fingerprint())
	fmt.Fprintln(w)
	for _, c := range p.Commands {
		fmt.Fprintln(w, "  "+c)
	}
}

func printResult(w io.Writer, fx publish.Facts, res publish.Result) {
	if res.Pushed != "" {
		fmt.Fprintf(w, "✓ pushed %s → %s/%s\n", domain.ShortRev(res.Pushed), fx.Remote, fx.RemoteBranch)
	}
	switch res.Act {
	case publish.ActCreate:
		state := "ready"
		if res.Draft {
			state = "draft"
		}
		fmt.Fprintf(w, "✓ opened %s (%s), linked to %s\n", res.PR.URL, state, fx.Card)
	case publish.ActUpdate:
		fmt.Fprintf(w, "✓ updated %s\n", res.PR.URL)
	case publish.ActReady:
		fmt.Fprintf(w, "✓ %s is ready for review\n", res.PR.URL)
	case publish.ActDraft:
		fmt.Fprintf(w, "✓ %s is a draft again\n", res.PR.URL)
	}
}
