package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// bugFeatureAt mints a bug-kind work item at the requested stage, mirroring
// the engine package test helper.
func bugFeatureAt(num int, title string, stage domain.Stage) domain.Feature {
	id, _ := domain.NewID(domain.KindBug, num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return domain.Feature{
		ID: id, Num: num, Kind: domain.KindBug, Title: title, Slug: slug, Stage: stage,
		CreatedAt: now, UpdatedAt: now,
	}
}

// createBug mints and persists a bug directly in the store, creates its
// worktree, and writes its bug report to the workspace. The driver can then
// be invoked with Driver.Drive on the returned feature.
func (h *harness) createBug(t *testing.T, title string, stage domain.Stage, report string) domain.Feature {
	t.Helper()
	ctx := context.Background()
	num, err := h.store.MintFeatureNum(ctx, h.ws.SeqFile())
	if err != nil {
		t.Fatalf("mint feature num: %v", err)
	}
	f := bugFeatureAt(num, title, stage)
	if _, err := h.wt.Create(ctx, &f); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	p := filepath.Join(h.root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	content := "# " + string(f.ID) + "\n\n## Verification\n\n" + report + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := h.store.CreateFeature(ctx, &f); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return f
}

// A bug whose Verification section tags no [env:] live checks, with a
// prerequisite probed PRESENT, cannot reach pass via the gummi run path:
// the omission gate downgrades the raw pass to blocked.
func TestDriverOmissionGateBlocksBugPass(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})
	if err := os.WriteFile(h.ws.ConfigFile(), []byte("env:\n  docker:\n    probe: \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := h.createBug(t, "driver omission gate", domain.StageVerify, "Run local unit tests only.")
	out, err := h.driver(Options{}).Drive(context.Background(), f)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}

	var verifyResult string
	for _, e := range h.events() {
		if e["event"] == "stage" && e["stage"] == "verify" {
			if r, ok := e["result"].(string); ok {
				verifyResult = r
			}
		}
	}
	if verifyResult != "blocked" {
		t.Fatalf("verify result = %q, want blocked; events=%v", verifyResult, h.events())
	}
	if out.Status == StatusDone {
		t.Fatalf("status = done, want non-terminal/blocked; omission gate must not let a bug pass")
	}
}
