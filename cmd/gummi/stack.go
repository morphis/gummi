package main

// `gummi stack` — the headless half of stacks. A stack is a chain of
// cards whose branches fork from one another; these verbs build one, read
// it back, and force the replay the board does on its own.
//
// Like `gummi deps`, this is a thin shell over the store and the engine:
// the rules live in internal/stack and internal/state, never here.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/stack"
	"github.com/morphis/gummi/internal/state"
)

// runStackNew implements `gummi stack new <bottom-card> [--name <name>]`:
// start a stack from the card that will sit at its bottom.
func runStackNew(args []string) error {
	fs := flag.NewFlagSet("stack new", flag.ContinueOnError)
	name := fs.String("name", "", "the stack's display name (default: the bottom card's slug)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi stack new <bottom-card> [--name <name>]")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("stack new takes exactly one card")
	}
	env, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	bottom, err := resolveDepsID(ctx, env.store, fs.Arg(0))
	if err != nil {
		return err
	}
	f, err := env.store.GetFeature(ctx, bottom)
	if err != nil {
		return err
	}
	if f.StackID != "" {
		return fmt.Errorf("%s is already in stack %s", f.ID, f.StackID)
	}
	label := *name
	if label == "" {
		label = f.Slug
	}
	id, err := domain.NewStackID(label, f.ID)
	if err != nil {
		return err
	}
	st := domain.Stack{ID: id, Name: label, Repo: f.Repo}
	if err := env.store.CreateStack(ctx, &st, time.Now()); err != nil {
		return err
	}
	if err := env.store.AddToStack(ctx, id, f.ID, 0); err != nil {
		return err
	}
	fmt.Printf("stack %s created with %s at the bottom\n", id, f.ID)
	return nil
}

// runStackAdd implements `gummi stack add <stack> <card> [--pos N]`.
func runStackAdd(args []string) error {
	fs := flag.NewFlagSet("stack add", flag.ContinueOnError)
	pos := fs.Int("pos", -1, "position in the stack, 0 at the bottom (default: the top)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi stack add <stack> <card> [--pos N]")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("stack add takes a stack and a card")
	}
	env, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	id := domain.StackID(fs.Arg(0))
	card, err := resolveDepsID(ctx, env.store, fs.Arg(1))
	if err != nil {
		return err
	}
	if err := env.store.AddToStack(ctx, id, card, *pos); err != nil {
		return err
	}
	fmt.Printf("%s added to %s\n", card, id)
	return printStack(ctx, env.store, id)
}

// runStackRm implements `gummi stack rm <card>`: take a card out of its
// stack, closing the gap so the cards above it move down a rung.
func runStackRm(args []string) error {
	fs := flag.NewFlagSet("stack rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi stack rm <card>") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("stack rm takes exactly one card")
	}
	env, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	card, err := resolveDepsID(ctx, env.store, fs.Arg(0))
	if err != nil {
		return err
	}
	f, err := env.store.GetFeature(ctx, card)
	if err != nil {
		return err
	}
	was := f.StackID
	if was == "" {
		return fmt.Errorf("%s is not in a stack", card)
	}
	if err := env.store.RemoveFromStack(ctx, card); err != nil {
		return err
	}
	fmt.Printf("%s removed from %s — the cards above it will be replayed onto their new base\n", card, was)
	return printStack(ctx, env.store, was)
}

