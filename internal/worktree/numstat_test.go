package worktree

import "testing"

// TestParseNumstatZHandlesRenames: `--numstat -z` does not write one
// record per NUL. A rename writes its counts with an EMPTY path field and
// then two further tokens, the old path and the new one. Splitting on NUL
// alone drops the destination — the very file a hygiene check has to see.
func TestParseNumstatZHandlesRenames(t *testing.T) {
	// binary, ordinary, rename — the three shapes, in git's own layout
	out := "-\t-\tb.bin\x001\t0\tc.txt\x001\t0\t\x00a.txt\x00renamed.txt\x00"
	got := parseNumstatZ(out)
	if len(got) != 3 {
		t.Fatalf("parsed %d files, want 3: %+v", len(got), got)
	}
	if got[0].Path != "b.bin" || !got[0].Binary {
		t.Errorf("binary entry = %+v", got[0])
	}
	if got[1].Path != "c.txt" || got[1].Binary {
		t.Errorf("ordinary entry = %+v", got[1])
	}
	if got[2].Path != "renamed.txt" {
		t.Errorf("rename entry = %+v, want the destination path", got[2])
	}
}

// A truncated rename record must not panic or invent a path.
func TestParseNumstatZToleratesTruncation(t *testing.T) {
	if got := parseNumstatZ("1\t0\t\x00a.txt\x00"); len(got) != 0 {
		t.Errorf("truncated rename produced %+v", got)
	}
	if got := parseNumstatZ(""); len(got) != 0 {
		t.Errorf("empty output produced %+v", got)
	}
}
