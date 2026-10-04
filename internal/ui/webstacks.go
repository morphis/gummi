package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/stack"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// Stacks on the web face (DESIGN §18). A stack is store rows and git
// facts; the board's part is the tick. So a read is the engine's snapshot
// — the same one the board's stack marker is drawn from
// (stackRowsForFeatures) — and a write is the store's own verb followed by
// a tick of the stack it touched, which is what replays any card whose
// base the write moved.

// Stacks is GET /api/stacks: every stack, members bottom first.
func (b *Bridge) Stacks(ctx context.Context) (webapi.Stacks, error) {
	return webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.Stacks, error), error) {
		store, eng, release, err := m.webStackHandles()
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (webapi.Stacks, error) {
			defer release()
			stacks, err := store.ListStacks(ctx)
			if err != nil {
				return webapi.Stacks{}, err
			}
			out := webapi.Stacks{Stacks: make([]webapi.Stack, 0, len(stacks))}
			for _, st := range stacks {
				view, err := eng.StackSnapshot(ctx, st.ID)
				if err != nil {
					return webapi.Stacks{}, err
				}
				out.Stacks = append(out.Stacks, webStackReplay(webStack(view), eng))
			}
			return out, nil
		}, nil
	})
}

// Stack is GET /api/stacks/{id}.
func (b *Bridge) Stack(ctx context.Context, id string) (webapi.Stack, error) {
	return webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.Stack, error), error) {
		_, eng, release, err := m.webStackHandles()
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (webapi.Stack, error) {
			defer release()
			view, err := eng.StackSnapshot(ctx, domain.StackID(id))
			if err != nil {
				return webapi.Stack{}, webStoreErr(err)
			}
			return webStackReplay(webStack(view), eng), nil
		}, nil
	})
}

// webStackHandles is the store and an engine to read stacks through: the
// board's, or the transient agent-less one a board with no agent reads
// them with (stackEngine) — a stack is a store-and-git fact.
func (m *Shell) webStackHandles() (*state.Store, *engine.Engine, func(), error) {
	eng, release := m.stackEngine()
	if eng == nil {
		return nil, nil, nil, webErr(WebUnavailable, "this board has no store to keep stacks in")
	}
	return m.store, eng, release, nil
}

// webStack projects one stack's snapshot, bottom first.
func webStack(view engine.StackView) webapi.Stack {
	snap := view.Snapshot
	out := webapi.Stack{
		ID: string(snap.ID), Name: snap.Name, Repo: snap.Repo, Base: snap.Base,
		Members: make([]webapi.StackMember, 0, len(snap.Members)),
	}
	for _, mem := range snap.Members {
		sm := webapi.StackMember{
			ID: string(mem.ID), Pos: mem.Pos, Branch: mem.Branch, Tree: mem.HasTree,
			Landed: mem.Landed, Stale: mem.Stale, Running: mem.Running, Dirty: mem.Dirty,
		}
		if f, ok := view.Card(mem.ID); ok {
			sm.Title, sm.Stage, sm.Adopted = f.Title, string(f.Stage), f.Adopted()
			// the policy counts a finished card as landed so nothing forks
			// from it again; the page must not tell a handed-off card's
			// reader that its branch reached the base
			if f.HandedOff() && f.LandedSHA == "" {
				sm.Landed, sm.HandedOff = false, true
			}
		}
		if below, ok := stack.BelowDeclared(snap, mem.ID); ok {
			sm.Below = string(below)
		}
		if blocker, ok := stack.LandBlocker(snap, mem.ID); ok {
			sm.Blocker = string(blocker)
		}
		out.Members = append(out.Members, sm)
	}
	return out
}

// webStackReplay adds the stack's latest replay walk to its projection:
// the cards the board's ticks (or a restack) moved and the pushes they
// now need, so a page opened after an automatic replay still shows them.
func webStackReplay(st webapi.Stack, eng *engine.Engine) webapi.Stack {
	if eng == nil {
		return st
	}
	r, ok := eng.LastReplay(domain.StackID(st.ID))
	if !ok {
		return st
	}
	return withStackReplay(st, r)
}

