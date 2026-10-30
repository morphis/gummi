package diffannot

import (
	"strconv"
	"strings"
)

// Lines splits a unified diff into the lines an annotation's index counts
// in. Every reader that anchors, locates or draws annotations must split
// the same way — the index is only a coordinate while they agree — so
// they all go through here.
func Lines(diff string) []string {
	return strings.Split(strings.TrimRight(diff, "\n"), "\n")
}

// File statuses Parse reports.
const (
	StatusModified = "modified"
	StatusAdded    = "added"
	StatusDeleted  = "deleted"
	StatusRenamed  = "renamed"
	StatusCopied   = "copied"
)

// File is one file's section of a unified diff.
type File struct {
	// Path is the file's path after the change — its old path when the
	// change deleted it. OldPath is set when it differs (a rename, a copy).
	Path    string
	OldPath string
	Status  string
	// Binary marks a file git reports only as "Binary files … differ":
	// it has no hunks.
	Binary bool
	Add    int
	Del    int
	Hunks  []Hunk
}

// Hunk is one @@ block.
type Hunk struct {
	Header string
	Lines  []Line
}

// Line is one line of a hunk. Idx is its index into the diff's Lines —
// the coordinate Anchor and Locate work in. Old and New are its line
// numbers on each side, zero on the side it does not exist on.
type Line struct {
	// Op is ' ', '+' or '-'.
	Op   byte
	Old  int
	New  int
	Text string
	Idx  int
}

// Parse reads a git unified diff, split by Lines, into files and hunks.
// It is forgiving: a line it does not recognize is skipped rather than
// failing the parse, since the caller is drawing a diff for a person and
// a partial drawing beats none. "\ No newline at end of file" markers are
// dropped; they annotate the line above and are not content.
func Parse(lines []string) []File {
	files, _ := parse(lines)
	return files
}

// parse is Parse, with the index of each file's diff --git header.
func parse(lines []string) ([]File, []int) {
	var (
		starts   []int
		files    []File
		cur      *File
		hunk     *Hunk
		old, neu int
	)
	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			flushFile()
			starts = append(starts, i)
			a, b := gitHeaderPaths(strings.TrimPrefix(l, "diff --git "))
			cur = &File{Path: b, Status: StatusModified}
			if a != b {
				cur.OldPath = a
			}
			continue
		}
		if cur == nil {
			continue
		}
		if hunk != nil {
			if l == "" {
				// a context line whose text is empty loses its leading
				// space to nothing but a stripped trailing blank; it is
				// still a line of the file
				hunk.Lines = append(hunk.Lines, Line{Op: ' ', Old: old, New: neu, Idx: i})
				old++
				neu++
				continue
			}
			switch l[0] {
			case ' ':
				hunk.Lines = append(hunk.Lines, Line{Op: ' ', Old: old, New: neu, Text: l[1:], Idx: i})
				old++
				neu++
				continue
			case '+':
				hunk.Lines = append(hunk.Lines, Line{Op: '+', New: neu, Text: l[1:], Idx: i})
				neu++
				cur.Add++
				continue
			case '-':
				hunk.Lines = append(hunk.Lines, Line{Op: '-', Old: old, Text: l[1:], Idx: i})
				old++
				cur.Del++
				continue
			case '\\':
				continue
			}
			flushHunk()
		}
		switch {
		case strings.HasPrefix(l, "@@"):
			hunk = &Hunk{Header: l}
			old, neu = hunkStarts(l)
		case strings.HasPrefix(l, "new file mode"):
			cur.Status = StatusAdded
		case strings.HasPrefix(l, "deleted file mode"):
			cur.Status = StatusDeleted
		case strings.HasPrefix(l, "rename from "):
			cur.Status, cur.OldPath = StatusRenamed, unquote(strings.TrimPrefix(l, "rename from "))
		case strings.HasPrefix(l, "rename to "):
			cur.Path = unquote(strings.TrimPrefix(l, "rename to "))
		case strings.HasPrefix(l, "copy from "):
			cur.Status, cur.OldPath = StatusCopied, unquote(strings.TrimPrefix(l, "copy from "))
		case strings.HasPrefix(l, "copy to "):
			cur.Path = unquote(strings.TrimPrefix(l, "copy to "))
		case strings.HasPrefix(l, "Binary files ") || l == "GIT binary patch":
			cur.Binary = true
		case strings.HasPrefix(l, "--- ") && cur.Status == StatusDeleted:
			// a deleted file's +++ is /dev/null: its --- names it, which
			// the diff --git header cannot when the path holds " b/"
			if p, ok := sidePath(strings.TrimPrefix(l, "--- "), "a/"); ok {
				cur.Path = p
			}
		case strings.HasPrefix(l, "+++ "):
			// a deleted file's +++ is /dev/null; its path stays the one
			// the diff --git header named
			if p, ok := sidePath(strings.TrimPrefix(l, "+++ "), "b/"); ok {
				cur.Path = p
			}
		}
	}
	flushFile()
	for i := range files {
		f := &files[i]
		if f.Status == StatusDeleted || f.Status == StatusAdded {
			f.OldPath = ""
		}
		if f.OldPath == f.Path {
			f.OldPath = ""
		}
	}
	return files, starts
}

// gitHeaderPaths takes "a/x b/y" apart, either side quoted the way git
// quotes a path with unusual bytes. Paths with spaces are ambiguous in
// this header; one path on both sides (a/x b/x, the header of every
// change but a rename or copy) is read as that path, and the ---/+++ and
// rename lines that follow correct the rest.
func gitHeaderPaths(rest string) (a, b string) {
	if strings.HasPrefix(rest, `"`) {
		if q, err := strconv.QuotedPrefix(rest); err == nil && strings.HasPrefix(rest[len(q):], " ") {
			a, b = unquote(q), unquote(rest[len(q)+1:])
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	if half := (len(rest) - 1) / 2; len(rest)%2 == 1 && rest[half] == ' ' &&
		strings.HasPrefix(rest, "a/") && strings.HasPrefix(rest[half+1:], "b/") && rest[2:half] == rest[half+3:] {
		return rest[2:half], rest[2:half]
	}
	if j := strings.LastIndex(rest, ` "b/`); j >= 0 && strings.HasSuffix(rest, `"`) {
		return strings.TrimPrefix(rest[:j], "a/"), strings.TrimPrefix(unquote(rest[j+1:]), "b/")
	}
	if j := strings.LastIndex(rest, " b/"); j >= 0 {
		a, b = rest[:j], rest[j+3:]
		a = strings.TrimPrefix(a, "a/")
		return unquote(a), unquote(b)
	}
	return rest, rest
}

// sidePath reads a ---/+++ header's path: false for /dev/null.
func sidePath(p, prefix string) (string, bool) {
	p = strings.TrimSuffix(p, "\t")
	if p == "/dev/null" {
		return "", false
	}
	p = unquote(p)
	return strings.TrimPrefix(p, prefix), true
}

// unquote undoes git's C-style quoting of a path with unusual bytes.
func unquote(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

// hunkStarts reads "@@ -a,b +c,d @@" into the first line number of each
// side.
func hunkStarts(h string) (old, neu int) {
	fields := strings.Fields(h)
	for _, f := range fields[1:] {
		switch {
		case strings.HasPrefix(f, "-"):
			old = rangeStart(f[1:])
		case strings.HasPrefix(f, "+"):
			neu = rangeStart(f[1:])
		case f == "@@":
			return old, neu
		}
	}
	return old, neu
}

func rangeStart(r string) int {
	s, _, _ := strings.Cut(r, ",")
	n, _ := strconv.Atoi(s)
	return n
}
