package state

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func ids(xs []domain.FeatureID) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func TestDependencyAddListRemove(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	if err := s.CreateFeature(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, b); err != nil {
		t.Fatal(err)
	}

	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	// Forward: what a depends on.
	deps, err := s.ListDependencies(ctx, a.ID)
	if err != nil || len(deps) != 1 || deps[0] != b.ID {
		t.Fatalf("ListDependencies = %v, err=%v; want [%s]", ids(deps), err, b.ID)
	}
	// Reverse: who depends on b.
	deps, err = s.ListDependents(ctx, b.ID)
	if err != nil || len(deps) != 1 || deps[0] != a.ID {
		t.Fatalf("ListDependents = %v, err=%v; want [%s]", ids(deps), err, a.ID)
	}
	// The other directions are empty.
	if deps, _ := s.ListDependencies(ctx, b.ID); len(deps) != 0 {
		t.Fatalf("b has forward edges: %v", ids(deps))
	}
	if deps, _ := s.ListDependents(ctx, a.ID); len(deps) != 0 {
		t.Fatalf("a has dependents: %v", ids(deps))
	}

	// Remove empties both directions.
	if err := s.RemoveDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	if deps, _ := s.ListDependencies(ctx, a.ID); len(deps) != 0 {
		t.Fatalf("deps after remove: %v", ids(deps))
	}
	if deps, _ := s.ListDependents(ctx, b.ID); len(deps) != 0 {
		t.Fatalf("dependents after remove: %v", ids(deps))
	}
}

func TestDependencyRejectsSelfLoop(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	if err := s.CreateFeature(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, a.ID, a.ID); !errors.Is(err, ErrSelfLoop) {
		t.Fatalf("AddDependency(A,A) = %v, want ErrSelfLoop", err)
	}
}

func TestDependencyRejectsCycle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	for _, f := range []*domain.Feature{a, b} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	// b → a would close a→b→a.
	if err := s.AddDependency(ctx, b.ID, a.ID); !errors.Is(err, ErrCycle) {
		t.Fatalf("AddDependency(B,A) = %v, want ErrCycle", err)
	}
	// WouldCycle pre-checks the same condition the store rejects: an edge
	// the store would refuse as a cycle reads true here, without writing.
	if cyc, err := s.WouldCycle(ctx, b.ID, a.ID); err != nil || !cyc {
		t.Fatalf("WouldCycle(B,A) = %v, err=%v; want true", cyc, err)
	}
	if cyc, err := s.WouldCycle(ctx, a.ID, b.ID); err != nil || cyc {
		t.Fatalf("WouldCycle(A,B) = %v, err=%v; want false (already the edge)", cyc, err)
	}
	// Still only the original edge.
	if deps, _ := s.ListDependencies(ctx, b.ID); len(deps) != 0 {
		t.Fatalf("b gained edges despite cycle rejection: %v", ids(deps))
	}
	// The original edge survives unchanged: a still depends on b.
	if deps, _ := s.ListDependencies(ctx, a.ID); len(deps) != 1 || deps[0] != b.ID {
		t.Fatalf("a deps after cycle rejection = %v, want [%s]", ids(deps), b.ID)
	}
	if deps, _ := s.ListDependents(ctx, b.ID); len(deps) != 1 || deps[0] != a.ID {
		t.Fatalf("dependents of b = %v, want [%s]", ids(deps), a.ID)
	}
}

func TestDependencyRejectsLateAttachment(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	a.Stage = domain.StageImplement
	b := feat(2, "Beta")
	for _, f := range []*domain.Feature{a, b} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	// A card already at a coding stage may not take on a new dependency.
	if err := s.AddDependency(ctx, a.ID, b.ID); !errors.Is(err, ErrLateAttachment) {
		t.Fatalf("AddDependency on coding card = %v, want ErrLateAttachment", err)
	}
	if deps, _ := s.ListDependencies(ctx, a.ID); len(deps) != 0 {
		t.Fatalf("coding card gained edges: %v", ids(deps))
	}
}

