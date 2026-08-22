package driver

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// readSeq returns the current workspace sequence number.
func readSeq(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seq %s: %v", path, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse seq %q: %v", raw, err)
	}
	return n
}

// multiRepoHarness wires a driver over a pool with the workspace root as
// the default repo plus two nested named repos.
func multiRepoHarness(t *testing.T) (*Driver, *state.Store) {
	t.Helper()
	h := newMultiRepoHarness(t, nil)
	return h.driver(Options{}), h.store
}

// TestCreatePersistsRepo: a card created with a named repo persists the
// name; the default (empty) repo persists an empty value.
func TestCreatePersistsRepo(t *testing.T) {
	d, store := multiRepoHarness(t)
	ctx := context.Background()

	d2 := *d
	d2.opts = d.opts
	d2.opts.Repo = "a"
	f, err := d2.Create(ctx, domain.KindFeature, "A feature in repo a")
	if err != nil {
		t.Fatal(err)
	}
	if f.Repo != "a" {
		t.Errorf("created repo = %q, want a", f.Repo)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil || got.Repo != "a" {
		t.Errorf("persisted repo = %q, err=%v; want a", got.Repo, err)
	}

	f2, err := d.Create(ctx, domain.KindFeature, "A feature in the default repo")
	if err != nil {
		t.Fatal(err)
	}
	if f2.Repo != "" {
		t.Errorf("default created repo = %q, want empty", f2.Repo)
	}
}

// TestCreateRejectsUnknownRepo: an unknown repo name fails at creation,
// before any card is minted.
func TestCreateRejectsUnknownRepo(t *testing.T) {
	h := newMultiRepoHarness(t, nil)
	d := h.driver(Options{})
	d.opts.Repo = "nope"
	seq := readSeq(t, h.ws.SeqFile())
	if _, err := d.Create(context.Background(), domain.KindFeature, "should not exist"); err == nil {
		t.Fatal("expected an error creating against an unconfigured repo")
	}
	// the unknown repo is rejected before a sequence number is consumed.
	if got := readSeq(t, h.ws.SeqFile()); got != seq {
		t.Errorf("seq advanced on rejected create: %d -> %d", seq, got)
	}
}
