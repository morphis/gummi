package diffannot

// Property tests of anchoring and of the diff reader, driven by hegel
// (https://hegel.dev).
//
// Anchor and Locate are read over drawn lines. Parse and FileAt are read
// against git itself: each case builds a repository in a TempDir, commits
// files, changes them — edits, adds, deletes, renames, a binary file, a
// last line with no newline — and diffs the index against HEAD. git's own
// --name-status and --numstat, and the files' contents on each side, are
// the oracle. File content includes lines that look like the diff's own
// headers, and paths include spaces, " b/", quotes, a tab, a newline and
// non-ASCII, which git quotes. A case spawns a dozen gits, so these run
// fewer cases than the pure properties.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	_ "github.com/morphis/gummi/internal/gittest"
)

const (
	propCases = 2000
	gitCases  = 100
)

func props(t *testing.T, n int, fn func(*hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(n), hegel.WithDatabase(""))
}

// propLines are diff lines: content under each marker, headers, and lines
// that only look like them.
var propLines = []string{
	" a", "+a", "-a", " b", "+b", "-b", "", " ", "+", "-", "@@ -1,2 +1,2 @@", "diff --git a/x b/x",
	"+++ b/x", "--- a/x", "\\ No newline at end of file", " a", " a",
}

func drawLines(ht *hegel.T) []string {
	return hegel.Draw(ht, hegel.Lists(hegel.SampledFrom(propLines)).MaxSize(30))
}

// TestPropertyLocateFindsTheFirstLineWithTheSameAnchor: an anchor taken
// at a line locates to the first line whose anchor is the same — that
// line, unless an earlier one has the same content around it.
func TestPropertyLocateFindsTheFirstLineWithTheSameAnchor(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		lines := drawLines(ht)
		if len(lines) == 0 {
			return
		}
		i := hegel.Draw(ht, hegel.Integers(0, len(lines)-1))
		a := Anchor(lines, i)
		first := slices.IndexFunc(lines, func(string) bool { return false })
		for j := range lines {
			if Anchor(lines, j) == a {
				first = j
				break
			}
		}
		if got := Locate(lines, a); got != first || got > i {
			ht.Fatalf("Locate(Anchor(%d)) = %d, first with that anchor %d", i, got, first)
		}
	})
}

// TestPropertyLocateAllAgreesWithLocate: resolving many anchors at once
// answers what resolving each alone does, the empty anchor and anchors
// from another diff included.
func TestPropertyLocateAllAgreesWithLocate(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		lines, other := drawLines(ht), drawLines(ht)
		var wants []string
		n := hegel.Draw(ht, hegel.Integers(0, 6))
		for range n {
			src := lines
			if hegel.Draw(ht, hegel.Booleans()) {
				src = other
			}
			idx := hegel.Draw(ht, hegel.Integers(-1, len(src)))
			wants = append(wants, Anchor(src, idx))
		}
		got := LocateAll(lines, wants)
		for i, w := range wants {
			if want := Locate(lines, w); got[i] != want {
				ht.Fatalf("LocateAll[%d] = %d, Locate = %d", i, got[i], want)
			}
		}
	})
}

// TestPropertyAnAnchorIgnoresTheDiffMarker: a line's anchor is its text,
// not its role — the same window with any line's +, - or space marker
// swapped anchors the same.
func TestPropertyAnAnchorIgnoresTheDiffMarker(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		lines := drawLines(ht)
		if len(lines) == 0 {
			return
		}
		i := hegel.Draw(ht, hegel.Integers(0, len(lines)-1))
		j := hegel.Draw(ht, hegel.Integers(0, len(lines)-1))
		l := lines[j]
		if l == "" || !strings.ContainsRune("+- ", rune(l[0])) {
			return
		}
		swapped := slices.Clone(lines)
		swapped[j] = hegel.Draw(ht, hegel.SampledFrom([]string{"+", "-", " "})) + l[1:]
		if Anchor(lines, i) != Anchor(swapped, i) {
			ht.Fatalf("swapping line %d's marker (%q → %q) moved line %d's anchor", j, l, swapped[j], i)
		}
	})
}

// TestPropertyAnAnchorSurvivesAnEditOutsideItsWindow: lines inserted or
// removed more than two lines away from the anchored one leave it
// locatable, at a line with its anchor.
func TestPropertyAnAnchorSurvivesAnEditOutsideItsWindow(t *testing.T) {
	props(t, propCases, func(ht *hegel.T) {
		lines := drawLines(ht)
		if len(lines) == 0 {
			return
		}
		i := hegel.Draw(ht, hegel.Integers(0, len(lines)-1))
		a := Anchor(lines, i)
		edited := slices.Clone(lines)
		before := hegel.Draw(ht, hegel.Booleans())
		ins := drawLines(ht)
		shift := 0
		if before && i-context-1 >= 0 {
			at := hegel.Draw(ht, hegel.Integers(0, i-context-1))
			edited = slices.Insert(edited, at, ins...)
			shift = len(ins)
		} else if !before && i+context+1 <= len(edited) {
			at := hegel.Draw(ht, hegel.Integers(i+context+1, len(edited)))
			edited = slices.Insert(edited, at, ins...)
		}
		got := Locate(edited, a)
		if got < 0 || got > i+shift || Anchor(edited, got) != a {
			ht.Fatalf("anchor at %d: located %d in the edited diff (shift %d)", i, got, shift)
		}
	})
}