func TestDependencyRejectsUnknownFeature(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	if err := s.CreateFeature(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, a.ID, "FD-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddDependency to unknown = %v, want ErrNotFound", err)
	}
	if err := s.AddDependency(ctx, "FD-999", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddDependency from unknown = %v, want ErrNotFound", err)
	}
	// List on a missing feature also reports ErrNotFound.
	if _, err := s.ListDependencies(ctx, "FD-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListDependencies missing = %v, want ErrNotFound", err)
	}
	if _, err := s.ListDependents(ctx, "FD-999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListDependents missing = %v, want ErrNotFound", err)
	}
}

func TestDependencyAddIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	for _, f := range []*domain.Feature{a, b} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	// Re-adding is a no-op, not a raw UNIQUE-constraint error.
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("re-add = %v, want nil (idempotent)", err)
	}
	if deps, _ := s.ListDependencies(ctx, a.ID); len(deps) != 1 {
		t.Fatalf("duplicate edge created a second row: %v", ids(deps))
	}
}

func TestDependencyRemoveIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	for _, f := range []*domain.Feature{a, b} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("removing missing edge = %v, want nil", err)
	}
}

func TestDeleteFeatureRefusesDependedOn(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	for _, f := range []*domain.Feature{a, b} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddDependency(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteFeature(ctx, b.ID); !errors.Is(err, ErrDependedOn) {
		t.Fatalf("DeleteFeature(depended-on) = %v, want ErrDependedOn", err)
	}
	// Both cards still exist and the edge is intact.
	for _, id := range []domain.FeatureID{a.ID, b.ID} {
		if _, err := s.GetFeature(ctx, id); err != nil {
			t.Fatalf("card %s missing after refused delete: %v", id, err)
		}
	}
	if deps, _ := s.ListDependencies(ctx, a.ID); len(deps) != 1 || deps[0] != b.ID {
		t.Fatalf("edge dropped after refused delete: %v", ids(deps))
	}
}

func TestDeleteDependentKeepsOtherEdges(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := feat(1, "Alpha")
	b := feat(2, "Beta")
	c := feat(3, "Gamma")
	for _, f := range []*domain.Feature{a, b, c} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	// b and c both depend on a.
	if err := s.AddDependency(ctx, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, c.ID, a.ID); err != nil {
		t.Fatal(err)
	}

	// Deleting the dependent b (nobody depends on it) is clean and
	// removes only b→a; c's edge to a survives.
	if err := s.DeleteFeature(ctx, b.ID); err != nil {
		t.Fatalf("DeleteFeature(b) = %v", err)
	}
	if _, err := s.GetFeature(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b still present: %v", err)
	}
	if deps, _ := s.ListDependents(ctx, a.ID); len(deps) != 1 || deps[0] != c.ID {
		t.Fatalf("dependents of a after b deleted = %v, want [%s]", ids(deps), c.ID)
	}
	// a is still intact and reachable; deleting it is refused while c
	// depends on it.
	if err := s.DeleteFeature(ctx, a.ID); !errors.Is(err, ErrDependedOn) {
		t.Fatalf("DeleteFeature(a) = %v, want ErrDependedOn", err)
	}
}

func TestDependencyKindsAreOrthogonal(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Alpha")
	bg, _ := domain.NewID(domain.KindBug, 2)
	b := feat(2, "Beta")
	b.ID = bg
	b.Kind = domain.KindBug
	c := feat(3, "Gamma")
	for _, f := range []*domain.Feature{f, b, c} {
		if err := s.CreateFeature(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	// A feature may depend on a bug and vice versa, with no cycle.
	if err := s.AddDependency(ctx, f.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDependency(ctx, b.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if deps, _ := s.ListDependencies(ctx, f.ID); len(deps) != 1 || deps[0] != b.ID {
		t.Fatalf("feature deps = %v, want [%s]", ids(deps), b.ID)
	}
	if deps, _ := s.ListDependencies(ctx, b.ID); len(deps) != 1 || deps[0] != c.ID {
		t.Fatalf("bug deps = %v, want [%s]", ids(deps), c.ID)
	}
}
