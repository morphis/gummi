package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// unmappedResearchDoc is a research document whose one question no slice
// and no out-of-scope line answers — a document the done edge refuses.
const unmappedResearchDoc = "# RS-001: lines semantics\n\n## Questions\n\n" +
	"- Does Lines count a trailing newline as a line?\n\n" +
	"## Findings\n\nLines splits on newline.\n"

// runResearchVerify runs a research card's verify stage over doc with an
// agent that always says pass, and returns the session and the kickoff
// the verifier received.
func runResearchVerify(t *testing.T, doc string) (*Session, string) {
	t.Helper()
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var kickoff string
	fk := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		if kickoff == "" {
			kickoff = msg
		}
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "all good\nVERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	fk.Caps.ReadOnlyEnforce = true
	e := New(Config{Agents: singleAgent(fk), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "lines semantics", domain.StageVerify)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	dir := ws.DraftsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.DraftFilename(&f)), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	mu.Lock()
	defer mu.Unlock()
	return e.Get(f.ID), kickoff
}

// TestResearchVerifyCannotPassADocumentTheFloorRejects is the regression
// for a real research card whose verify passed and whose "mark done" was
// then refused by the document floor, five unmapped questions and not a
// word about the rule anywhere before the refusal. The floor now runs at
// both ends of the verify stage: its report (and the rule) opens the
// kickoff, and a failing floor at the end overrules the agent's pass,
// with the report on the thread as a failed check.
func TestResearchVerifyCannotPassADocumentTheFloorRejects(t *testing.T) {
	s, kickoff := runResearchVerify(t, unmappedResearchDoc)

	for _, want := range []string{"deterministic floor", "FAILS", "Does Lines count a trailing newline as a line?", "## Slices"} {
		if !strings.Contains(kickoff, want) {
			t.Errorf("verify kickoff does not carry %q:\n%s", want, kickoff)
		}
	}
	snap := s.Snapshot()
	if snap.VerdictFloor != "fail" {
		t.Fatalf("verdict floor = %q, want fail: a pass on a document the floor rejects is the bug", snap.VerdictFloor)
	}
	if !strings.Contains(snap.VerdictFloorReason, "1 unmapped question") {
		t.Errorf("floor reason = %q, want the floor's own count", snap.VerdictFloorReason)
	}
	acts := strings.Join(snap.Activity, "\n")
	if !strings.Contains(acts, "check document floor: FAIL") {
		t.Errorf("no failed document-floor check row in the activity:\n%s", acts)
	}
}

// TestResearchVerifyRecordsAPassingFloor: a document that meets the floor
// keeps the agent's verdict and says so on the thread.
func TestResearchVerifyRecordsAPassingFloor(t *testing.T) {
	doc := unmappedResearchDoc + "\n## Out of scope\n\n" +
		"- Does Lines count a trailing newline as a line: settled by the existing tests\n"
	s, kickoff := runResearchVerify(t, doc)
	if !strings.Contains(kickoff, "it passes") {
		t.Errorf("kickoff does not report the passing floor:\n%s", kickoff)
	}
	snap := s.Snapshot()
	if snap.VerdictFloor != "" {
		t.Errorf("verdict floor = %q on a document that meets the floor", snap.VerdictFloor)
	}
	if acts := strings.Join(snap.Activity, "\n"); !strings.Contains(acts, "check document floor: pass") {
		t.Errorf("no passing document-floor check row:\n%s", acts)
	}
}

// TestResearchStagesAreToldTheFloor: the rule reaches every research
// stage's hints, including a research verify's own contract, which used
// to be the feature one (gummi-checks, INV- lines, "land on main").
func TestResearchStagesAreToldTheFloor(t *testing.T) {
	for _, st := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		f := feature(1, "rs", st)
		f.ID, f.Kind = "RS-001", domain.KindResearch
		all := strings.Join(stageHints(f, "/doc.md", "", flavorStage), "\n")
		if !strings.Contains(all, "requirements") || !strings.Contains(all, "## Out of scope") {
			t.Errorf("%s: research hints do not state the coverage rule", st)
		}
		if st == domain.StageVerify && strings.Contains(all, "INV-1") {
			t.Errorf("research verify is still served the feature verify contract")
		}
	}
	f := feature(1, "rs", domain.StageImplement)
	f.ID, f.Kind = "RS-001", domain.KindResearch
	if all := strings.Join(stageHints(f, "/doc.md", "", flavorCritique), "\n"); !strings.Contains(all, "requirements") {
		t.Error("the research critique is not told the coverage rule")
	}
}
