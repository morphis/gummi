package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

const sectionsFixture = `# FD-042: Dark mode

## Problem

Dark mode is missing.
%% @user(2026-07-03): per-device or synced?

## Out of scope

sync

## Considered approaches

Option A and option B.

## Chosen approach

A.
`

func TestViewSectionKnownSection(t *testing.T) {
	body, ok := ViewSection(sectionsFixture, "Problem")
	if !ok {
		t.Fatal("expected Problem section to be found")
	}
	want := "\nDark mode is missing.\n%% @user(2026-07-03): per-device or synced?\n\n"
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if strings.Contains(body, "## Problem") {
		t.Errorf("body must not include the heading line: %q", body)
	}
}

func TestViewSectionCaseInsensitiveMatch(t *testing.T) {
	for _, name := range []string{"considered approaches", "CONSIDERED APPROACHES", "Considered Approaches"} {
		body, ok := ViewSection(sectionsFixture, name)
		if !ok {
			t.Fatalf("expected %q to match ## Considered approaches", name)
		}
		if !strings.Contains(body, "Option A and option B.") {
			t.Errorf("body for %q = %q, want the Considered approaches body", name, body)
		}
	}
}

func TestViewSectionWholeDoc(t *testing.T) {
	body, ok := ViewSection(sectionsFixture, "")
	if !ok {
		t.Fatal("expected an empty name to succeed")
	}
	if body != sectionsFixture {
		t.Error("empty name should return the whole document unchanged")
	}
	if got, ok := ViewSection(sectionsFixture, "  "); !ok || got != sectionsFixture {
		t.Error("blank name should also return the whole document unchanged")
	}
}

func TestViewSectionUnknownSection(t *testing.T) {
	if _, ok := ViewSection(sectionsFixture, "Nope"); ok {
		t.Error("expected unknown section to report ok=false")
	}
}

func TestReplaceSectionSwapsBody(t *testing.T) {
	out, title, err := ReplaceSection(sectionsFixture, "Chosen approach", "new body")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Chosen approach" {
		t.Errorf("matchedTitle = %q, want Chosen approach", title)
	}
	if !strings.Contains(out, "new body") || strings.Contains(out, "A.\n") {
		t.Errorf("replacement body missing (or old body remains):\n%s", out)
	}
	if !strings.Contains(out, "## Problem\n\nDark mode is missing.") {
		t.Errorf("other sections must be untouched:\n%s", out)
	}
	if !strings.Contains(out, "Option A and option B.") {
		t.Errorf("preceding section must be untouched:\n%s", out)
	}
}

func TestReplaceSectionUnknownSection(t *testing.T) {
	if _, _, err := ReplaceSection(sectionsFixture, "Nope", "x"); err != ErrSectionUnknown {
		t.Errorf("err = %v, want ErrSectionUnknown", err)
	}
}

func TestReplaceSectionRejectsHeadingInBody(t *testing.T) {
	_, _, err := ReplaceSection(sectionsFixture, "Problem", "## Injected\nboom")
	if err != ErrSectionHeadingInBody {
		t.Errorf("err = %v, want ErrSectionHeadingInBody", err)
	}
}

func TestReplaceSectionAllowsIndentedHeadingInBody(t *testing.T) {
	body := "  ## Not a heading\ninside a code block"
	out, _, err := ReplaceSection(sectionsFixture, "Problem", body)
	if err != nil {
		t.Errorf("err = %v, want no error for indented heading", err)
		return
	}
	if !strings.Contains(out, body) {
		t.Errorf("indented heading body missing:\n%s", out)
	}
}

func TestReplaceSectionCanonicalTitle(t *testing.T) {
	_, title, err := ReplaceSection(sectionsFixture, "problem", "x")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Problem" {
		t.Errorf("matchedTitle = %q, want on-disk heading text Problem", title)
	}
}

func TestReplaceSectionRoundTripByteIdentical(t *testing.T) {
	f := &domain.Feature{ID: "FD-042", Num: 42, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageTodo}
	tpl := Template(f)
	for _, name := range []string{
		"Problem", "Out of scope", "Considered approaches", "Chosen approach",
		"Implementation notes", "Progress", "Review", "Verification plan",
	} {
		body, ok := ViewSection(tpl, name)
		if !ok {
			t.Fatalf("section %q not found in template", name)
		}
		out, title, err := ReplaceSection(tpl, name, body)
		if err != nil {
			t.Fatalf("replace %q: %v", name, err)
		}
		if title != name {
			t.Errorf("matchedTitle = %q, want %q", title, name)
		}
		if out != tpl {
			t.Errorf("round-trip of %q not byte-identical", name)
		}
	}
}

