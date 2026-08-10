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
