package ui

import (
	"sync"

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
}

func newInbox() *inbox {
	return &inbox{items: map[domain.FeatureID]attnItem{}}
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
	_, existed := b.items[it.Feature]
	if !existed {
		b.order = append(b.order, it.Feature)
	}
	b.items[it.Feature] = it
	return !existed
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
