package state

import (
	"errors"
	"strings"
	"testing"
)

func testLocks(t *testing.T) *CardLocks {
	t.Helper()
	dir := t.TempDir()
	return NewCardLocks(Workspace{Root: dir, RepoRoot: dir})
}

// Two holders inside one process share the card's flock: the second
// Acquire joins rather than refusing itself, which is what lets a merge
// run on a card the board's own engine is already driving.
func TestCardLocksRefcount(t *testing.T) {
	c := testLocks(t)

	first, err := c.Acquire("FD-001")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := c.Acquire("FD-001")
	if err != nil {
		t.Fatalf("second acquire in the same process: %v — holders must share the flock", err)
	}
	if !c.Holds("FD-001") {
		t.Fatal("Holds is false while two holders are live")
	}

	// the flock survives the first holder leaving...
	first()
	if !c.Holds("FD-001") {
		t.Fatal("the lock dropped while a holder was still live")
	}
	if _, err := AcquireLock(c.ws.CardLockFile("FD-001")); !errors.Is(err, ErrLocked) {
		t.Fatalf("a foreign acquire succeeded (%v) while a holder was live", err)
	}

	// ...and is dropped when the last one does.
	second()
	if c.Holds("FD-001") {
		t.Fatal("Holds is true after every holder left")
	}
	release, err := AcquireLock(c.ws.CardLockFile("FD-001"))
	if err != nil {
		t.Fatalf("the flock was not released: %v", err)
	}
	release()
}

// A holder's release is one-shot: a caller that releases on two paths
// (a session's death path and its teardown) must not drop someone else's
// hold.
func TestCardLocksReleaseIsIdempotent(t *testing.T) {
	c := testLocks(t)

	a, err := c.Acquire("FD-002")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Acquire("FD-002")
	if err != nil {
		t.Fatal(err)
	}
	a()
	a() // the double release must not retire b's hold
	if !c.Holds("FD-002") {
		t.Fatal("a repeated release dropped another holder's lock")
	}
	b()
	if c.Holds("FD-002") {
		t.Fatal("the lock outlived its last holder")
	}
}

// A card another process holds is refused, with the card named.
func TestCardLocksRefusesForeignHolder(t *testing.T) {
	c := testLocks(t)

	// stand in for the other process: a flock taken outside the registry,
	// exactly as a headless run/resume takes it.
	foreign, err := AcquireLock(c.ws.CardLockFile("FD-003"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign()

	if _, err := c.Acquire("FD-003"); !errors.Is(err, ErrLocked) {
		t.Fatalf("Acquire on a foreign-held card = %v, want ErrLocked", err)
	} else if got := err.Error(); !strings.Contains(got, "FD-003") {
		t.Errorf("error %q does not name the card", got)
	}
	if c.Holds("FD-003") {
		t.Error("Holds is true for a card this process failed to lock")
	}
}

// Disjoint cards lock independently — the whole point of a per-card lock.
func TestCardLocksDisjointCards(t *testing.T) {
	c := testLocks(t)

	a, err := c.Acquire("FD-004")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := c.Acquire("FD-005")
	if err != nil {
		t.Fatalf("a second card was refused while another was held: %v", err)
	}
	b()
}

// A nil registry is a working no-op, so a caller with no locking wired
// (a test scaffold, the headless driver that locks for itself) needs no
// branch at the call site.
func TestNilCardLocks(t *testing.T) {
	var c *CardLocks
	release, err := c.Acquire("FD-006")
	if err != nil {
		t.Fatalf("nil registry refused: %v", err)
	}
	release()
	if c.Holds("FD-006") {
		t.Error("a nil registry claims to hold a card")
	}
}