// --- against git ----------------------------------------------------------

// propSegments are path segments, the awkward ones git quotes or splits.
var propSegments = []string{"a", "b", "src", "d b", "x y", "é", `q"t`, "t\tb", "n\nl", "b ", "-x"}

// propContent are file lines, some shaped like a diff's own lines.
var propContent = []string{
	"alpha", "beta", "", "gamma delta", "++ b/evil", "-- a/evil", "+++ b/evil", "--- a/evil",
	"@@ -1 +1 @@", "diff --git a/q b/q", "\\ No newline at end of file", "rename to x", " lead", "\ttab",
}

type gitCase struct {
	dir   string
	lines []string
	files []File
}

func git(ht *hegel.T, dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		ht.Fatalf("git %v: %v: %s", args, err, errb.String())
	}
	return out.String()
}

func drawPath(ht *hegel.T, taken map[string]bool) string {
	for {
		depth := hegel.Draw(ht, hegel.Integers(1, 3))
		var segs []string
		for range depth {
			seg := hegel.Draw(ht, hegel.SampledFrom(propSegments))
			segs = append(segs, seg)
		}
		p := strings.Join(segs, "/") + ".txt"
		free := true
		for q := range taken {
			// a path may not be a directory of another, nor one already taken
			free = free && q != p && !strings.HasPrefix(q, strings.TrimSuffix(p, ".txt")+"/") && !strings.HasPrefix(p, strings.TrimSuffix(q, ".txt")+"/")
		}
		if free {
			taken[p] = true
			return p
		}
	}
}

func drawContent(ht *hegel.T) string {
	ls := hegel.Draw(ht, hegel.Lists(hegel.SampledFrom(propContent)).MinSize(1).MaxSize(12))
	s := strings.Join(ls, "\n")
	if hegel.Draw(ht, hegel.WeightedBooleans(0.85)) {
		s += "\n"
	}
	return s
}

func write(ht *hegel.T, dir, p, content string) {
	full := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		ht.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		ht.Fatalf("write: %v", err)
	}
}

// drawGitCase builds a repository, commits drawn files, changes some and
// returns the staged diff against HEAD.
func drawGitCase(t *testing.T, ht *hegel.T) gitCase {
	dir, err := os.MkdirTemp(t.TempDir(), "repo")
	if err != nil {
		ht.Fatalf("tempdir: %v", err)
	}
	git(ht, dir, "init", "-q")
	taken := map[string]bool{}
	type file struct{ path, content string }
	var files []file
	n := hegel.Draw(ht, hegel.Integers(1, 4))
	for range n {
		f := file{drawPath(ht, taken), drawContent(ht)}
		write(ht, dir, f.path, f.content)
		files = append(files, f)
	}
	git(ht, dir, "add", "-A")
	git(ht, dir, "commit", "-q", "-m", "base")
	for _, f := range files {
		switch hegel.Draw(ht, hegel.Integers(0, 4)) {
		case 0: // left alone
		case 1:
			write(ht, dir, f.path, drawContent(ht))
		case 2:
			git(ht, dir, "rm", "-q", "--", f.path)
		case 3:
			to := drawPath(ht, taken)
			write(ht, dir, to, f.content)
			git(ht, dir, "rm", "-q", "--", f.path)
		case 4:
			write(ht, dir, f.path, "\x00\x01binary"+f.content)
		}
	}
	if hegel.Draw(ht, hegel.Booleans()) {
		write(ht, dir, drawPath(ht, taken), drawContent(ht))
	}
	git(ht, dir, "add", "-A")
	diff := git(ht, dir, "diff", "--cached", "-M", "--no-color", "--no-ext-diff", "HEAD")
	lines := Lines(diff)
	ht.Note("diff:\n" + diff)
	return gitCase{dir: dir, lines: lines, files: Parse(lines)}
}

// TestPropertyParseNamesTheFilesGitNames: every file git's --name-status
// lists, Parse reads with the same path, old path and status, in order.
func TestPropertyParseNamesTheFilesGitNames(t *testing.T) {
	props(t, gitCases, func(ht *hegel.T) {
		c := drawGitCase(t, ht)
		fields := strings.Split(strings.TrimSuffix(git(ht, c.dir, "diff", "--cached", "-M", "--name-status", "-z", "HEAD"), "\x00"), "\x00")
		var want []File
		for i := 0; i < len(fields) && fields[i] != ""; {
			st := fields[i]
			f := File{Path: fields[i+1]}
			switch st[0] {
			case 'M':
				f.Status = StatusModified
			case 'A':
				f.Status = StatusAdded
			case 'D':
				f.Status = StatusDeleted
			case 'R':
				f.Status, f.OldPath, f.Path = StatusRenamed, fields[i+1], fields[i+2]
				i++
			case 'C':
				f.Status, f.OldPath, f.Path = StatusCopied, fields[i+1], fields[i+2]
				i++
			}
			want = append(want, f)
			i += 2
		}
		if len(c.files) != len(want) {
			ht.Fatalf("Parse read %d files, git names %d: %+v vs %+v", len(c.files), len(want), c.files, want)
		}
		for i, w := range want {
			g := c.files[i]
			if g.Path != w.Path || g.OldPath != w.OldPath || g.Status != w.Status {
				ht.Fatalf("file %d: Parse %q (from %q, %s), git %q (from %q, %s)", i, g.Path, g.OldPath, g.Status, w.Path, w.OldPath, w.Status)
			}
		}
	})
}

