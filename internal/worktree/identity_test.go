package worktree

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetIdentityWritesTheRepositorysOwnConfigOnly(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	for _, a := range [][]string{{"-C", root, "init", "-q"}, {"config", "--global", "user.name", "Machine"}, {"config", "--global", "user.email", "machine@example.com"}} {
		if out, err := exec.CommandContext(ctx, "git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	if n, e := Identity(ctx, root); n != "Machine" || e != "machine@example.com" {
		t.Fatalf("identity = %q <%s>, want the machine's", n, e)
	}

	if err := SetIdentity(ctx, root, " Ada Lovelace ", "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if n, e := Identity(ctx, root); n != "Ada Lovelace" || e != "ada@example.com" {
		t.Fatalf("identity = %q <%s>", n, e)
	}
	ident, err := exec.CommandContext(ctx, "git", "-C", root, "var", "GIT_AUTHOR_IDENT").Output()
	if err != nil || !strings.HasPrefix(string(ident), "Ada Lovelace <ada@example.com> ") {
		t.Fatalf("git would commit as %q, %v", ident, err)
	}
	if out, _ := exec.CommandContext(ctx, "git", "config", "--global", "user.name").Output(); strings.TrimSpace(string(out)) != "Machine" {
		t.Fatalf("the global identity was touched: %q", out)
	}

	for _, bad := range [][2]string{{"Ada", ""}, {"", "ada@example.com"}, {"Ada <x>", "ada@example.com"}, {"Ada", "not-an-email"}, {"Ada", "a b@example.com"}, {"Ada\nLovelace", "ada@example.com"}} {
		if err := SetIdentity(ctx, root, bad[0], bad[1]); err == nil {
			t.Errorf("SetIdentity(%q, %q) was accepted", bad[0], bad[1])
		}
	}
	if n, _ := Identity(ctx, root); n != "Ada Lovelace" {
		t.Fatalf("a refused identity changed the stored one: %q", n)
	}

	// clearing leaves the machine's, and clearing twice is not an error
	for range 2 {
		if err := SetIdentity(ctx, root, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if n, e := Identity(ctx, root); n != "Machine" || e != "machine@example.com" {
		t.Fatalf("after clearing = %q <%s>, want the machine's", n, e)
	}
}
