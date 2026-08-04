package state

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A feature's gate-approval mode survives create → read, and the
// SetGateApproval side-channel updates it in place without moving the stage
// — the persistence a resume relies on to inherit the run's chosen mode.
func TestGateApprovalRoundtrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	f := feat(1, "Add a healthz endpoint")
	f.GateApproval = domain.GateCaller
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateCaller {
		t.Fatalf("gate-approval lost in roundtrip: %q, want %q", got.GateApproval, domain.GateCaller)
	}

	// side-channel override to auto.
	if err := s.SetGateApproval(ctx, f.ID, domain.GateAuto); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateAuto {
		t.Fatalf("SetGateApproval did not persist: %q, want %q", got.GateApproval, domain.GateAuto)
	}

	// an unknown mode is refused, not stored.
	if err := s.SetGateApproval(ctx, f.ID, "sometimes"); err == nil {
		t.Fatal("SetGateApproval accepted an unknown mode")
	}
}

// A feature created without an explicit mode reads back empty (which the
// driver treats as auto) — old rows and default runs need no backfill.
func TestGateApprovalDefaultsEmpty(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(2, "Another feature")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != "" {
		t.Fatalf("default gate-approval = %q, want empty", got.GateApproval)
	}
}