// TestReplaceSectionDoesNotWeldNextHeading is the regression for the bug
// where a replacement body without a trailing newline was spliced verbatim
// onto the following `## ` heading, welding it mid-line so it stopped
// parsing as a heading and its section became invisible to ViewSection.
func TestReplaceSectionDoesNotWeldNextHeading(t *testing.T) {
	const doc = "## Summary\nold\n\n## Environment\nenv\n"
	out, _, err := ReplaceSection(doc, "summary", "new body") // note: no trailing \n
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "new body## Environment") {
		t.Fatalf("body was welded onto the next heading:\n%s", out)
	}
	body, ok := ViewSection(out, "environment")
	if !ok {
		t.Fatalf("Environment section vanished:\n%s", out)
	}
	if want := "env\n"; body != want {
		t.Errorf("Environment body = %q, want %q", body, want)
	}
}

// TestReplaceSectionTrailingNewlineBoundary covers the splice boundary
// matrix: bodies with 0, 1, and several trailing newlines, an empty body,
// and a last-section-at-EOF replacement. In every case the following
// section (or the section itself at EOF) must remain addressable and the
// splice must not weld the next heading.
func TestReplaceSectionTrailingNewlineBoundary(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no trailing newline", "new body", "new body\n"},
		{"single trailing newline", "new body\n", "new body\n"},
		{"several trailing newlines", "new body\n\n\n", "new body\n\n\n"},
		{"empty body", "", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const doc = "## Summary\nold\n\n## Environment\nenv\n\n## Fix\nfix\n"
			out, _, err := ReplaceSection(doc, "summary", tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("replacement body %q not spliced with %q:\n%s", tc.body, tc.want, out)
			}
			if _, ok := ViewSection(out, "environment"); !ok {
				t.Fatalf("Environment section vanished:\n%s", out)
			}
			if _, ok := ViewSection(out, "fix"); !ok {
				t.Fatalf("Fix section vanished:\n%s", out)
			}
		})
	}

	t.Run("last section at EOF", func(t *testing.T) {
		const doc = "## Summary\nold\n\n## Fix\nfix\n"
		out, _, err := ReplaceSection(doc, "fix", "new fix")
		if err != nil {
			t.Fatal(err)
		}
		body, ok := ViewSection(out, "fix")
		if !ok {
			t.Fatalf("Fix section vanished at EOF:\n%s", out)
		}
		if want := "new fix\n"; body != want {
			t.Errorf("Fix body = %q, want %q", body, want)
		}
	})

	t.Run("round-trip following section", func(t *testing.T) {
		const doc = "## Summary\nold\n\n## Environment\nenv\n\n## Fix\nfix\n"
		out, _, err := ReplaceSection(doc, "summary", "new body")
		if err != nil {
			t.Fatal(err)
		}
		envBody, ok := ViewSection(out, "environment")
		if !ok {
			t.Fatalf("Environment section vanished:\n%s", out)
		}
		// Replacing the following section with its own viewed body must not
		// corrupt the section after it.
		out2, _, err := ReplaceSection(out, "environment", envBody)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ViewSection(out2, "fix"); !ok {
			t.Fatalf("Fix section vanished after round-trip:\n%s", out2)
		}
	})
}

// TestHealWeldedHeadingsRepairsCorruption is the regression for the repair
// path required alongside the splice-normalization fix: an artifact already
// corrupted by a pre-fix write (a `## ` heading welded mid-line so it lost
// column-0 identity) must be recoverable. HealWeldedHeadings re-splits the
// welded heading back onto its own line, making its section addressable
// again, and is idempotent.
func TestHealWeldedHeadingsRepairsCorruption(t *testing.T) {
	const welded = "## Summary\nnew body## Environment\nenv\n"
	healed := HealWeldedHeadings(welded)
	if strings.Contains(healed, "new body## Environment") {
		t.Fatalf("heading still welded:\n%q", healed)
	}
	body, ok := ViewSection(healed, "environment")
	if !ok {
		t.Fatalf("Environment section not addressable after heal:\n%q", healed)
	}
	if want := "env\n"; body != want {
		t.Errorf("Environment body = %q, want %q", body, want)
	}
	// A second pass must be a no-op: the healer is idempotent.
	if again := HealWeldedHeadings(healed); again != healed {
		t.Errorf("healer not idempotent:\n%q vs\n%q", again, healed)
	}
}

func TestHealWeldedHeadingsLeavesCleanDocUntouched(t *testing.T) {
	const clean = "## Summary\nnew body\n\n## Environment\nenv\n"
	if out := HealWeldedHeadings(clean); out != clean {
		t.Errorf("clean doc changed:\n%q", out)
	}
}

func TestHealWeldedHeadingsRepeatedWelds(t *testing.T) {
	const welded = "## Summary\na## Environment\nb## Fix\nc\n"
	healed := HealWeldedHeadings(welded)
	for _, name := range []string{"environment", "fix"} {
		if _, ok := ViewSection(healed, name); !ok {
			t.Fatalf("%s section not addressable after heal:\n%q", name, healed)
		}
	}
}

