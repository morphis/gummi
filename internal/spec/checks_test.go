package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestParseChecks(t *testing.T) {
	doc := "# spec\n\n## Verification plan\n\n```gummi-checks\n" +
		"- name: build\n  cmd: go build ./...\n" +
		"- cmd: go test ./...\n" + // name defaults to cmd
		"- name: empty\n" + // no cmd: dropped
		"```\n\nprose after\n"
	checks, found, err := ParseChecks(doc)
	if !found {
		t.Fatal("block not found")
	}
	if err != nil {
		t.Fatalf("well-formed block errored: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	if checks[0].Name != "build" || checks[0].Cmd != "go build ./..." {
		t.Errorf("check 0 = %+v", checks[0])
	}
	if checks[1].Name != "go test ./..." {
		t.Errorf("unnamed check should default name to cmd: %+v", checks[1])
	}
}

func TestParseChecksAbsent(t *testing.T) {
	if _, found, _ := ParseChecks("# spec\n\n## Verification plan\n"); found {
		t.Error("found a block in a doc without one")
	}
}

func TestParseChecksMalformedYAMLStillFound(t *testing.T) {
	doc := "```gummi-checks\n\t: not yaml [\n```\n"
	checks, found, err := ParseChecks(doc)
	if !found {
		t.Error("a malformed block still exists — found should be true")
	}
	if len(checks) != 0 {
		t.Errorf("malformed block yielded checks: %+v", checks)
	}
	if err == nil {
		t.Error("malformed YAML should surface an error, not read as empty")
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	in := []domain.Check{
		{Name: "build", Cmd: "go build ./..."},
		{Name: "tricky", Cmd: `sh -c "echo 'a: b' && exit 1"`},
	}
	out, found, _ := ParseChecks(RenderChecks(in))
	if !found || len(out) != 2 {
		t.Fatalf("round trip lost checks: %+v", out)
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("check %d: got %+v want %+v", i, out[i], in[i])
		}
	}
}

func TestUpsertChecksInsertsUnderVerification(t *testing.T) {
	f := &domain.Feature{ID: "FD-001", Num: 1, Title: "t", Slug: "t", Kind: domain.KindFeature}
	doc := Template(f)
	out, err := UpsertChecks(doc, []domain.Check{{Name: "test", Cmd: "go test ./..."}})
	if err != nil {
		t.Fatal(err)
	}
	idx := strings.Index(out, "## Verification plan")
	blk := strings.Index(out, "```gummi-checks")
	if idx == -1 || blk == -1 || blk < idx {
		t.Fatalf("block not inserted under the Verification section:\n%s", out)
	}
	checks, found, _ := ParseChecks(out)
	if !found || len(checks) != 1 || checks[0].Cmd != "go test ./..." {
		t.Fatalf("parse-back = %+v (found=%v)", checks, found)
	}
	// the section's seeded %% prompt survives below the block
	if !strings.Contains(out, "feature-specific live checks") {
		t.Error("verification prompt lost on upsert")
	}
}

func TestUpsertChecksBugReport(t *testing.T) {
	f := &domain.Feature{ID: "BG-001", Num: 1, Title: "b", Slug: "b", Kind: domain.KindBug}
	out, err := UpsertChecks(BugTemplate(f), []domain.Check{{Name: "test", Cmd: "make test"}})
	if err != nil {
		t.Fatal(err)
	}
	if checks, found, _ := ParseChecks(out); !found || len(checks) != 1 {
		t.Fatalf("parse-back = %+v (found=%v)", checks, found)
	}
}

func TestUpsertChecksReplacesExisting(t *testing.T) {
	f := &domain.Feature{ID: "FD-001", Num: 1, Title: "t", Slug: "t", Kind: domain.KindFeature}
	doc, err := UpsertChecks(Template(f), []domain.Check{{Name: "old", Cmd: "false"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := UpsertChecks(doc, []domain.Check{{Name: "new", Cmd: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "```gummi-checks") != 1 {
		t.Fatalf("upsert duplicated the block:\n%s", out)
	}
	checks, _, _ := ParseChecks(out)
	if len(checks) != 1 || checks[0].Name != "new" {
		t.Errorf("old block not replaced: %+v", checks)
	}
}

func TestUpsertChecksNoSectionErrors(t *testing.T) {
	if _, err := UpsertChecks("# doc without sections\n", []domain.Check{{Cmd: "true"}}); err == nil {
		t.Error("expected an error for a doc without a Verification section")
	}
}
