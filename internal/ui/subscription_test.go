package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSubscriptionIdentity covers the tag's identity contract: a
// subscription-wrapped command is recognized by isSubscription for the
// pump's skip decision, and a plain finite command is never mistaken for
// one. Each subscription() call yields a fresh closure that Go compiles to
// its own code object, so identity relies on registration rather than code
// pointers.
func TestSubscriptionIdentity(t *testing.T) {
	if isSubscription(nil) {
		t.Fatal("nil cmd reported as a subscription")
	}
	finite := func() tea.Msg { return nil }
	if isSubscription(finite) {
		t.Fatal("plain finite cmd reported as a subscription")
	}
	wrapped := subscription(finite)
	if !isSubscription(wrapped) {
		t.Fatal("subscription-wrapped cmd not recognized")
	}
	if isSubscription(finite) {
		t.Fatal("wrapping a finite cmd leaked its identity onto the bare fn")
	}
}

// TestSubscriptionSitesAreTagged audits the production subscription sites
// under internal/ui so a regression — a new timer cmd shipped un-wrapped,
// or a channel-blocking msg producer called bare — fails loudly at the
// offending file:line instead of surfacing as a suite-wide flake.
//
// Two topologies are checked independently (an inline string match can't
// span a multi-segment selector like m.engine.Events(), nor tell the two
// wrap shapes apart):
//
//   - Timer-emitting cmds (inline wrap): any function body that calls
//     tea.Tick must also call subscription(...) in the same body, since
//     the wrap must live where the timer is produced.
//   - Channel-blocking msg producers (reference wrap): any function whose
//     single result is tea.Msg and whose body does `<-x.y.Events()` is a
//     long-lived subscription; every value use of it across the package
//     must sit directly inside a subscription(...) call. This catches
//     listenEngine (whose wrap lives on the callers via listenEngineCmd)
//     and any future bare Events() producer.
//
// listenIngestSteps (ingestview.go) is deliberately excluded by shape: its
// receive operand is a plain channel ident (`<-ch`), not a SelectorExpr
// ending in Events, and its per-operation channel closes when the pass
// finishes — bounded lifetime, unlike the engine's long-lived event bus.
func TestSubscriptionSitesAreTagged(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var prodFiles []string
	for _, e := range entries {
		if !strings.HasSuffix(e, "_test.go") {
			prodFiles = append(prodFiles, e)
		}
	}
	if len(prodFiles) == 0 {
		t.Fatal("no production .go files found to audit")
	}

	fset := token.NewFileSet()
	producers := make(map[string]token.Pos) // func name -> decl pos, for messages
	for _, name := range prodFiles {
		f, err := parser.ParseFile(fset, name, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// inline-wrap: timer cmd bodies must also wrap in subscription.
			if containsTeaTick(fn.Body) && !containsSubscriptionCall(fn.Body) {
				t.Errorf("%s: %s calls tea.Tick but is not wrapped in subscription(...)",
					fset.Position(fn.Pos()), fn.Name.Name)
			}
			// reference-wrap: identify channel-blocking tea.Msg producers.
			if isChannelBlockingProducer(fn) {
				producers[fn.Name.Name] = fn.Pos()
			}
		}
	}

	if len(producers) == 0 {
		t.Fatal("audit found no subscription-producing functions; the shape assumptions may be stale")
	}

	// Reference-wrap: every value use of a producer must sit directly
	// inside a subscription(...) call.
	for _, name := range prodFiles {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parents := nodeParents(f)
		for n := range parents {
			id, ok := n.(*ast.Ident)
			if !ok {
				continue
			}
			if _, ok := producers[id.Name]; !ok {
				continue
			}
			sub := valueUseInsideSubscription(id, parents)
			if sub == nil {
				// Not a bare value use (declaration / method call) — fine.
				if _, bare := bareValueSelector(id, parents); bare {
					t.Errorf("%s: value use of subscription producer %q (%s) is not wrapped in subscription(...)",
						fset.Position(id.Pos()), id.Name, fset.Position(producers[id.Name]))
				}
			}
		}
	}
}

// nodeParents builds a parent map for every ast.Node in f.
func nodeParents(f *ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}

// containsTeaTick reports whether n contains a tea.Tick(...) call.
func containsTeaTick(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		call, ok := c.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Tick" {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "tea" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// containsSubscriptionCall reports whether n contains a subscription(...)
// call.
func containsSubscriptionCall(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		call, ok := c.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "subscription" {
			found = true
			return false
		}
		return true
	})
	return found
}

// isChannelBlockingProducer reports whether fn is a long-lived
// subscription: a single tea.Msg result and a `<-x.y.Events()` receive.
func isChannelBlockingProducer(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	rt := fn.Type.Results.List[0].Type
	if id, ok := rt.(*ast.Ident); ok {
		if id.Name != "Msg" {
			return false
		}
	} else if sel, ok := rt.(*ast.SelectorExpr); ok {
		if _, xok := sel.X.(*ast.Ident); !xok || sel.Sel.Name != "Msg" {
			return false
		}
	} else {
		return false
	}
	blocking := false
	ast.Inspect(fn.Body, func(c ast.Node) bool {
		u, ok := c.(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return true
		}
		call, ok := u.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		cal, ok := call.Fun.(*ast.SelectorExpr)
		if ok && cal.Sel.Name == "Events" {
			blocking = true
			return false
		}
		return true
	})
	return blocking
}

// valueUseInsideSubscription reports whether the value expression
// containing id (if id is a value use) sits directly inside a
// subscription(...) call. Returns the subscription CallExpr if so, else
// nil. Returns nil when id is not a bare value use at all (a declaration
// name, or a method-call site like m.listenEngine()).
func valueUseInsideSubscription(id *ast.Ident, parents map[ast.Node]ast.Node) *ast.CallExpr {
	if fn, ok := parents[id].(*ast.FuncDecl); ok && fn.Name == id {
		return nil // declaration
	}
	node := ast.Node(id)
	// Collapse a possibly-nested selector chain up to the outermost
	// SelectorExpr that is the actual value expression.
	for {
		p, ok := parents[node]
		if !ok {
			return nil
		}
		if sel, ok := p.(*ast.SelectorExpr); ok && sel.Sel == node {
			node = sel
			continue
		}
		break
	}
	// If the collapsed value is itself the callee of a call, it's a method
	// call (m.listenEngine()) — not a bare value use.
	if call, ok := parents[node].(*ast.CallExpr); ok && call.Fun == node {
		return nil
	}
	call, ok := parents[node].(*ast.CallExpr)
	if !ok {
		return nil
	}
	if id2, ok := call.Fun.(*ast.Ident); ok && id2.Name == "subscription" {
		return call
	}
	return nil
}

// bareValueSelector reports whether id is a bare value use (for the error
// path: a value use that was not inside a subscription call). Returns the
// outer value node and true when id is not a declaration and not a method
// call.
func bareValueSelector(id *ast.Ident, parents map[ast.Node]ast.Node) (ast.Node, bool) {
	if fn, ok := parents[id].(*ast.FuncDecl); ok && fn.Name == id {
		return nil, false
	}
	if valueUseInsideSubscription(id, parents) != nil {
		return nil, false
	}
	node := ast.Node(id)
	for {
		p, ok := parents[node]
		if !ok {
			return node, true
		}
		if sel, ok := p.(*ast.SelectorExpr); ok && sel.Sel == node {
			node = sel
			continue
		}
		break
	}
	if call, ok := parents[node].(*ast.CallExpr); ok && call.Fun == node {
		return nil, false // method call
	}
	return node, true
}
