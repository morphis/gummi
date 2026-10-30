package worktree

// Property tests of a workflow run's snapshots, driven by hegel
// (https://hegel.dev), against real git. Each case commits files under
// awkward paths, ignores some, stages a change and leaves another on disk,
// takes the run's start snapshot, then edits the tree the way steps do —
// content, deletions, new files, a file turned into a directory and back,
// an executable bit, a symlink, the .gitignore itself — and takes the end
// one. Revert run is a promise made to a person: these hold it.

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

const snapshotCases = 60

var snapSegments = []string{"a", "b", "d b", "é", "-x", "*", "[x]", ":c", "x.log", "build"}

type snapCase struct {
	dir   string
	keep  []string
	start string // the start snapshot
	end   string // the end snapshot
	disk  map[string]string
	index []byte
	head  string
}

func sh(ht *hegel.T, dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		ht.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// diskState is every file and link under dir, outside .git, as its mode
// and content (a link's target).
func diskState(ht *hegel.T, dir string) map[string]string {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			out[filepath.ToSlash(rel)] = "link:" + t
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = info.Mode().Perm().String()[3:4] + ":" + string(b)
		}
		return nil
	})
	if err != nil {
		ht.Fatalf("walk: %v", err)
	}
	return out
}

func put(ht *hegel.T, dir, rel, content string, exe bool) {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	_ = os.RemoveAll(full)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		// a file stands where a directory must go: it gives way
		for d := filepath.Dir(full); d != dir; d = filepath.Dir(d) {
			if fi, e := os.Lstat(d); e == nil && !fi.IsDir() {
				_ = os.Remove(d)
			}
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			ht.Fatalf("mkdir: %v", err)
		}
	}
	mode := os.FileMode(0o644)
	if exe {
		mode = 0o755
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		ht.Fatalf("write: %v", err)
	}
	if err := os.Chmod(full, mode); err != nil {
		ht.Fatalf("chmod: %v", err)
	}
}

func drawRel(ht *hegel.T) string {
	n := hegel.Draw(ht, hegel.Integers(1, 3))
	var segs []string
	for range n {
		seg := hegel.Draw(ht, hegel.SampledFrom(snapSegments))
		segs = append(segs, seg)
	}
	return strings.Join(segs, "/")
}

// drawSnapCase builds the repository, takes the start snapshot, edits the
// tree and takes the end one.
func drawSnapCase(t *testing.T, ht *hegel.T) snapCase {
	ctx := context.Background()
	dir, err := os.MkdirTemp(t.TempDir(), "tree")
	if err != nil {
		ht.Fatalf("tempdir: %v", err)
	}
	sh(ht, dir, "init", "-q")
	put(ht, dir, ".gitignore", "*.log\nbuild/\n", false)
	n := hegel.Draw(ht, hegel.Integers(1, 5))
	for range n {
		rel := drawRel(ht)
		content := hegel.Draw(ht, hegel.SampledFrom([]string{"one\n", "two\n", "", "x\x00y"}))
		put(ht, dir, rel, content, false)
	}
	sh(ht, dir, "add", "-A")
	sh(ht, dir, "commit", "-q", "--allow-empty", "-m", "base")
	// what the person had going before the run: a staged change and an
	// unstaged one
	if hegel.Draw(ht, hegel.Booleans()) {
		put(ht, dir, drawRel(ht), "staged\n", false)
		sh(ht, dir, "add", "-A")
	}
	if hegel.Draw(ht, hegel.Booleans()) {
		put(ht, dir, drawRel(ht), "unstaged\n", false)
	}
	keep, err := Ignored(ctx, dir)
	if err != nil {
		ht.Fatalf("Ignored: %v", err)
	}
	start, err := Snapshot(ctx, dir, keep...)
	if err != nil {
		ht.Fatalf("Snapshot: %v", err)
	}
	disk := diskState(ht, dir)
	index, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	head := sh(ht, dir, "rev-parse", "HEAD")

	edits := hegel.Draw(ht, hegel.Integers(1, 6))
	for range edits {
		rel := drawRel(ht)
		full := filepath.Join(dir, filepath.FromSlash(rel))
		switch hegel.Draw(ht, hegel.Integers(0, 5)) {
		case 0, 1:
			content := hegel.Draw(ht, hegel.SampledFrom([]string{"changed\n", "", "z\x00"}))
			exe := hegel.Draw(ht, hegel.Booleans())
			put(ht, dir, rel, content, exe)
		case 2:
			_ = os.RemoveAll(full)
		case 3:
			put(ht, dir, rel+"/inner", "dir now\n", false)
		case 4:
			_ = os.RemoveAll(full)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err == nil {
				_ = os.Symlink("a", full)
			}
		case 5:
			// stop ignoring *.log: what was ignored at the start stays out
			put(ht, dir, ".gitignore", "build/\n", false)
		}
	}
	end, err := Snapshot(ctx, dir, keep...)
	if err != nil {
		ht.Fatalf("Snapshot: %v", err)
	}
	return snapCase{dir: dir, keep: keep, start: start, end: end, disk: disk, index: index, head: head}
}

func props(t *testing.T, fn func(*hegel.T)) {
	t.Helper()
	hegel.Test(t, fn, hegel.WithTestCases(snapshotCases), hegel.WithDatabase(""))
}

