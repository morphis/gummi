package reviewround

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// fakeStore records which accessor was called and returns a canned value,
// proving each helper forwards to exactly the right store method.
type fakeStore struct {
	id       domain.FeatureID
	load     int
	bump     int
	reset    int
	loadErr  error
	writeErr error
}

func (f *fakeStore) ReviewRounds(_ context.Context, id domain.FeatureID) (int, error) {
	f.id = id
	f.load++
	return 3, f.loadErr
}

func (f *fakeStore) IncrementReviewRounds(_ context.Context, id domain.FeatureID) error {
	f.id = id
	f.bump++
	return f.writeErr
}

func (f *fakeStore) ClearReviewRounds(_ context.Context, id domain.FeatureID) error {
	f.id = id
	f.reset++
	return f.writeErr
}

func id() domain.FeatureID {
	i, _ := domain.NewFeatureID(7)
	return i
}

func TestLoadCallsReviewRounds(t *testing.T) {
	f := &fakeStore{}
	got, err := Load(context.Background(), f, id())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != 3 {
		t.Fatalf("Load = %d, want 3 (the store's value)", got)
	}
	if f.load != 1 || f.bump != 0 || f.reset != 0 {
		t.Fatalf("Load called load=%d bump=%d reset=%d; want 1/0/0", f.load, f.bump, f.reset)
	}
}

func TestBumpCallsIncrement(t *testing.T) {
	f := &fakeStore{}
	if err := Bump(context.Background(), f, id()); err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if f.bump != 1 || f.load != 0 || f.reset != 0 {
		t.Fatalf("Bump called bump=%d load=%d reset=%d; want 1/0/0", f.bump, f.load, f.reset)
	}
}

func TestResetCallsClear(t *testing.T) {
	f := &fakeStore{}
	if err := Reset(context.Background(), f, id()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if f.reset != 1 || f.load != 0 || f.bump != 0 {
		t.Fatalf("Reset called reset=%d load=%d bump=%d; want 1/0/0", f.reset, f.load, f.bump)
	}
}

func TestLoadPropagatesError(t *testing.T) {
	want := errors.New("read failed")
	f := &fakeStore{loadErr: want}
	if _, err := Load(context.Background(), f, id()); !errors.Is(err, want) {
		t.Fatalf("Load err = %v, want %v", err, want)
	}
}

func TestBumpPropagatesError(t *testing.T) {
	want := errors.New("write failed")
	f := &fakeStore{writeErr: want}
	if err := Bump(context.Background(), f, id()); !errors.Is(err, want) {
		t.Fatalf("Bump err = %v, want %v", err, want)
	}
}

func TestResetPropagatesError(t *testing.T) {
	want := errors.New("write failed")
	f := &fakeStore{writeErr: want}
	if err := Reset(context.Background(), f, id()); !errors.Is(err, want) {
		t.Fatalf("Reset err = %v, want %v", err, want)
	}
}
