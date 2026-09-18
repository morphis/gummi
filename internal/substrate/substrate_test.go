package substrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/config"
)

// rig is a substrate made of files: "up" exists when it is provisioned,
// "dirty" when a job has used it and nobody has reset it.
func rig(t *testing.T, spec config.Substrate) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	if spec.Probe == "" {
		spec.Probe = "test -f up && ! test -f dirty"
	}
	return New(filepath.Join(root, ".gummi", "state"), root, map[string]config.Substrate{"rig": spec}), root
}

func touch(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusClassifiesTheProbe(t *testing.T) {
	m, root := rig(t, config.Substrate{})
	ctx := context.Background()
	if st, _ := m.Status(ctx, "rig"); st.State != Absent {
		t.Fatalf("nothing there: got %s", st.State)
	}
	touch(t, root, "up")
	if st, _ := m.Status(ctx, "rig"); st.State != Ready {
		t.Fatalf("got %s", st.State)
	}
	m2, _ := rig(t, config.Substrate{Probe: "no-such-command-anywhere"})
	if st, _ := m2.Status(ctx, "rig"); st.State != Broken {
		t.Fatalf("a probe that cannot run is no clean answer: got %s", st.State)
	}
	if _, err := m.Status(ctx, "other"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

// Two jobs on one substrate are not slow, they are wrong, so the second is
// refused — and told who has it.
func TestOneHolderAtATime(t *testing.T) {
	m, root := rig(t, config.Substrate{})
	touch(t, root, "up")
	ctx := context.Background()
	lease, err := m.Acquire("rig", Holder{Who: "GL-001", Purpose: "integration run"})
	if err != nil {
		t.Fatal(err)
	}
	var held *HeldError
	if _, err := m.Acquire("rig", Holder{Who: "FD-009"}); !errors.As(err, &held) || held.Holder.Who != "GL-001" {
		t.Fatalf("got %v", err)
	}
	st, _ := m.Status(ctx, "rig")
	if st.State != Held || !strings.Contains(st.Detail, "GL-001 (integration run)") {
		t.Fatalf("a held substrate says who has it, unprobed: %+v", st)
	}
	lease.Release()
	lease.Release() // twice is fine
	if st, _ := m.Status(ctx, "rig"); st.State != Ready {
		t.Fatalf("released: got %s", st.State)
	}
	if l2, err := m.Acquire("rig", Holder{Who: "FD-009"}); err != nil {
		t.Fatal(err)
	} else {
		l2.Release()
	}
}

func TestEnsureReadyResetsBeforeItProvisions(t *testing.T) {
	m, root := rig(t, config.Substrate{Provision: "touch up; rm -f dirty", Reset: "rm -f dirty"})
	ctx := context.Background()
	lease, err := m.Acquire("rig", Holder{Who: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	// not ready, and a probe cannot say whether that is absent or dirty:
	// the cheap step first, then the slow one
	ops, st, err := lease.EnsureReady(ctx, nil)
	if err != nil || st.State != Ready || len(ops) != 2 || ops[0].Kind != "reset" || ops[1].Kind != "provision" || !ops[1].OK {
		t.Fatalf("ops %+v state %s err %v", ops, st.State, err)
	}
	if st.ProvisionedAt.IsZero() {
		t.Fatal("a provision is remembered")
	}
	// ready: nothing to do, nothing to charge
	if ops, _, err := lease.EnsureReady(ctx, nil); err != nil || len(ops) != 0 {
		t.Fatalf("ops %+v err %v", ops, err)
	}
	// dirty: the cheap step is enough
	touch(t, root, "dirty")
	ops, st, err = lease.EnsureReady(ctx, nil)
	if err != nil || st.State != Ready || len(ops) != 1 || ops[0].Kind != "reset" {
		t.Fatalf("ops %+v state %s err %v", ops, st.State, err)
	}
}

func TestEnsureReadySaysWhenNothingHelped(t *testing.T) {
	m, _ := rig(t, config.Substrate{Provision: "true"}) // provisions nothing
	lease, err := m.Acquire("rig", Holder{Who: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ops, st, err := lease.EnsureReady(context.Background(), nil)
	if !errors.Is(err, ErrNotReady) || st.State != Absent || len(ops) != 1 {
		t.Fatalf("ops %+v state %s err %v", ops, st.State, err)
	}
	m2, _ := rig(t, config.Substrate{}) // nothing it may do
	l2, _ := m2.Acquire("rig", Holder{Who: "t"})
	defer l2.Release()
	if ops, _, err := l2.EnsureReady(context.Background(), nil); !errors.Is(err, ErrNotReady) || len(ops) != 0 {
		t.Fatalf("ops %+v err %v", ops, err)
	}
}

// A substrate past its TTL may be reclaimed under a job at any moment,
// whatever its probe says, and no job that cannot finish in time starts.
func TestExpiryOutranksAPassingProbe(t *testing.T) {
	m, _ := rig(t, config.Substrate{Provision: "touch up", TTL: "24h"})
	now := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return now }
	ctx := context.Background()
	lease, _ := m.Acquire("rig", Holder{Who: "t"})
	if _, _, err := lease.EnsureReady(ctx, nil); err != nil {
		t.Fatal(err)
	}
	lease.Release()

	now = now.Add(23 * time.Hour)
	st, _ := m.Status(ctx, "rig")
	if st.State != Ready || st.Fits(90*time.Minute) || !st.Fits(30*time.Minute) {
		t.Fatalf("an hour left fits a half-hour job and not a ninety-minute one: %+v", st)
	}
	now = now.Add(2 * time.Hour)
	if st, _ = m.Status(ctx, "rig"); st.State != Expired {
		t.Fatalf("got %s", st.State)
	}
	lease, _ = m.Acquire("rig", Holder{Who: "t"})
	defer lease.Release()
	ops, st, err := lease.EnsureReady(ctx, nil)
	if err != nil || st.State != Ready || len(ops) != 1 || ops[0].Kind != "provision" {
		t.Fatalf("an expired substrate is provisioned again: ops %+v state %s err %v", ops, st.State, err)
	}
}
