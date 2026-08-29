package state

import (
	"fmt"
	"sync"

	"github.com/morphis/gummi/internal/domain"
)

// CardLocks holds a workspace's per-card locks on behalf of one long-lived
// process — the TUI board.
//
// Headless run/resume/verify/merge/clean each take CardLockFile for the
// span of one command and let it go (cmd/gummi), which is all a
// one-shot process needs. The board is different: it drives many cards at
// once, over its whole lifetime, from several places at once (a session,
// a merge, a rebase). It needs the same cross-process exclusion, but
// re-acquiring the same card's flock from within the same process fails
// exactly as a foreign process's would — advisory locks are per open file
// description, not per process. So the board holds each card's lock once
// and refcounts it here: concurrent holders inside this process share the
// one flock, while any other gummi is excluded for as long as any of them
// is live.
//
// A nil *CardLocks is a working no-op that hands back a no-op release, so
// callers that do their own locking (the headless driver, which already
// holds the card's lock around the whole command) and tests need no
// branch at each call site.
type CardLocks struct {
	ws Workspace

	mu   sync.Mutex
	held map[domain.FeatureID]*heldCard
}

// heldCard is one flock plus the number of live holders inside this
// process.
type heldCard struct {
	release func()
	n       int
}

// NewCardLocks returns the registry for ws. The board builds exactly one
// and shares it between its engine and its own git verbs, so a merge on a
// card this board is already driving joins the lock it holds instead of
// deadlocking against itself.
func NewCardLocks(ws Workspace) *CardLocks {
	return &CardLocks{ws: ws, held: map[domain.FeatureID]*heldCard{}}
}

// Acquire takes (or joins) the lock on card id and returns the release
// for this holder. The returned func is idempotent — a caller that
// releases on two paths releases once — and the flock itself is dropped
// only when the last holder in this process lets go.
//
// It returns ErrLocked, wrapped with the card id, when another gummi
// process holds the card: a headless run/resume/merge/clean for it.
func (c *CardLocks) Acquire(id domain.FeatureID) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.held[id]; ok {
		h.n++
		return c.releaser(id), nil
	}
	release, err := AcquireLock(c.ws.CardLockFile(id))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	c.held[id] = &heldCard{release: release, n: 1}
	return c.releaser(id), nil
}

// Holds reports whether this process currently holds card id's lock. It
// is the honest answer to "are we driving this", for a caller deciding
// whether a refusal belongs to another process or to this one.
func (c *CardLocks) Holds(id domain.FeatureID) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.held[id]
	return ok
}

// releaser builds this holder's one-shot release.
func (c *CardLocks) releaser(id domain.FeatureID) func() {
	var once sync.Once
	return func() { once.Do(func() { c.drop(id) }) }
}

// drop retires one holder, unlocking when the last lets go.
func (c *CardLocks) drop(id domain.FeatureID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.held[id]
	if !ok {
		return
	}
	h.n--
	if h.n > 0 {
		return
	}
	delete(c.held, id)
	h.release()
}
