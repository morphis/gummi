package ui

import (
	"reflect"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// subscription marks a command as a long-lived subscription: one that, in
// the real Bubble Tea runtime, never returns in finite time (a blocking
// channel read bridged into the tea loop, or a re-arming timer). It is a
// runtime no-op — the returned closure's only behavior is to invoke inner
// — but it lets test scaffolds that drive commands synchronously
// (flow_test.go pump) tell a finite command from a subscription without
// resorting to a wall-clock timeout.
//
// Identity is by registration, not by code pointer: Go compiles a distinct
// closure trampoline per call site, so two subscription() results never
// share a function value. Each wrapped command is instead recorded in
// subscriptions when it is created, so pump can answer "is this the tag?"
// with a pointer-keyed lookup.
func subscription(inner tea.Cmd) tea.Cmd {
	cmd := func() tea.Msg { return inner() }
	subscriptions.mu.Lock()
	subscriptions.s[reflect.ValueOf(cmd).Pointer()] = struct{}{}
	subscriptions.mu.Unlock()
	return cmd
}

// isSubscription reports whether cmd was produced by subscription and so,
// in the real runtime, never returns on its own.
func isSubscription(cmd tea.Cmd) bool {
	subscriptions.mu.Lock()
	defer subscriptions.mu.Unlock()
	_, ok := subscriptions.s[reflect.ValueOf(cmd).Pointer()]
	return ok
}

// subscriptions is the registry of live subscription-wrapped commands.
// A plain finite command is never registered, so it can never be mistaken
// for a subscription.
var subscriptions = struct {
	mu sync.Mutex
	s  map[uintptr]struct{}
}{s: map[uintptr]struct{}{}}