func withStackReplay(st webapi.Stack, r engine.StackReplay) webapi.Stack {
	st.Push = nil
	for _, p := range r.Push {
		if p != "" {
			st.Push = append(st.Push, p)
		}
	}
	st.Replayed = make([]string, 0, len(r.Cards))
	for _, c := range r.Cards {
		st.Replayed = append(st.Replayed, string(c))
	}
	st.ReplayedAt = r.At
	return st
}

// webStoreErr classes a store refusal: a missing row is not found, any
// other refusal (a card in another stack, another repository, a kind with
// no branch) is the board's state saying no.
func webStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := IsWebError(err); ok {
		return err
	}
	if errors.Is(err, state.ErrNotFound) {
		return webErr(WebNotFound, "%s", sanitize(err.Error()))
	}
	return webErr(WebConflict, "%s", sanitize(err.Error()))
}

// stackWrite runs one store edit off the loop, then ticks the stack it
// touched and reloads the board — what the TUI does after it stacks a
// card (cardCreated's stack tick).
func (b *Bridge) stackWrite(ctx context.Context, edit func(context.Context, *state.Store) (domain.StackID, string, error)) (WebOutcome, error) {
	return b.stackWriteTick(ctx, true, edit)
}

// stackWriteTick is stackWrite, with the tick left out when tick is false:
// a deleted stack has nothing left to walk, and ticking it would only
// report that it is gone.
func (b *Bridge) stackWriteTick(ctx context.Context, tick bool, edit func(context.Context, *state.Store) (domain.StackID, string, error)) (WebOutcome, error) {
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		return WebOutcome{}, err
	}
	if store == nil {
		return WebOutcome{}, webErr(WebUnavailable, "this board has no store to keep stacks in")
	}
	id, text, err := edit(ctx, store)
	if err != nil {
		return WebOutcome{}, webStoreErr(err)
	}
	out, err := b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		if tick {
			m.queueStackTick(id)
		}
		return func() tea.Msg { return noticeMsg{text: text, reload: true} }, nil
	})
	out.ID = string(id)
	return out, err
}

// CreateStack is POST /api/stacks: start a stack from its bottom card, as
// `gummi stack new` does, and put any further cards on top of it in order.
func (b *Bridge) CreateStack(ctx context.Context, req webapi.StackRequest) (WebOutcome, error) {
	bottom := webID(req.Card)
	if bottom == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the card at the bottom of the stack")
	}
	return b.stackWrite(ctx, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		st, err := store.StartStack(ctx, bottom, strings.TrimSpace(req.Name), time.Now())
		if err != nil {
			return "", "", err
		}
		for _, c := range req.Cards {
			if err := store.AddToStack(ctx, st.ID, webID(c), -1); err != nil {
				return st.ID, "", err
			}
		}
		return st.ID, fmt.Sprintf("stack %s created with %s at the bottom", st.ID, bottom), nil
	})
}

// AddToStack is POST /api/stacks/{id}/cards: `gummi stack add`.
func (b *Bridge) AddToStack(ctx context.Context, id string, req webapi.StackRequest) (WebOutcome, error) {
	card := webID(req.Card)
	if card == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the card to add")
	}
	pos := -1
	if req.Pos != nil {
		pos = *req.Pos
	}
	return b.stackWrite(ctx, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		sid := domain.StackID(id)
		if err := store.AddToStack(ctx, sid, card, pos); err != nil {
			return "", "", err
		}
		return sid, fmt.Sprintf("%s added to %s", card, sid), nil
	})
}

// MoveInStack is POST /api/stacks/{id}/move: `gummi stack mv`.
func (b *Bridge) MoveInStack(ctx context.Context, id string, req webapi.StackRequest) (WebOutcome, error) {
	card := webID(req.Card)
	if card == "" || req.Pos == nil {
		return WebOutcome{}, webErr(WebBadRequest, "name the card and the position to move it to")
	}
	return b.stackWrite(ctx, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		sid := domain.StackID(id)
		if err := stackMember(ctx, store, sid, card); err != nil {
			return "", "", err
		}
		if err := store.MoveInStack(ctx, card, *req.Pos); err != nil {
			return "", "", err
		}
		f, err := store.GetFeature(ctx, card)
		if err != nil {
			return "", "", err
		}
		return sid, fmt.Sprintf("%s moved to position %d in %s", card, f.StackPos, sid), nil
	})
}

