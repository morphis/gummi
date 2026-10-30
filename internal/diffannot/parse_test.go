package diffannot

import (
	"reflect"
	"testing"
)

const parseSample = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@ package main
 package main

-func a() {}
+func a() { b() }
+func b() {}
 // end
\ No newline at end of file
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+hello
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 4444444..0000000
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-bye
-now
diff --git a/old name.md b/new name.md
similarity index 90%
rename from old name.md
rename to new name.md
index 5555555..6666666 100644
--- a/old name.md
+++ b/new name.md
@@ -3 +3 @@ title
-x
+y
diff --git a/logo.png b/logo.png
index 7777777..8888888 100644
Binary files a/logo.png and b/logo.png differ`

func TestParseReadsEveryKindOfFile(t *testing.T) {
	lines := Lines(parseSample + "\n")
	files := Parse(lines)
	type shape struct {
		Path, OldPath, Status string
		Binary                bool
		Add, Del, Hunks       int
	}
	var got []shape
	for _, f := range files {
		got = append(got, shape{f.Path, f.OldPath, f.Status, f.Binary, f.Add, f.Del, len(f.Hunks)})
	}
	want := []shape{
		{"main.go", "", StatusModified, false, 2, 1, 1},
		{"new.txt", "", StatusAdded, false, 1, 0, 1},
		{"gone.txt", "", StatusDeleted, false, 0, 2, 1},
		{"new name.md", "old name.md", StatusRenamed, false, 1, 1, 1},
		{"logo.png", "", StatusModified, true, 0, 0, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files =\n%+v\nwant\n%+v", got, want)
	}

	h := files[0].Hunks[0]
	if h.Header != "@@ -1,4 +1,5 @@ package main" {
		t.Errorf("header = %q", h.Header)
	}
	wantLines := []Line{
		{Op: ' ', Old: 1, New: 1, Text: "package main", Idx: 5},
		{Op: ' ', Old: 2, New: 2, Text: "", Idx: 6},
		{Op: '-', Old: 3, Text: "func a() {}", Idx: 7},
		{Op: '+', New: 3, Text: "func a() { b() }", Idx: 8},
		{Op: '+', New: 4, Text: "func b() {}", Idx: 9},
		{Op: ' ', Old: 4, New: 5, Text: "// end", Idx: 10},
	}
	if !reflect.DeepEqual(h.Lines, wantLines) {
		t.Fatalf("lines =\n%+v\nwant\n%+v", h.Lines, wantLines)
	}
	// (the blank context line above has lost its leading space, as an
	// editor or a trim can make it; it is still a line of the file)
	//
	// Idx is the coordinate Anchor works in: the line it names is the
	// line the parse says it is.
	for _, l := range h.Lines {
		if raw := lines[l.Idx]; raw != "" && raw[1:] != l.Text {
			t.Errorf("idx %d names %q, parse says %q", l.Idx, lines[l.Idx], l.Text)
		}
	}
	if d := files[2].Hunks[0].Lines; d[0].Old != 1 || d[1].Old != 2 || d[1].New != 0 {
		t.Errorf("deleted lines = %+v", d)
	}
	if r := files[3].Hunks[0].Lines; r[0].Old != 3 || r[1].New != 3 {
		t.Errorf("renamed lines = %+v", r)
	}
}

func TestLinesMatchesTheAnchorSplit(t *testing.T) {
	if got := Lines("a\nb\n\n"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Lines = %q", got)
	}
}

// TestParseReadsTheAwkwardPathsGitWrites: a deleted file whose path holds
// " b/", one git quotes, a rename to a quoted path, and a file whose
// content holds "++ b/evil" are each read with the path git means, by
// Parse and by FileAt alike.
func TestParseReadsTheAwkwardPathsGitWrites(t *testing.T) {
	diff := "diff --git a/d b/x.txt b/d b/x.txt\n" +
		"deleted file mode 100644\n" +
		"index 975fbec..0000000\n" +
		"--- a/d b/x.txt\t\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-y\n" +
		"diff --git a/q.txt b/q.txt\n" +
		"index 243154c..20afd38 100644\n" +
		"--- a/q.txt\n" +
		"+++ b/q.txt\n" +
		"@@ -1,2 +1,3 @@\n" +
		" keep\n" +
		"+++ b/evil\n" +
		" me\n" +
		"diff --git \"a/\\303\\251.txt\" \"b/\\303\\251.txt\"\n" +
		"deleted file mode 100644\n" +
		"index 587be6b..0000000\n" +
		"--- \"a/\\303\\251.txt\"\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-x\n" +
		"diff --git a/a.txt \"b/\\303\\274.txt\"\n" +
		"similarity index 100%\n" +
		"rename from a.txt\n" +
		"rename to \"\\303\\274.txt\"\n"
	lines := Lines(diff)
	var got []string
	for _, f := range Parse(lines) {
		got = append(got, f.OldPath+">"+f.Path)
	}
	if want := []string{">d b/x.txt", ">q.txt", ">é.txt", "a.txt>ü.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if f := FileAt(lines, 14); f != "q.txt" {
		t.Errorf("FileAt the line after \"+++ b/evil\" = %q, want q.txt", f)
	}
}
