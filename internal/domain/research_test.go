package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"
)

func TestNewResearchID(t *testing.T) {
	cases := []struct {
		n    int
		want FeatureID
		err  bool
	}{
		{7, "RS-007", false},
		{1000, "RS-1000", false},
		{0, "", true},
	}
	for _, c := range cases {
		got, err := NewID(KindResearch, c.n)
		if (err != nil) != c.err {
			t.Errorf("NewID(research, %d) error = %v, want err=%v", c.n, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("NewID(research, %d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func researchFeature() Feature {
	now := time.Now()
	return Feature{
		ID: "RS-007", Num: 7, Kind: KindResearch, Title: "Widget perf",
		OneLiner: "study", Slug: "my-topic", Stage: StageInvestigate,
		Profile: "thrifty", CreatedAt: now, UpdatedAt: now,
	}
}

func TestResearchArtifactPath(t *testing.T) {
	f := researchFeature()
	if got := f.ArtifactPath(); got != ".gummi/research/RS-007-my-topic.md" {
		t.Errorf("research ArtifactPath = %q", got)
	}
	if got := f.BranchName(); got != "gummi/RS-007-my-topic" {
		t.Errorf("research BranchName = %q", got)
	}
}

func TestResearchValidate(t *testing.T) {
	f := researchFeature()
	if err := f.Validate(); err != nil {
		t.Fatalf("valid research card rejected: %v", err)
	}
	mutations := map[string]func(*Feature){
		"unknown kind":    func(f *Feature) { f.Kind = "shipping" },
		"id/num mismatch": func(f *Feature) { f.Num = 99 },
		"empty title":     func(f *Feature) { f.Title = "  " },
		"bad slug":        func(f *Feature) { f.Slug = "../evil" },
		"invalid stage":   func(f *Feature) { f.Stage = "shipping" },
		"neg envelope":    func(f *Feature) { f.Budget.Envelope = -1 },
	}
	for name, mut := range mutations {
		g := researchFeature()
		mut(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

// TestNoResearchSkipFlag locks the absence of a quick/one-pass route over
// investigate: SkipFlags carries exactly the five creation flags, none of
// them named Investigate.
func TestNoResearchSkipFlag(t *testing.T) {
	rt := reflect.TypeOf(SkipFlags{})
	if n := rt.NumField(); n != 5 {
		t.Errorf("SkipFlags has %d fields, want 5 (brainstorm, plan, triage, diagnose, quick)", n)
	}
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "Investigate" {
			t.Error("SkipFlags must not gain an Investigate field — no quick one-pass research route")
		}
	}
}

// TestResearchIDDistinct locks the shared monotonic counter: RS ids draw
// the same number space as FD/BG, so RS-NNN never collides with FD-NNN or
// BG-NNN for the same n.
func TestResearchIDDistinct(t *testing.T) {
	for _, n := range []int{1, 7, 42} {
		r, err1 := NewID(KindResearch, n)
		f, err2 := NewID(KindFeature, n)
		b, err3 := NewID(KindBug, n)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("NewID(%d) errored: %v %v %v", n, err1, err2, err3)
		}
		ids := []FeatureID{r, f, b}
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if ids[i] == ids[j] {
					t.Errorf("ids %q and %q collide for n=%d", ids[i], ids[j], n)
				}
			}
		}
	}
}

// renderResearchSurface renders a research card's domain surface so the
// new kind, prefix, artifact home, stage set, and superstate are reviewable
// as a golden.
func renderResearchSurface(f Feature) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind: %s\n", f.Kind)
	fmt.Fprintf(&b, "id: %s\n", f.ID)
	fmt.Fprintf(&b, "prefix: %s\n", f.ID.Kind().prefix())
	fmt.Fprintf(&b, "artifact: %s\n", f.ArtifactPath())
	fmt.Fprintf(&b, "stages: %s %s\n", StageInvestigate, StageShape)
	fmt.Fprintf(&b, "superstate: %s\n", f.Stage.SuperState())
	return b.String()
}

func TestResearchKindSurface(t *testing.T) {
	f := researchFeature()
	golden.RequireEqual(t, []byte(renderResearchSurface(f)))
}

// TestResearchSeedShape locks the seed surface a creation form (FD-083)
// drives: exactly the two request-owned sections — Brief and Questions —
// and nothing the investigate/shape stages are supposed to own.
func TestResearchSeedShape(t *testing.T) {
	tp := reflect.TypeOf(ResearchSeed{})
	var fields []string
	for i := 0; i < tp.NumField(); i++ {
		fields = append(fields, tp.Field(i).Name)
	}
	want := []string{"Brief", "Questions"}
	if len(fields) != len(want) || !reflect.DeepEqual(fields, want) {
		t.Errorf("ResearchSeed fields = %v, want %v", fields, want)
	}
	got := ResearchSeed{Brief: "ask", Questions: []string{"why?"}}
	if got.Brief != "ask" || got.Questions[0] != "why?" {
		t.Errorf("ResearchSeed field round-trip failed: %+v", got)
	}
}
