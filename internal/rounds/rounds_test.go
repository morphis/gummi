package rounds

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// fakeStore records the (id, kind) triple each call dispatched, so the
// tests can assert the helpers route the right key through.
type fakeStore struct {
	gotID   domain.FeatureID
	gotKind domain.RoundKind
	value   int
	err     error
}

func (f *fakeStore) Rounds(_ context.Context, id domain.FeatureID, kind domain.RoundKind) (int, error) {
	f.gotID, f.gotKind = id, kind
	return f.value, f.err
}

func (f *fakeStore) IncrementRounds(_ context.Context, id domain.FeatureID, kind domain.RoundKind) error {
	f.gotID, f.gotKind = id, kind
	return f.err
}

func (f *fakeStore) ClearRounds(_ context.Context, id domain.FeatureID, kind domain.RoundKind) error {
	f.gotID, f.gotKind = id, kind
	return f.err
}

func TestLoadDispatchesIDAndKind(t *testing.T) {
	fs := &fakeStore{value: 2}
	got, err := Load(context.Background(), fs, "FD-001", domain.RoundKindPlan)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("Load = %d, want 2", got)
	}
	if fs.gotID != "FD-001" || fs.gotKind != domain.RoundKindPlan {
		t.Errorf("Load dispatched (%q, %q), want (FD-001, plan)", fs.gotID, fs.gotKind)
	}
}

func TestBumpDispatchesIDAndKind(t *testing.T) {
	fs := &fakeStore{}
	if err := Bump(context.Background(), fs, "RS-003", domain.RoundKindReview); err != nil {
		t.Fatal(err)
	}
	if fs.gotID != "RS-003" || fs.gotKind != domain.RoundKindReview {
		t.Errorf("Bump dispatched (%q, %q), want (RS-003, review)", fs.gotID, fs.gotKind)
	}
}

func TestResetDispatchesIDAndKind(t *testing.T) {
	fs := &fakeStore{}
	if err := Reset(context.Background(), fs, "BG-007", domain.RoundKindReview); err != nil {
		t.Fatal(err)
	}
	if fs.gotID != "BG-007" || fs.gotKind != domain.RoundKindReview {
		t.Errorf("Reset dispatched (%q, %q), want (BG-007, review)", fs.gotID, fs.gotKind)
	}
}

func TestHelpersPropagateError(t *testing.T) {
	fs := &fakeStore{err: errors.New("boom")}
	if _, err := Load(context.Background(), fs, "FD-001", domain.RoundKindPlan); err == nil {
		t.Error("Load: want error propagated")
	}
	if err := Bump(context.Background(), fs, "FD-001", domain.RoundKindPlan); err == nil {
		t.Error("Bump: want error propagated")
	}
	if err := Reset(context.Background(), fs, "FD-001", domain.RoundKindPlan); err == nil {
		t.Error("Reset: want error propagated")
	}
}
