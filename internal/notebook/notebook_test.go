package notebook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheReferenceIsTheOwnersAndSaysWhenItChanged(t *testing.T) {
	n := Open(filepath.Join(t.TempDir(), "GL-001"))
	src := filepath.Join(t.TempDir(), "design.html")
	if err := os.WriteFile(src, []byte("three tiers"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !n.Empty() || n.Index() != "" {
		t.Fatal("a goal that knows nothing says nothing")
	}
	if err := n.AddReference(src); err != nil {
		t.Fatal(err)
	}
	if err := n.Pin(); err != nil {
		t.Fatal(err)
	}
	if refs := n.Reference(); len(refs) != 1 || refs[0].Name != "design.html" || refs[0].Changed || refs[0].Missing {
		t.Fatalf("%+v", refs)
	}
	if err := os.WriteFile(filepath.Join(n.ReferenceDir(), "design.html"), []byte("two tiers"), 0o600); err != nil {
		t.Fatal(err)
	}
	if refs := n.Reference(); !refs[0].Changed || !strings.Contains(n.Index(), "CHANGED since the plan was agreed") {
		t.Fatalf("a reference that moved under the goal is reported wherever it is listed: %+v", refs)
	}
	_ = os.Remove(filepath.Join(n.ReferenceDir(), "design.html"))
	if refs := n.Reference(); len(refs) != 1 || !refs[0].Missing {
		t.Fatalf("%+v", refs)
	}
}

func TestTheRegistryHasOneValuePerKeyAndRemembersWhatItReplaced(t *testing.T) {
	n := Open(filepath.Join(t.TempDir(), "GL-001"))
	if _, err := n.Set(Entry{Key: "transit mac", Value: "x"}); err == nil {
		t.Fatal("a key is one word a spec can cite")
	}
	if _, err := n.Set(Entry{Key: "transit-mac", Value: ""}); err == nil {
		t.Fatal("a constant has a value")
	}
	prev, err := n.Set(Entry{Key: "transit-mac", Value: "0a:58:00:00:00:NN, NN = chassis index", Why: "symmetric", Decision: "D-2", At: time.Now()})
	if err != nil || prev != nil {
		t.Fatal(err, prev)
	}
	prev, err = n.Set(Entry{Key: "transit-mac", Value: "0a:58:64:00:00:NN", Decision: "D-5", At: time.Now()})
	if err != nil || prev == nil || !strings.HasPrefix(prev.Value, "0a:58:00") {
		t.Fatalf("%v %+v", err, prev)
	}
	reg := n.Registry()
	if len(reg) != 1 || reg[0].Value != "0a:58:64:00:00:NN" || !strings.HasPrefix(reg[0].Was, "0a:58:00") {
		t.Fatalf("%+v", reg)
	}
	raw, _ := os.ReadFile(filepath.Join(n.Dir(), "REGISTRY.md"))
	if !strings.Contains(string(raw), "## transit-mac") || !strings.Contains(string(raw), "_Replaced:_") {
		t.Fatalf("the file cards read:\n%s", raw)
	}
}

// A finding is never edited, only superseded, so a spec that cites F-1
// cites something that will always say what it said.
func TestFindingsAreAppendOnly(t *testing.T) {
	n := Open(filepath.Join(t.TempDir(), "GL-001"))
	if _, err := n.Record(Finding{Claim: "no-learning drops learned routes"}); err == nil {
		t.Fatal("a finding without evidence is an opinion")
	}
	f1, err := n.Record(Finding{Claim: "no-learning drops learned routes", Evidence: "RS-002 §3", Card: "RS-002"})
	if err != nil || f1.Ref() != "F-1" || f1.Status != Holds {
		t.Fatalf("%v %+v", err, f1)
	}
	if _, err := n.Record(Finding{Claim: "x", Evidence: "y", Supersedes: 9}); err == nil {
		t.Fatal("there is no F-9")
	}
	f2, err := n.Record(Finding{Claim: "no-learning only stops learning on that router; routes already learned stay", Evidence: "run 20260918T101500", Supersedes: 1})
	if err != nil || f2.N != 2 {
		t.Fatal(err, f2)
	}
	if _, err := n.Record(Finding{Claim: "z", Evidence: "y", Supersedes: 1}); err == nil {
		t.Fatal("F-1 already has a successor")
	}
	all := n.Findings()
	if all[0].Status != Superseded || all[0].SupersededBy != 2 || all[0].Claim != "no-learning drops learned routes" {
		t.Fatalf("the old one keeps its words: %+v", all[0])
	}
	idx := n.Index()
	if !strings.Contains(idx, "- F-1 superseded by F-2") || !strings.Contains(idx, "- F-2 (holds) no-learning only stops") {
		t.Fatalf("a line per entry:\n%s", idx)
	}
}

func TestAGoalThatContinuesAnotherStartsFromWhatItKnew(t *testing.T) {
	a := Open(filepath.Join(t.TempDir(), "GL-001"))
	src := filepath.Join(t.TempDir(), "design.html")
	_ = os.WriteFile(src, []byte("three tiers"), 0o600)
	_ = a.AddReference(src)
	_, _ = a.Set(Entry{Key: "vrf-provider", Value: "100", Decision: "D-1"})
	_, _ = a.Record(Finding{Claim: "old", Evidence: "e"})
	_, _ = a.Record(Finding{Claim: "new", Evidence: "e", Supersedes: 1})
	_, _ = a.Record(Finding{Claim: "wrong", Evidence: "e", Status: Refuted})

	b := Open(filepath.Join(t.TempDir(), "GL-002"))
	_, _ = b.Set(Entry{Key: "vrf-provider", Value: "101"})
	if err := b.Import(a, "GL-001"); err != nil {
		t.Fatal(err)
	}
	if reg := b.Registry(); len(reg) != 1 || reg[0].Value != "101" {
		t.Fatalf("what this goal already decided wins: %+v", reg)
	}
	fs := b.Findings()
	if len(fs) != 1 || fs[0].Claim != "new" || fs[0].From != "GL-001" || fs[0].N != 1 {
		t.Fatalf("what still held comes over, and says where from: %+v", fs)
	}
	if refs := b.Reference(); len(refs) != 1 {
		t.Fatalf("%+v", refs)
	}
}