// runStackMv implements `gummi stack mv <card> <pos>`.
func runStackMv(args []string) error {
	fs := flag.NewFlagSet("stack mv", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi stack mv <card> <position>") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("stack mv takes a card and a position")
	}
	var pos int
	if _, err := fmt.Sscanf(fs.Arg(1), "%d", &pos); err != nil {
		return fmt.Errorf("%q is not a position", fs.Arg(1))
	}
	env, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	card, err := resolveDepsID(ctx, env.store, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := env.store.MoveInStack(ctx, card, pos); err != nil {
		return err
	}
	f, err := env.store.GetFeature(ctx, card)
	if err != nil {
		return err
	}
	fmt.Printf("%s moved to position %d in %s\n", card, f.StackPos, f.StackID)
	return printStack(ctx, env.store, f.StackID)
}

// runStackList implements `gummi stack list [<stack>]`: every stack, or
// the members of one.
func runStackList(args []string) error {
	fs := flag.NewFlagSet("stack list", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi stack list [<stack>]") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := openDepsEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	if fs.NArg() == 1 {
		return printStack(ctx, env.store, domain.StackID(fs.Arg(0)))
	}
	stacks, err := env.store.ListStacks(ctx)
	if err != nil {
		return err
	}
	if len(stacks) == 0 {
		fmt.Println("no stacks — press T on a card in the board to stack the next one on it")
		return nil
	}
	for _, st := range stacks {
		members, merr := env.store.ListStackCards(ctx, st.ID)
		if merr != nil {
			return merr
		}
		repo := st.Repo
		if repo == "" {
			repo = "(default repo)"
		}
		fmt.Printf("%-24s %-20s %d card(s)  %s\n", st.ID, st.Name, len(members), repo)
	}
	return nil
}

// runStackRestack implements `gummi stack restack <stack|card>`: force
// the walk the board does on its own, to completion.
//
// It exists for the outside driver and for the reader who wants to see it
// happen, not because the board needs it: a stack replays itself.
func runStackRestack(args []string) error {
	fs := flag.NewFlagSet("stack restack", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi stack restack <stack|card>") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("stack restack takes a stack or a card in one")
	}
	env, cleanup, err := openStackEngine()
	if err != nil {
		return err
	}
	defer cleanup()
	ctx := context.Background()
	id, err := resolveStackID(ctx, env.store, fs.Arg(0))
	if err != nil {
		return err
	}
	// Walk to a fixed point rather than one step: a caller who asked for
	// this wants the stack settled when the command returns. The cap is
	// a safety net against a member that reports stale forever; the
	// policy moves at most one card per tick, so a stack of N settles in
	// N ticks and anything past 4N is a bug, not a long stack.
	const maxTicks = 64
	moved := 0
	for i := 0; i < maxTicks; i++ {
		res, terr := env.eng.StackTick(ctx, id)
		if terr != nil {
			return terr
		}
		if res.Conflict != nil {
			fmt.Printf("%s: replaying onto its base hit conflicts in %s\n",
				res.Restacked, strings.Join(res.Conflict.Files, ", "))
			fmt.Println("its branch is untouched — resolve them on the branch, then run this again")
			return fmt.Errorf("%s stopped on conflicts", id)
		}
		if res.Restacked != "" {
			moved++
			fmt.Printf("%s replayed onto its base\n", res.Restacked)
		}
		if !res.Again {
			break
		}
		if len(res.Actions) == 1 && res.Actions[0].Kind == stack.ActionWait {
			fmt.Printf("%s: waiting on %s — %s\n", id, res.Actions[0].Card, res.Actions[0].Reason)
			return nil
		}
	}
	if moved == 0 {
		fmt.Printf("%s is already settled — every card sits on its current base\n", id)
	}
	return printStack(ctx, env.store, id)
}

// resolveStackID accepts a stack id or a card in one.
func resolveStackID(ctx context.Context, store *state.Store, arg string) (domain.StackID, error) {
	if _, err := store.GetStack(ctx, domain.StackID(arg)); err == nil {
		return domain.StackID(arg), nil
	}
	card, err := resolveDepsID(ctx, store, arg)
	if err != nil {
		return "", fmt.Errorf("%q is neither a stack nor a card", arg)
	}
	f, err := store.GetFeature(ctx, card)
	if err != nil {
		return "", err
	}
	if f.StackID == "" {
		return "", fmt.Errorf("%s is not in a stack", card)
	}
	return f.StackID, nil
}

// printStack renders one stack bottom-first, each member with what it
// forks from — the chain read downwards.
func printStack(ctx context.Context, store *state.Store, id domain.StackID) error {
	st, err := store.GetStack(ctx, id)
	if err != nil {
		return err
	}
	members, err := store.ListStackCards(ctx, id)
	if err != nil {
		return err
	}
	base := "the checked-out branch"
	if len(members) > 0 && members[0].Base != "" {
		base = members[0].Base
	}
	fmt.Printf("%s (%s) on %s\n", st.ID, st.Name, base)
	// Build the light view the pure helpers read, so the CLI names the
	// same base the engine would resolve.
	v := stack.View{ID: st.ID, Name: st.Name, Repo: st.Repo}
	if len(members) > 0 {
		v.Base = members[0].Base
	}
	for _, f := range members {
		v.Members = append(v.Members, stack.Member{
			ID: f.ID, Pos: f.StackPos, Branch: f.BranchName(),
			HasTree: true,
			Landed:  f.LandedSHA != "" || f.Stage == domain.StageDone,
		})
	}
	for _, f := range members {
		from := stack.BaseFor(v, f.ID)
		if from == "" {
			from = "the checked-out branch"
		}
		mark := " "
		if f.LandedSHA != "" || f.Stage == domain.StageDone {
			mark = "✔"
		}
		fmt.Printf("  %s %d  %-8s %-26s %-12s ← %s\n",
			mark, f.StackPos, f.ID, f.BranchName(), f.Stage, from)
	}
	return nil
}

// stackEnv is the store plus an engine, for the one stack verb that has
// to do git: restack.
type stackEnv struct {
	store *state.Store
	eng   *engine.Engine
}

// openStackEngine wires a store, a pool and an engine, and teaches the
// pool how to resolve a card's base — the same wiring the board does at
// launch, without which a stacked card would fork from the checkout's
// HEAD instead of the card below it.
//
// A restack is deterministic git (rebase --onto), so it needs no coding
// agent: a bare engine will do when none is configured, exactly as bug
// ingestion does it.
func openStackEngine() (*stackEnv, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return nil, nil, err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return nil, nil, err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, nil, err
	}
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	eng, _, _, _ := newEngineFromEnv(store, pool, ws)
	if eng == nil {
		eng = engine.New(engine.Config{Store: store, Pool: pool, Workspace: ws})
	}
	pool.SetBaseLookup(eng.StackBaseFor)
	return &stackEnv{store: store, eng: eng}, func() { _ = store.Close() }, nil
}
