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

func TestRenderParseRoundTripOptionalFields(t *testing.T) {
	// unset Timeout/Baseline must not emit keys (omitempty)
	in := []domain.Check{{Name: "plain", Cmd: "true"}}
	rendered := RenderChecks(in)
	if strings.Contains(rendered, "timeout:") || strings.Contains(rendered, "baseline:") {
		t.Fatalf("omitempty fields emitted: %s", rendered)
	}
	out, found, err := ParseChecks(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(out) != 1 || out[0].Timeout != "" || out[0].Baseline != nil {
		t.Errorf("round trip mismatch: %+v", out)
	}

	// set values round-trip byte-stable
	f := false
	in2 := []domain.Check{{Name: "configured", Cmd: "true", Timeout: "90s", Baseline: &f}}
	rendered2 := RenderChecks(in2)
	out2, found2, err2 := ParseChecks(rendered2)
	if err2 != nil {
		t.Fatal(err2)
	}
	if !found2 || len(out2) != 1 {
		t.Fatalf("round trip lost checks: %+v", out2)
	}
	if out2[0].Timeout != "90s" || out2[0].Baseline == nil || *out2[0].Baseline != false {
		t.Errorf("round trip mismatch: %+v", out2[0])
	}
}

func TestParseChecksRejectsBadTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout string
	}{
		{name: "malformed", timeout: "soon"},
		{name: "over-ceiling", timeout: "31m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "```gummi-checks\n- name: bad\n  cmd: \"true\"\n  timeout: " + tc.timeout + "\n```\n"
			_, _, err := ParseChecks(doc)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), `"bad"`) {
				t.Errorf("error should name check: %v", err)
			}
		})
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

// TestParseChecksRepairsAgentMistakes covers the three malformations
// observed in real artifacts. Each one used to take the whole block down
// — and with it the approval-time baseline — for a block whose intent
// was never in doubt.
func TestParseChecksRepairsAgentMistakes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []domain.Check
	}{{
		name: "skip tag inside the block",
		// [env: docker] carries a colon-space: "mapping values are not
		// allowed in this context" at the block's 4th line.
		body: "- name: build\n  cmd: go build ./...\n- name: e2e\n  cmd: ./e2e.sh [env: docker]\n",
		want: []domain.Check{{Name: "build", Cmd: "go build ./..."}, {Name: "e2e", Cmd: "./e2e.sh"}},
	}, {
		name: "CI-only tag inside the block",
		body: "- name: build\n  cmd: go build ./... [CI-only]\n",
		want: []domain.Check{{Name: "build", Cmd: "go build ./..."}},
	}, {
		name: "reviewer marker inside the fence",
		body: "- name: lint\n  cmd: make lint\n%% @reviewer(2026-09-06): PASS: all green\n",
		want: []domain.Check{{Name: "lint", Cmd: "make lint"}},
	}, {
		name: "unquoted colon in a command",
		body: "- name: note\n  cmd: echo done: ok\n",
		want: []domain.Check{{Name: "note", Cmd: "echo done: ok"}},
	}, {
		name: "unquoted colon in a name",
		body: "- name: verify: repro gone\n  cmd: ./repro.sh\n",
		want: []domain.Check{{Name: "verify: repro gone", Cmd: "./repro.sh"}},
	}, {
		name: "continuation key flush left",
		// "with cmd: <command> on the next line", taken literally
		body: "- name: build\ncmd: go build ./...\n- name: test\ncmd: go test ./...\n",
		want: []domain.Check{{Name: "build", Cmd: "go build ./..."}, {Name: "test", Cmd: "go test ./..."}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			checks, found, err := ParseChecks("```gummi-checks\n" + tc.body + "```\n")
			if !found {
				t.Fatal("block not found")
			}
			if err != nil {
				t.Fatalf("repairable block still errored: %v", err)
			}
			if len(checks) != len(tc.want) {
				t.Fatalf("checks = %+v, want %+v", checks, tc.want)
			}
			for i := range tc.want {
				if checks[i] != tc.want[i] {
					t.Errorf("check %d = %+v, want %+v", i, checks[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseChecksRepairLeavesGoodBlocksAlone guards against the repair
// rewriting a block that was already correct.
func TestParseChecksRepairLeavesGoodBlocksAlone(t *testing.T) {
	f := false
	in := []domain.Check{
		{Name: "build", Cmd: "go build ./..."},
		{Name: "tricky", Cmd: `sh -c "echo 'a: b' && exit 1"`},
		{Name: "npm", Cmd: "npm run test:unit"},
		{Name: "new", Cmd: "go test ./internal/ui/ -run TestX", Timeout: "6m", Baseline: &f},
	}
	out, _, err := ParseChecks(RenderChecks(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("got %+v", out)
	}
	for i := range in {
		if out[i].Name != in[i].Name || out[i].Cmd != in[i].Cmd || out[i].Timeout != in[i].Timeout {
			t.Errorf("check %d = %+v, want %+v", i, out[i], in[i])
		}
	}
	if _, changed := repairChecksBlock("- name: build\n  cmd: go build ./...\n"); changed {
		t.Error("repair touched a well-formed block")
	}
}

// TestChecksErrorQuotesTheOffendingLine: "line 4" counts from inside the
// fence, so the reader has nothing to count against — the text has to
// come with it.
func TestChecksErrorQuotesTheOffendingLine(t *testing.T) {
	doc := "```gummi-checks\n- name: build\n  cmd: go build ./...\n- name: broken\n  cmd: [oops\n```\n"
	_, found, err := ParseChecks(doc)
	if !found || err == nil {
		t.Fatalf("expected a surfaced error, got found=%v err=%v", found, err)
	}
	if !strings.Contains(err.Error(), "block line") {
		t.Errorf("error does not say the line is fence-relative: %v", err)
	}
	if !strings.Contains(err.Error(), "name: broken") {
		t.Errorf("error does not quote the offending line: %v", err)
	}
}

// TestParseChecksRepairsTabIndent: YAML forbids tabs in indentation, and
// an agent that reaches for one takes the block down with it.
func TestParseChecksRepairsTabIndent(t *testing.T) {
	checks, _, err := ParseChecks("```gummi-checks\n- name: build\n\tcmd: go build ./...\n```\n")
	if err != nil {
		t.Fatalf("tab-indented block still errored: %v", err)
	}
	if len(checks) != 1 || checks[0].Cmd != "go build ./..." {
		t.Errorf("checks = %+v", checks)
	}
}

// TestParseChecksRepairLeavesBlockScalarsAlone: a literal scalar's body
// is the command, not structure — a line inside it that happens to open
// with "cmd: " must survive verbatim.
func TestParseChecksRepairLeavesBlockScalarsAlone(t *testing.T) {
	doc := "```gummi-checks\n" +
		"- name: multi\n" +
		"  cmd: |\n" +
		"    go test ./...\n" +
		"    echo cmd: done\n" +
		"- name: after\n" +
		"  cmd: go vet ./... [env: go]\n" +
		"```\n"
	checks, _, err := ParseChecks(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	if !strings.Contains(checks[0].Cmd, "echo cmd: done") {
		t.Errorf("block scalar body was rewritten: %q", checks[0].Cmd)
	}
	// and the entry after the scalar is still repaired
	if checks[1].Cmd != "go vet ./..." {
		t.Errorf("repair did not resume after the block scalar: %q", checks[1].Cmd)
	}
}
