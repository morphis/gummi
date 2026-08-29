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
	f.GateApproval = domain.GateOff
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateOff {
		t.Fatalf("gate-approval lost in roundtrip: %q, want %q", got.GateApproval, domain.GateOff)
	}

	// side-channel override to auto.
	if err := s.SetGateApproval(ctx, f.ID, domain.GateGates); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Fatalf("SetGateApproval did not persist: %q, want %q", got.GateApproval, domain.GateGates)
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

// A DB holding the pre-rename spellings ("auto"/"caller") comes back with
// the new canonical ones ("gates"/"off") after a fresh OpenStore — the
// migration behind the gate-approval vocabulary rename. The old values are
// written directly with a raw UPDATE (domain.Feature.Validate no longer
// accepts them, so CreateFeature can't be used to seed them), simulating a
// row persisted by a pre-rename binary.
func TestGateApprovalMigrationRewritesOldValues(t *testing.T) {
	w, err := Init(gitRoot(t), gitRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	autoFeat := feat(1, "Auto-gated feature")
	callerFeat := feat(2, "Caller-gated feature")
	if err := s.CreateFeature(ctx, autoFeat); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFeature(ctx, callerFeat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE features SET gate_approval = 'auto' WHERE id = ?`, string(autoFeat.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE features SET gate_approval = 'caller' WHERE id = ?`, string(callerFeat.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(w.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })

	got, err := s2.GetFeature(ctx, autoFeat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateGates {
		t.Errorf("migrated gate-approval (was 'auto') = %q, want %q", got.GateApproval, domain.GateGates)
	}
	got, err = s2.GetFeature(ctx, callerFeat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateApproval != domain.GateOff {
		t.Errorf("migrated gate-approval (was 'caller') = %q, want %q", got.GateApproval, domain.GateOff)
	}
}