// RemoveFromStack is DELETE /api/stacks/{id}/cards/{card}: `gummi stack rm`.
func (b *Bridge) RemoveFromStack(ctx context.Context, id, cardID string) (WebOutcome, error) {
	card := webID(cardID)
	return b.stackWrite(ctx, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		sid := domain.StackID(id)
		if err := stackMember(ctx, store, sid, card); err != nil {
			return "", "", err
		}
		if err := store.RemoveFromStack(ctx, card); err != nil {
			return "", "", err
		}
		return sid, fmt.Sprintf("%s removed from %s — any card above it will be replayed onto its new base", card, sid), nil
	})
}

// RenameStack is POST /api/stacks/{id}/rename.
func (b *Bridge) RenameStack(ctx context.Context, id, name string) (WebOutcome, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return WebOutcome{}, webErr(WebBadRequest, "name the stack")
	}
	return b.stackWrite(ctx, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		sid := domain.StackID(id)
		if err := store.RenameStack(ctx, sid, name); err != nil {
			return "", "", err
		}
		return sid, fmt.Sprintf("%s renamed to %s", sid, name), nil
	})
}

// DeleteStack is DELETE /api/stacks/{id}: only an empty stack goes.
func (b *Bridge) DeleteStack(ctx context.Context, id string) (WebOutcome, error) {
	return b.stackWriteTick(ctx, false, func(ctx context.Context, store *state.Store) (domain.StackID, string, error) {
		sid := domain.StackID(id)
		if _, err := store.GetStack(ctx, sid); err != nil {
			return "", "", err
		}
		if err := store.DeleteStack(ctx, sid); err != nil {
			return "", "", err
		}
		return sid, fmt.Sprintf("stack %s deleted", sid), nil
	})
}

// stackMember refuses a card that is not in the stack the route named.
func stackMember(ctx context.Context, store *state.Store, id domain.StackID, card domain.FeatureID) error {
	f, err := store.GetFeature(ctx, card)
	if err != nil {
		return err
	}
	if f.StackID != id {
		return fmt.Errorf("%s is not in stack %s: %w", card, id, state.ErrNotFound)
	}
	return nil
}

// Restack is POST /api/stacks/{id}/restack: the walk to a fixed point
// `gummi stack restack` forces (engine.Restack), with the pushes it now
// needs. A conflict is reported, not handed to an agent: the TUI offers
// one through a dialog, and a session costs credits nobody here agreed to.
func (b *Bridge) Restack(ctx context.Context, id string) (webapi.Restack, error) {
	res, err := webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.Restack, error), error) {
		_, eng, release, err := m.webStackHandles()
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context) (webapi.Restack, error) {
			defer release()
			sid := domain.StackID(id)
			r, err := eng.Restack(ctx, sid)
			if err != nil {
				return webapi.Restack{}, webStoreErr(err)
			}
			out := webapi.Restack{Replayed: make([]string, 0, len(r.Replayed)), Waiting: r.Waiting}
			for _, c := range r.Replayed {
				out.Replayed = append(out.Replayed, string(c))
			}
			if r.Conflict != nil {
				out.Conflict = &webapi.StackConflict{Card: string(r.ConflictCard), Files: r.Conflict.Files}
			}
			view, err := eng.StackSnapshot(ctx, sid)
			if err != nil {
				return webapi.Restack{}, webStoreErr(err)
			}
			out.Stack = webStack(view)
			out.Stack.Push = r.Push
			return out, nil
		}, nil
	})
	if err != nil {
		return res, err
	}
	if len(res.Replayed) > 0 {
		// the replays moved branches: the board's git-derived columns are
		// stale, exactly as after a tick that replayed (onStackTick)
		b.deliver(noticeMsg{reload: true})
	}
	return res, nil
}
