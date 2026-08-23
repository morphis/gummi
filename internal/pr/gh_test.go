package pr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// fakeGH writes an executable shell shim to a temp dir that logs its argv
// (one space-joined line per invocation) to argvLog and prints viewOut for
// any "pr view" call or listOut for any "pr list" call. No network, no real
// gh CLI — the deterministic offline seam the plan calls for.
func fakeGH(t *testing.T, viewOut, listOut string) (binPath, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv.log")
	viewFile := filepath.Join(dir, "view.json")
	listFile := filepath.Join(dir, "list.json")
	if err := os.WriteFile(viewFile, []byte(viewOut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listFile, []byte(listOut), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + argvLog + "\"\n" +
		"case \"$*\" in\n" +
		"  *\"pr view\"*) cat \"" + viewFile + "\" ;;\n" +
		"  *\"pr list\"*) cat \"" + listFile + "\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	binPath = filepath.Join(dir, "gh")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argvLog
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePullRefURL(t *testing.T) {
	repo, number, err := ParsePullRefURL("https://github.com/o/r/pull/42")
	if err != nil {
		t.Fatalf("ParsePullRefURL: %v", err)
	}
	if repo != "o/r" || number != 42 {
		t.Errorf("ParsePullRefURL = (%q, %d), want (\"o/r\", 42)", repo, number)
	}
	for _, bad := range []string{"", "https://example.com/o/r/pull/42", "https://github.com/o/r/issues/42", "not a url"} {
		if _, _, err := ParsePullRefURL(bad); err == nil {
			t.Errorf("ParsePullRefURL(%q) = nil error, want rejection", bad)
		}
	}
}

func TestAvailableMissingGH(t *testing.T) {
	t.Setenv("GH_TOKEN", "x")
	if err := Available(filepath.Join(t.TempDir(), "nonexistent-gh-binary")); err == nil {
		t.Fatal("Available with a missing binary should error")
	} else if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("error %q should name gh as the missing piece", err)
	}
}

// TestAvailableAcceptsGhAuthLoginUser stands in for the `gh auth login`
// state: gh is on PATH and authenticated, but neither GH_TOKEN nor
// GITHUB_TOKEN is set (gh keeps that credential in its own keyring/config,
// not the environment). Available must not reject this — auth is gh's own
// concern, surfaced via its own error on the first real call if it's
// genuinely missing.
func TestAvailableAcceptsGhAuthLoginUser(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nif [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then exit 0; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if err := Available(bin); err != nil {
		t.Fatalf("Available with gh authed via `gh auth login` (no env token) = %v, want nil", err)
	}
}

const viewJSON = `{"number":42,"url":"https://github.com/o/r/pull/42","headRefOid":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}`

func TestResolveURL(t *testing.T) {
	bin, log := fakeGH(t, viewJSON, "[]")
	ref, err := Resolve(context.Background(), bin, "https://github.com/o/r/pull/42", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if ref != want {
		t.Errorf("Resolve(url) = %+v, want %+v", ref, want)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr view 42") || !strings.Contains(argv, "--repo o/r") {
		t.Errorf("argv %q missing expected pr view + --repo o/r", argv)
	}
}

func TestResolveBareNumber(t *testing.T) {
	bin, log := fakeGH(t, viewJSON, "[]")
	ref, err := Resolve(context.Background(), bin, "42", t.TempDir(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref.Number != 42 || ref.Repo != "o/r" {
		t.Errorf("Resolve(number) = %+v, want number 42 repo o/r", ref)
	}
	argv := readLog(t, log)
	if strings.Contains(argv, "--repo") {
		t.Errorf("bare-number form must not pass --repo (relies on gh auto-detecting from dir): argv %q", argv)
	}
}

func TestResolveAuto(t *testing.T) {
	bin, log := fakeGH(t, "{}", "["+viewJSON+"]")
	ref, err := Resolve(context.Background(), bin, "", t.TempDir(), "gummi/FD-090-add-pr-ref")
	if err != nil {
		t.Fatalf("Resolve(--auto): %v", err)
	}
	if ref.Number != 42 {
		t.Errorf("Resolve(--auto) = %+v, want number 42", ref)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr list") || !strings.Contains(argv, "--head gummi/FD-090-add-pr-ref") {
		t.Errorf("argv %q missing expected pr list --head", argv)
	}
}

func TestResolveAutoRequiresExactlyOneMatch(t *testing.T) {
	bin, _ := fakeGH(t, "{}", "[]")
	if _, err := Resolve(context.Background(), bin, "", t.TempDir(), "some-branch"); err == nil {
		t.Fatal("--auto with zero matches should error")
	}
	bin2, _ := fakeGH(t, "{}", "["+viewJSON+","+viewJSON+"]")
	if _, err := Resolve(context.Background(), bin2, "", t.TempDir(), "some-branch"); err == nil {
		t.Fatal("--auto with multiple matches should error")
	}
}

func TestResolveRejectsGarbageSpec(t *testing.T) {
	// A garbage spec is rejected before gh is ever invoked, so no fake
	// binary or valid directory is needed.
	if _, err := Resolve(context.Background(), "/nonexistent/gh", "not-a-url-or-number", "/nonexistent/dir", ""); err == nil {
		t.Fatal("a spec that is neither URL nor number should error")
	}
}

func TestLiveStatus(t *testing.T) {
	bin, log := fakeGH(t, `{"state":"OPEN","comments":[{},{},{}]}`, "[]")
	ref := domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	state, comments, err := LiveStatus(context.Background(), bin, ref)
	if err != nil {
		t.Fatalf("LiveStatus: %v", err)
	}
	if state != "OPEN" || comments != 3 {
		t.Errorf("LiveStatus = (%q, %d), want (\"OPEN\", 3)", state, comments)
	}
	argv := readLog(t, log)
	if !strings.Contains(argv, "pr view 42") || !strings.Contains(argv, "--repo o/r") {
		t.Errorf("argv %q missing expected pr view 42 --repo o/r", argv)
	}
}
