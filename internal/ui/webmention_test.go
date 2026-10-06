package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMentionWordIsTheLastToken(t *testing.T) {
	for in, want := range map[string]string{"@": "", "look at @no": "no", "a\n@x/y": "x/y"} {
		got, ok := mentionWord(in)
		if !ok || got != want {
			t.Errorf("mentionWord(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"plain", "mail@host", "@done "} {
		if _, ok := mentionWord(in); ok {
			t.Errorf("mentionWord(%q) found a mention", in)
		}
	}
}

func TestFileCompletionsRankNameBeforePath(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, p := range []string{"docs/notes.md", "notes.go", "internal/annotes/x.go", "build/notes.txt"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range fileCompletions(dir, "notes") {
		got = append(got, c.Text)
	}
	want := []string{"@notes.go ", "@docs/notes.md ", "@internal/annotes/x.go "}
	if len(got) != len(want) {
		t.Fatalf("fileCompletions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fileCompletions = %q, want %q", got, want)
		}
	}
}
