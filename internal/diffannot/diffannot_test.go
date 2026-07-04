package diffannot

import (
	"strings"
	"testing"
)

const sample = `diff --git a/foo.go b/foo.go
index 111..222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,5 @@
 package foo

-func Old() {}
+func New() {}
+func Extra() {}
 // tail
diff --git a/bar.go b/bar.go
index 333..444 100644
--- a/bar.go
+++ b/bar.go
@@ -1 +1 @@
-package bar
+package baz`

func lines() []string { return strings.Split(sample, "\n") }

func TestAnchorLocateRoundTrip(t *testing.T) {
	ls := lines()
	// anchor the "+func New() {}" line, then relocate it.
	idx := -1
	for i, l := range ls {
		if l == "+func New() {}" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("setup: line not found")
	}
	a := Anchor(ls, idx)
	if got := Locate(ls, a); got != idx {
		t.Errorf("Locate = %d, want %d", got, idx)
	}
}

func TestAnchorSurvivesUnrelatedEdit(t *testing.T) {
	ls := lines()
	idx := 0
	for i, l := range ls {
		if l == "+func New() {}" {
			idx = i
		}
	}
	a := Anchor(ls, idx)
	// a rebase shifts the hunk down by inserting a file before it, but the
	// ±2 context around the target line is unchanged → still located.
	shifted := append([]string{
		"diff --git a/zero.go b/zero.go",
		"--- a/zero.go",
		"+++ b/zero.go",
		"@@ -0,0 +1 @@",
		"+package zero",
	}, ls...)
	if got := Locate(shifted, a); got < 0 {
		t.Error("anchor orphaned by an unrelated preceding change")
	} else if shifted[got] != "+func New() {}" {
		t.Errorf("relocated to wrong line %q", shifted[got])
	}
}

func TestAnchorOrphansWhenContextChanges(t *testing.T) {
	ls := lines()
	a := Anchor(ls, 8)
	// rewrite the neighborhood entirely
	gone := []string{"diff --git a/x b/x", "+++ b/x", "@@ -1 +1 @@", "+totally different"}
	if got := Locate(gone, a); got != -1 {
		t.Errorf("expected orphan (-1), got %d", got)
	}
}

func TestFileAt(t *testing.T) {
	ls := lines()
	var fooLine, barLine int
	for i, l := range ls {
		if l == "+func Extra() {}" {
			fooLine = i
		}
		if l == "+package baz" {
			barLine = i
		}
	}
	if f := FileAt(ls, fooLine); f != "foo.go" {
		t.Errorf("FileAt(fooLine) = %q, want foo.go", f)
	}
	if f := FileAt(ls, barLine); f != "bar.go" {
		t.Errorf("FileAt(barLine) = %q, want bar.go", f)
	}
}
