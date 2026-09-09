package domain

import "testing"

func TestParseIssueRefShapes(t *testing.T) {
	cases := []struct {
		in   string
		want IssueRef
		ok   bool
	}{
		{"https://github.com/canonical/lxd/issues/18409", IssueRef{"canonical", "lxd", 18409}, true},
		{"  https://github.com/canonical/lxd/issues/18409/  ", IssueRef{"canonical", "lxd", 18409}, true},
		{"github.com/canonical/lxd/issues/18409#issuecomment-1", IssueRef{"canonical", "lxd", 18409}, true},
		{"canonical/lxd#18409", IssueRef{"canonical", "lxd", 18409}, true},
		{"#18409", IssueRef{Number: 18409}, true},
		// not references: a sentence, a pull request, trailing words, zero
		{"#18409 is flaky on arm64", IssueRef{}, false},
		{"https://github.com/canonical/lxd/pull/18409", IssueRef{}, false},
		{"fix the login loop", IssueRef{}, false},
		{"#0", IssueRef{}, false},
		{"", IssueRef{}, false},
	}
	for _, c := range cases {
		got, ok := ParseIssueRef(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseIssueRef(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if s := (IssueRef{"o", "r", 7}).String(); s != "o/r#7" {
		t.Errorf("String = %q", s)
	}
	if s := (IssueRef{Number: 7}).String(); s != "#7" {
		t.Errorf("bare String = %q", s)
	}
}

func TestSplitAcceptance(t *testing.T) {
	text := "Dark mode\n\nThe console is white at night.\n\n## Acceptance\n- a toggle in settings\n- remembered per user\n\n## Notes\nkeep it simple"
	rest, acc := SplitAcceptance(text)
	if acc != "- a toggle in settings\n- remembered per user" {
		t.Errorf("acceptance = %q", acc)
	}
	if rest != "Dark mode\n\nThe console is white at night.\n\n## Notes\nkeep it simple" {
		t.Errorf("rest = %q", rest)
	}
	// no heading: untouched
	if r, a := SplitAcceptance("just prose\n\nmore prose"); r != "just prose\n\nmore prose" || a != "" {
		t.Errorf("no heading: rest=%q acc=%q", r, a)
	}
	// the long spelling, case-insensitive, to the end of the text
	if _, a := SplitAcceptance("t\n\n### acceptance criteria\nx\ny"); a != "x\ny" {
		t.Errorf("long spelling acc = %q", a)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n  hello \nworld"); got != "hello" {
		t.Errorf("FirstLine = %q", got)
	}
}