// TestPropertyParseCountsWhatGitCounts: each file's added and deleted
// line counts are git's --numstat; a binary file is marked so.
func TestPropertyParseCountsWhatGitCounts(t *testing.T) {
	props(t, gitCases, func(ht *hegel.T) {
		c := drawGitCase(t, ht)
		out := git(ht, c.dir, "diff", "--cached", "-M", "--numstat", "-z", "HEAD")
		i := 0
		for _, f := range c.files {
			rest := out[i:]
			end := strings.IndexByte(rest, 0)
			rec := rest[:end]
			i += end + 1
			if strings.HasSuffix(rec, "\t") { // a rename: two NUL-ended paths follow
				for range 2 {
					i += strings.IndexByte(out[i:], 0) + 1
				}
			}
			parts := strings.SplitN(rec, "\t", 3)
			if parts[0] == "-" {
				if !f.Binary || len(f.Hunks) > 0 {
					ht.Fatalf("%q: git says binary, Parse binary=%v with %d hunks", f.Path, f.Binary, len(f.Hunks))
				}
				continue
			}
			add, _ := strconv.Atoi(parts[0])
			del, _ := strconv.Atoi(parts[1])
			if f.Add != add || f.Del != del || f.Binary {
				ht.Fatalf("%q: Parse +%d -%d binary=%v, git +%d -%d", f.Path, f.Add, f.Del, f.Binary, add, del)
			}
		}
	})
}

// TestPropertyALineIsTheTextOfItsFileAtItsNumber: a hunk line's New
// number is its line in the file after the change, and its Old number
// its line before — the text Parse read is the text there.
func TestPropertyALineIsTheTextOfItsFileAtItsNumber(t *testing.T) {
	props(t, gitCases, func(ht *hegel.T) {
		c := drawGitCase(t, ht)
		for _, f := range c.files {
			var after, before []string
			if f.Status != StatusDeleted {
				after = strings.Split(git(ht, c.dir, "show", ":"+f.Path), "\n")
			}
			if f.Status != StatusAdded {
				old := f.Path
				if f.OldPath != "" {
					old = f.OldPath
				}
				before = strings.Split(git(ht, c.dir, "show", "HEAD:"+old), "\n")
			}
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if l.Op != '-' && (l.New < 1 || l.New > len(after) || after[l.New-1] != l.Text) {
						ht.Fatalf("%q line %d (%c) reads %q; the file after has %q", f.Path, l.New, l.Op, l.Text, at(after, l.New))
					}
					if l.Op != '+' && (l.Old < 1 || l.Old > len(before) || before[l.Old-1] != l.Text) {
						ht.Fatalf("%q old line %d (%c) reads %q; the file before has %q", f.Path, l.Old, l.Op, l.Text, at(before, l.Old))
					}
				}
			}
		}
	})
}

func at(ls []string, n int) string {
	if n < 1 || n > len(ls) {
		return fmt.Sprintf("<none: %d lines>", len(ls))
	}
	return ls[n-1]
}

// TestPropertyALineIsTheDiffLineAtItsIndex: a hunk line's Idx is its
// coordinate in Lines — the marker and text Parse read are that line's.
func TestPropertyALineIsTheDiffLineAtItsIndex(t *testing.T) {
	props(t, gitCases, func(ht *hegel.T) {
		c := drawGitCase(t, ht)
		for _, f := range c.files {
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if want := string(l.Op) + l.Text; c.lines[l.Idx] != want && (l.Op != ' ' || l.Text != "" || c.lines[l.Idx] != "") {
						ht.Fatalf("%q: line %d is %q, Parse read %q", f.Path, l.Idx, c.lines[l.Idx], want)
					}
				}
			}
		}
	})
}

// TestPropertyFileAtAgreesWithParse: every hunk line's file, read by
// FileAt from its index alone, is the file Parse put it in.
func TestPropertyFileAtAgreesWithParse(t *testing.T) {
	props(t, gitCases, func(ht *hegel.T) {
		c := drawGitCase(t, ht)
		for _, f := range c.files {
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if got := FileAt(c.lines, l.Idx); got != f.Path {
						ht.Fatalf("FileAt(%d) = %q, Parse put the line in %q", l.Idx, got, f.Path)
					}
				}
			}
		}
	})
}
