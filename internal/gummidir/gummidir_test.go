package gummidir

import (
	"path/filepath"
	"testing"
)

func TestDefaultIsDotGummiUnderRoot(t *testing.T) {
	t.Setenv(Env, "")
	if got, want := Resolve("/ws"), "/ws/.gummi"; got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
	if got, want := FromRel("/ws", ".gummi/specs/a.md"), "/ws/.gummi/specs/a.md"; got != want {
		t.Fatalf("FromRel = %q, want %q", got, want)
	}
}

func TestCustomDirReplacesDotGummi(t *testing.T) {
	t.Setenv(Env, "/data/gummi-custom")
	if got := Resolve("/ws"); got != "/data/gummi-custom" {
		t.Fatalf("Resolve = %q", got)
	}
	if got, want := Path("/ws", "worktrees"), "/data/gummi-custom/worktrees"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if got, want := FromRel("/ws", ".gummi/specs/a.md"), "/data/gummi-custom/specs/a.md"; got != want {
		t.Fatalf("FromRel = %q, want %q", got, want)
	}
	if got, want := FromRel("/ws", "docs/a.md"), filepath.Join("/ws", "docs/a.md"); got != want {
		t.Fatalf("FromRel other = %q, want %q", got, want)
	}
}