// TestHealWeldedHeadingsLeavesCodeBlocksUntouched guards against the healer
// corrupting legitimate content: a `## ` inside a fenced code block or an
// indented code block (e.g. a reproduction snippet containing a string
// literal such as `"## Summary\nold\n"`) is real content, not a welded
// heading, and must survive byte-identical. The splice-normalization fix
// means no new welds occur, so the healer only ever runs on genuinely
// corrupted artifacts and must never rewrite verbatim content into
// column-0 headings.
func TestHealWeldedHeadingsLeavesCodeBlocksUntouched(t *testing.T) {
	const doc = "## Problem\nold\n\nFenced:\n```go\nconst doc = \"## Summary\\nold\\n\"\n```\n\nIndented:\n    const x = \"## Summary\\nold\\n\"\n\n## Review\nnew\n"
	out := HealWeldedHeadings(doc)
	if out != doc {
		t.Fatalf("healer rewrote code-block content:\n%q\nwant:\n%q", out, doc)
	}
	if _, ok := ViewSection(out, "problem"); !ok {
		t.Fatalf("Problem section vanished:\n%q", out)
	}
	if _, ok := ViewSection(out, "review"); !ok {
		t.Fatalf("Review section vanished:\n%q", out)
	}
}

// TestHealWeldedHeadingsLeavesInlineCodeUntouched guards the healer against
// rewriting prose that references a section heading as inline backtick code.
// A “ `## Problem` “ in regular text is legitimate content, not a welded
// heading, and must survive byte-identical; splitting it out to column 0
// would promote it into a real structural heading and leave an unbalanced
// backtick in the source line.
func TestHealWeldedHeadingsLeavesInlineCodeUntouched(t *testing.T) {
	const doc = "## Summary\nold\n\nA spec must contain a `## Problem` heading at column 0.\n\n## Fix\nfix\n"
	out := HealWeldedHeadings(doc)
	if out != doc {
		t.Fatalf("healer rewrote inline-code content:\n%q\nwant:\n%q", out, doc)
	}
	if _, ok := ViewSection(out, "summary"); !ok {
		t.Fatalf("Summary section vanished:\n%q", out)
	}
	if _, ok := ViewSection(out, "fix"); !ok {
		t.Fatalf("Fix section vanished:\n%q", out)
	}
}

// TestHealWeldedHeadingsSplitsMultipleWeldsPerLine guards the healer against
// stopping at the first weld on a line. A pre-fix artifact can have two (or
// more) headings welded onto one body line (e.g.
// `findings land here## Verification## Expected vs actual`); every welded
// heading must be split back onto its own line so each section is addressable
// again, and a second pass must be a no-op.
func TestHealWeldedHeadingsSplitsMultipleWeldsPerLine(t *testing.T) {
	const in = "## Review\nfindings land here## Verification## Expected vs actual\nbody\n"
	healed := HealWeldedHeadings(in)
	for _, name := range []string{"review", "verification", "expected vs actual"} {
		if _, ok := ViewSection(healed, name); !ok {
			t.Fatalf("%s section not addressable after heal:\n%q", name, healed)
		}
	}
	if again := HealWeldedHeadings(healed); again != healed {
		t.Errorf("healer not idempotent on multi-weld line:\n%q vs\n%q", again, healed)
	}
}

// TestHealWeldedHeadingsLeavesProseUntouched guards the healer against
// rewriting ordinary prose: an unindented line containing a `## ` whose title
// is not one of the template's fixed sections (outside backticks and code
// blocks) is a sentence, not a welded heading, and must be returned
// byte-identical. Without this, the healer silently rewrote prose into
// column-0 headings on every write.
func TestHealWeldedHeadingsLeavesProseUntouched(t *testing.T) {
	const doc = "## Summary\nSections are marked with ## Foo in markdown.\n\n## Fix\nfix\n"
	if out := HealWeldedHeadings(doc); out != doc {
		t.Fatalf("healer rewrote prose containing a non-template heading:\n%q\nwant:\n%q", out, doc)
	}
}

// TestHealWeldedHeadingsKnownTitleInProseIsResidual pins down the one
// accepted residual of the weld discriminator. A prose line ending in a known
// template title (`## review`, `## summary`, …) at end of line is
// byte-identical to a genuine weld: the glued title runs to the end of the
// line and there is no remaining text to prove it is prose. There is no
// rule narrower than title-membership that still repairs genuine bug-template
// welds (whose headings are not preceded by a blank line), so this prose is
// deliberately split back to column 0. The test locks in that behaviour so a
// future "fix" that silently stops healing real welds cannot slip through
// unnoticed, and documents that the residual never loses data — it only
// re-formats a single line.
func TestHealWeldedHeadingsKnownTitleInProseIsResidual(t *testing.T) {
	const doc = "## Summary\nWe will do the final ## review\n\n## Fix\nfix\n"
	healed := HealWeldedHeadings(doc)
	if strings.Contains(healed, "final ## review") {
		t.Fatalf("known-title prose was not split (residual changed):\n%q", healed)
	}
	// Both sections must stay addressable after the residual split, and a
	// second pass must be a no-op (idempotent).
	if _, ok := ViewSection(healed, "fix"); !ok {
		t.Fatalf("Fix section vanished after healer:\n%q", healed)
	}
	if again := HealWeldedHeadings(healed); again != healed {
		t.Errorf("healer not idempotent after residual split:\n%q vs\n%q", again, healed)
	}
}
