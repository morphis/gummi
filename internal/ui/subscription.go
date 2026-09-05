package ui

import (
	"reflect"
	"runtime"
	"strings"
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

// isSubscription reports whether cmd is one the real runtime services
// asynchronously and that therefore never returns promptly on its own:
// anything subscription() wrapped, plus the cursor blink a focused text
// widget hands back.
func isSubscription(cmd tea.Cmd) bool {
	subscriptions.mu.Lock()
	_, ok := subscriptions.s[reflect.ValueOf(cmd).Pointer()]
	subscriptions.mu.Unlock()
	return ok || isCursorBlink(cmd)
}

// isCursorBlink reports whether cmd is bubbles' cursor-blink command.
//
// It is a subscription in everything but name: it blocks until the blink
// timer fires, and the real event loop runs it on its own goroutine. A
// test harness that drains commands synchronously does not — so every
// keystroke into a focused textarea paid a full blink interval, and a
// test that typed a sixteen-character line paid it sixteen times. That
// is where internal/ui's runtime went: 266 seconds of sleeping, almost
// none of it work (a CPU profile of the worst single test showed 40ms of
// samples in 21s of wall clock).
//
// Matched on the symbol name because the command is a closure returned
// by a third-party constructor — there is no exported identity to
// compare against. A bubbles upgrade that renames it would make this
// stop matching, so TestCursorBlinkReadsAsASubscription asserts the
// match directly: the suite gets slow again only over a failing test,
// never silently.
func isCursorBlink(cmd tea.Cmd) bool {
	pc := reflect.ValueOf(cmd).Pointer()
	if pc == 0 {
		return false
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return false
	}
	name := fn.Name()
	return strings.Contains(name, "/cursor.") && strings.Contains(name, ".Blink")
}

// subscriptions is the registry of live subscription-wrapped commands.
// A plain finite command is never registered, so it can never be mistaken
// for a subscription.
var subscriptions = struct {
	mu sync.Mutex
	s  map[uintptr]struct{}
}{s: map[uintptr]struct{}{}}
