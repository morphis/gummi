package ui

import (
	"sync"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// attnKind classifies why a feature needs your attention.
type attnKind string

const (
	// attnGate: an autonomous stage finished and awaits your decision.
	attnGate attnKind = "gate"
	// attnFailure: a session errored.
	attnFailure attnKind = "failure"
	// attnQuestion: the agent asked something and is waiting.
	attnQuestion attnKind = "question"
	// attnBudget: a stage hit its budget and awaits a top-up/park decision.
	attnBudget attnKind = "budget"
)

// attnItem is one entry in the needs-attention queue.
type attnItem struct {
	Feature domain.FeatureID
	Kind    attnKind
	Text    string
	// Escalated marks a gate an automatic loop gave up on (round cap,
	// unclear verdict) rather than finished clean, so surfaces can tint
	// it as needs-you instead of ready-to-approve.
	Escalated bool
	// At is when the item's decision was raised: a seeded item takes the
	// durable decision_open record's own timestamp; a live-raised item is
	// stamped by put/seed with the shared clock the moment it lands. It is
	// what the inbox tab sorts oldest-first by.
	At time.Time
}

// inbox is the needs-attention queue (DESIGN §4.2): gates, failures,
// and agent questions land here, at most one per feature (newest wins),
// in insertion order. The user cycles it with `tab` and clears an entry
// by acting on its feature. It is mutated both from the Update loop
// (engine events, key handlers) and from command goroutines (session
// teardown on advance/delete), so every method is mutex-guarded.
type inbox struct {
	mu    sync.Mutex
	items map[domain.FeatureID]attnItem
	order []domain.FeatureID
	// now is the shared clock (indirected through Shell.now so tests that
	// fix it after construction still take effect — see NewShell) used to
	// stamp a live-raised item that arrives with no At of its own.
	now func() time.Time
}

func newInbox(now func() time.Time) *inbox {
	return &inbox{items: map[domain.FeatureID]attnItem{}, now: now}
}

// add upserts a feature's attention item, returning true when the feature
// had no prior item (a genuinely new alert, worth a notification) and
// false when it merely updated an existing one.
func (b *inbox) add(id domain.FeatureID, kind attnKind, text string) bool {
	return b.put(attnItem{Feature: id, Kind: kind, Text: text})
}

// addEscalated is add with the escalation flag set.
func (b *inbox) addEscalated(id domain.FeatureID, kind attnKind, text string) bool {
	return b.put(attnItem{Feature: id, Kind: kind, Text: text, Escalated: true})
}

func (b *inbox) put(it attnItem) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if it.At.IsZero() {
		it.At = b.now()
	}
	_, existed := b.items[it.Feature]
	if !existed {
		b.order = append(b.order, it.Feature)
	}
	b.items[it.Feature] = it
	return !existed
}

// seed is put's add-if-absent twin: it adds it only when the feature has
// no pending item yet, and is a no-op otherwise. A live engine event can
// raise a feature's item before a startup seed (the decision-open query's
// reply, or reconstructInbox's session inference) gets around to the same
// feature — both of those look at state that is, by the time their
// message lands, at best as fresh as the live event and often staler — so
// clobbering it with put/add would resurrect exactly what §10.18's record
// is not supposed to fight with: the live surface. Startup seeding and
// reconstructInbox's own adds both go through this instead.
func (b *inbox) seed(it attnItem) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, existed := b.items[it.Feature]; existed {
		return
	}
	if it.At.IsZero() {
		it.At = b.now()
	}
	b.order = append(b.order, it.Feature)
	b.items[it.Feature] = it
}

// get returns a feature's pending attention item, if any.
func (b *inbox) get(id domain.FeatureID) (attnItem, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	return it, ok
}

// remove clears a feature's item (it has been attended to).
func (b *inbox) remove(id domain.FeatureID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[id]; !ok {
		return
	}
	delete(b.items, id)
	for i, q := range b.order {
		if q == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			return
		}
	}
}

// len reports the number of pending items.
func (b *inbox) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// list returns the items in insertion order.
func (b *inbox) list() []attnItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]attnItem, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, b.items[id])
	}
	return out
}

// next returns the feature after `after` in the queue, wrapping. When
// `after` is not in the queue (or empty), it returns the first. Returns
// "" when the queue is empty.
func (b *inbox) next(after domain.FeatureID) domain.FeatureID {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.order) == 0 {
		return ""
	}
	for i, id := range b.order {
		if id == after {
			return b.order[(i+1)%len(b.order)]
		}
	}
	return b.order[0]
}