// TestPropertySnapshotTouchesNothing: taking a snapshot leaves the files,
// the index and HEAD exactly as they were.
func TestPropertySnapshotTouchesNothing(t *testing.T) {
	props(t, func(ht *hegel.T) {
		c := drawSnapCase(t, ht)
		before := diskState(ht, c.dir)
		index, _ := os.ReadFile(filepath.Join(c.dir, ".git", "index"))
		head := sh(ht, c.dir, "rev-parse", "HEAD")
		if _, err := Snapshot(context.Background(), c.dir, c.keep...); err != nil {
			ht.Fatalf("Snapshot: %v", err)
		}
		after := diskState(ht, c.dir)
		index2, _ := os.ReadFile(filepath.Join(c.dir, ".git", "index"))
		if !equalMaps(before, after) || string(index) != string(index2) || head != sh(ht, c.dir, "rev-parse", "HEAD") {
			ht.Fatalf("a snapshot changed the tree, the index or HEAD")
		}
	})
}

// TestPropertyRevertRunPutsTheTreeBack: reverting every path the run's
// diff names to the start snapshot leaves a tree whose snapshot is the
// start one, and every file as it was on disk when the run started.
func TestPropertyRevertRunPutsTheTreeBack(t *testing.T) {
	props(t, func(ht *hegel.T) {
		c := drawSnapCase(t, ht)
		ctx := context.Background()
		_, paths, err := DiffTrees(ctx, c.dir, c.start, c.end)
		if err != nil {
			ht.Fatalf("DiffTrees: %v", err)
		}
		if err := RevertTo(ctx, c.dir, c.start, paths, c.keep...); err != nil {
			ht.Fatalf("RevertTo %q: %v", paths, err)
		}
		// the person's .gitignore is a file like any other: put back with it
		again, err := Snapshot(ctx, c.dir, c.keep...)
		if err != nil {
			ht.Fatalf("Snapshot: %v", err)
		}
		if again != c.start {
			ht.Fatalf("reverted %q: snapshot %s, the run started at %s\n%s", paths, again, c.start, sh(ht, c.dir, "diff", "--stat", c.start, again))
		}
		now := diskState(ht, c.dir)
		var differ []string
		for p, v := range c.disk {
			if now[p] != v {
				differ = append(differ, p)
			}
		}
		for p := range now {
			if _, had := c.disk[p]; !had {
				differ = append(differ, p)
			}
		}
		// a file ignored only after the run started is the run's: the
		// snapshot never held it, so nothing could revert it
		var real []string
		for _, p := range differ {
			if !ignorable(p) {
				real = append(real, p)
			}
		}
		sort.Strings(real)
		if len(real) > 0 {
			ht.Fatalf("reverted %q, yet on disk %q differ from the start", paths, real)
		}
	})
}

// TestPropertyRevertRunLeavesIndexAndHEAD: a revert changes only files —
// the index and HEAD are as they were.
func TestPropertyRevertRunLeavesIndexAndHEAD(t *testing.T) {
	props(t, func(ht *hegel.T) {
		c := drawSnapCase(t, ht)
		ctx := context.Background()
		_, paths, err := DiffTrees(ctx, c.dir, c.start, c.end)
		if err != nil {
			ht.Fatalf("DiffTrees: %v", err)
		}
		if err := RevertTo(ctx, c.dir, c.start, paths, c.keep...); err != nil {
			ht.Fatalf("RevertTo: %v", err)
		}
		index, _ := os.ReadFile(filepath.Join(c.dir, ".git", "index"))
		if string(index) != string(c.index) {
			// the index file may be rewritten with the same entries: compare them
			if a, b := sh(ht, c.dir, "ls-files", "-s"), lsFilesOf(ht, c); a != b {
				ht.Fatalf("a revert changed the index:\n%s\nwas\n%s", a, b)
			}
		}
		if head := sh(ht, c.dir, "rev-parse", "HEAD"); head != c.head {
			ht.Fatalf("a revert moved HEAD")
		}
	})
}

func lsFilesOf(ht *hegel.T, c snapCase) string {
	tmp := filepath.Join(ht.TempDir(), "index")
	if err := os.WriteFile(tmp, c.index, 0o600); err != nil {
		ht.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "ls-files", "-s")
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmp)
	out, err := cmd.Output()
	if err != nil {
		ht.Fatalf("ls-files: %v", err)
	}
	return string(out)
}

// TestPropertyAnIgnoredFileOutlivesTheRun: what git ignored when the run
// started is never in its diff and never deleted or changed by its
// revert, even when the run stopped ignoring it.
func TestPropertyAnIgnoredFileOutlivesTheRun(t *testing.T) {
	props(t, func(ht *hegel.T) {
		c := drawSnapCase(t, ht)
		ctx := context.Background()
		_, paths, err := DiffTrees(ctx, c.dir, c.start, c.end)
		if err != nil {
			ht.Fatalf("DiffTrees: %v", err)
		}
		for _, p := range paths {
			if under(p, c.keep) {
				ht.Fatalf("the run's diff names %q, ignored when it started (%q)", p, c.keep)
			}
		}
		before := diskState(ht, c.dir)
		if err := RevertTo(ctx, c.dir, c.start, paths, c.keep...); err != nil {
			ht.Fatalf("RevertTo: %v", err)
		}
		now := diskState(ht, c.dir)
		for p, v := range before {
			if under(p, c.keep) && now[p] != v {
				ht.Fatalf("revert changed %q, ignored when the run started", p)
			}
		}
	})
}

// ignorable reports a path the case's .gitignore ignores in either of its
// forms: under a directory named *.log or build, or a *.log file.
func ignorable(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasSuffix(seg, ".log") || seg == "build" {
			return true
		}
	}
	return false
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
