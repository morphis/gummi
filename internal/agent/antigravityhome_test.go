package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// antigravityTokenFixture writes an operator home with a token in it and
// points HOME there, returning the token's path.
func antigravityTokenFixture(t *testing.T, token string) string {
	t.Helper()
	home := t.TempDir()
	p := filepath.Join(home, filepath.FromSlash(antigravityTokenRelPath))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return p
}

func antigravityTokenIn(home string) string {
	return filepath.Join(home, filepath.FromSlash(antigravityTokenRelPath))
}

// TestAntigravityHomeSeedsAndModes: a card home is created 0o700 and its
// seeded token copy 0o600 — the credential never lands group/world
// readable — and a home that already has a copy is left alone.
func TestAntigravityHomeSeedsAndModes(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok-operator")
	scratch := t.TempDir()
	home := filepath.Join(scratch, "agy-home")
	if err := seedAntigravityToken(home); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode = %v, want 0700", fi.Mode().Perm())
	}
	b, err := os.ReadFile(antigravityTokenIn(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "tok-operator" {
		t.Fatalf("seeded token = %q", b)
	}
	ti, err := os.Stat(antigravityTokenIn(home))
	if err != nil {
		t.Fatal(err)
	}
	if ti.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %v, want 0600", ti.Mode().Perm())
	}
	// an existing copy is kept, not overwritten by a re-seed on open.
	if err := os.WriteFile(antigravityTokenIn(home), []byte("tok-refreshed-by-agy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedAntigravityToken(home); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(antigravityTokenIn(home))
	if string(b) != "tok-refreshed-by-agy" {
		t.Errorf("open re-seeded an existing copy: %q", b)
	}
}

// TestAntigravityHomeIsolation: a session's child runs with HOME inside
// the card home, the operator's real config is never touched, and —
// because the seeding path reads but never writes there — the real
// home's file set and token bytes are unchanged after a session's whole
// life (INV-1/INV-2).
func TestAntigravityHomeIsolation(t *testing.T) {
	realToken := antigravityTokenFixture(t, "tok-real")
	before := antigravityHomeSnapshot(t, filepath.Dir(realToken))

	ag, err := NewAntigravity(writeFakeAgy(t, fakeAgyArgvEcho))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()
	log := filepath.Join(scratch, "calls")
	t.Setenv("AGY_LOG", log)
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: scratch, Permission: PermissionAllowAll,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	sess.Close()

	if after := antigravityHomeSnapshot(t, filepath.Dir(realToken)); !antigravitySnapshotsEqual(before, after) {
		t.Errorf("the operator's real home changed: before=%v after=%v", before, after)
	}
	if b, err := os.ReadFile(realToken); err != nil || string(b) != "tok-real" {
		t.Errorf("the operator's token moved: %q, %v", b, err)
	}

	// the card home had no scratch anchor → a temp home, already removed
	// at Close.
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "agy-home") {
			t.Errorf("a temp home survived Close: %s", e.Name())
		}
	}
}

func antigravityHomeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func antigravitySnapshotsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestAntigravityCardHomeRedirectsChild: a session with a ScratchDir
// anchors its card home there (<ScratchDir>/agy-home), the child's HOME
// is that home, and the home survives Close (conversations live under
// it, which is what resume needs).
func TestAntigravityCardHomeRedirectsChild(t *testing.T) {
	tok := antigravityTokenFixture(t, "tok")
	realHome := strings.TrimSuffix(tok, string(filepath.Separator)+filepath.FromSlash(antigravityTokenRelPath))
	gitconfig := filepath.Join(realHome, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\tname = op\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	sess.Close()

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var homes, gitconfigs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if h, ok := rec["home"].(string); ok {
			homes = append(homes, h)
			g, _ := rec["gitconfig"].(string)
			gitconfigs = append(gitconfigs, g)
		}
	}
	if len(homes) != 1 || homes[0] != filepath.Join(scratch, "agy-home") {
		t.Fatalf("child HOME = %v, want the card home under ScratchDir", homes)
	}
	// the tools agy runs keep the operator's git identity
	if gitconfigs[0] != gitconfig {
		t.Errorf("child GIT_CONFIG_GLOBAL = %q, want the operator's %s", gitconfigs[0], gitconfig)
	}
	if _, err := os.Stat(antigravityTokenIn(filepath.Join(scratch, "agy-home"))); err != nil {
		t.Errorf("card home's seeded token missing after Close: %v", err)
	}
}

// TestAntigravityTempHomeRemovedAtClose: a session without a ScratchDir
// pays a seeded temp home that is gone when the session is — the
// intended home for one-shot session kinds, doctor probes, and tests.
func TestAntigravityTempHomeRemovedAtClose(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	ag, err := NewAntigravity(writeFakeAgy(t, fakeAgyArgvEcho))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	t.Setenv("AGY_LOG", filepath.Join(t.TempDir(), "calls"))
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir(), Permission: PermissionAllowAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	sess.Close()
}

// TestAntigravityReseedOnAuthFailure: a spawn whose child reports an
// authentication failure has the card home's stale token copy repaired
// from the operator's current token before the RunFailure surfaces —
// the failure itself is still reported (INV-7), never retried silently.
func TestAntigravityReseedOnAuthFailure(t *testing.T) {
	tokenPath := antigravityTokenFixture(t, "tok-v1")
	scratch := t.TempDir()
	home := filepath.Join(scratch, "agy-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := seedAntigravityToken(home); err != nil {
		t.Fatal(err)
	}
	// the operator re-logged in; the card home's copy is now stale.
	if err := os.WriteFile(tokenPath, []byte("tok-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(antigravityTokenIn(home), []byte("tok-v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := agyLogHook + `
import sys, json, os
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    m = json.loads(line)
    if m.get("event") != "user": continue
    print("agy: unauthorized: token rejected", file=sys.stderr)
    out({"event":"result","status":"ERROR","error":"unauthorized"})
`
	ag, err := NewAntigravity(writeFakeAgy(t, script))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: scratch, Permission: PermissionAllowAll, ScratchDir: scratch})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	err = waitEventError(t, sess)
	var rf *RunFailure
	if !errAs(err, &rf) {
		t.Fatalf("err = %v (%T), want a *RunFailure", err, err)
	}
	_ = sess.Close()
	b, err := os.ReadFile(antigravityTokenIn(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "tok-v2" {
		t.Errorf("card home token = %q, want the operator's current tok-v2", b)
	}
}

// errAs is errors.As spelled with the test's shape.
func errAs(err error, target any) bool { return errors.As(err, target) }

// TestAntigravityMCPUnionAndPrune: two live sessions on one card home
// each keep their own reachable gummi entry in the file; a closed
// session's entry is pruned at the next spawn (INV-5).
func TestAntigravityMCPUnionAndPrune(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { antigravityExecPath = prev })

	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()
	home := filepath.Join(scratch, "agy-home")

	s1, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1-review.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := antigravityReadMCPConfig(t, home)
	if _, ok := got["gummi-FD-1"]; !ok {
		t.Errorf("union missing the first session's entry: %v", got)
	}
	if _, ok := got["gummi-FD-1-review"]; !ok {
		t.Errorf("union missing the second session's entry: %v", got)
	}

	// closing s1 leaves its entry in the file (prune is at next spawn)
	_ = s1.Close()
	got = antigravityReadMCPConfig(t, home)
	if _, ok := got["gummi-FD-1"]; !ok {
		t.Errorf("a closed session's entry vanished before the next spawn: %v", got)
	}
	// the next spawn prunes it.
	s3, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1-verify.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	got = antigravityReadMCPConfig(t, home)
	if _, ok := got["gummi-FD-1"]; ok {
		t.Errorf("the closed session's entry survived the next spawn: %v", got)
	}
	if _, ok := got["gummi-FD-1-verify"]; !ok {
		t.Errorf("the new spawn's entry missing: %v", got)
	}
	_ = s2.Close()
	_ = s3.Close()
}

// TestAntigravityConsultNeverLoadsTheStageEndpoint: a consult session
// spawned while a stage session is live keeps its own card home, whose
// config lists only the consult's read-only endpoint — never the stage
// session's, with its spec-writing and verdict tools — and the stage
// home's config never lists the consult's.
func TestAntigravityConsultNeverLoadsTheStageEndpoint(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { antigravityExecPath = prev })
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()

	stage, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		Role: RoleImplementer, FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	consult, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		Role: RoleConsult, FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/consult-FD-1-a.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consult.Close()

	got := antigravityReadMCPConfig(t, filepath.Join(scratch, "agy-home-consult"))
	if len(got) != 1 || got["gummi-consult-FD-1-a"] == nil {
		t.Errorf("consult home's servers = %v, want only its own gummi-consult-FD-1-a", got)
	}
	got = antigravityReadMCPConfig(t, filepath.Join(scratch, "agy-home"))
	if len(got) != 1 || got["gummi-FD-1"] == nil {
		t.Errorf("stage home's servers = %v, want only gummi-FD-1", got)
	}
}

func antigravityReadMCPConfig(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(antigravityMCPConfigRelPath)))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_config.json = %s, want an mcpServers map", b)
	}
	return servers
}

// TestAntigravityFailedConfigWriteKeepsTheHomeConsistent: a spawn whose
// mcp_config.json write fails errors loudly, leaves no entry behind in
// the home's union, and the card home still serves a later spawn. Four
// spawns fail concurrently on one shared home — their error paths may
// touch the home's sock map only under the adapter lock, never outside
// it (run with -race to hold that), and the failure is loud, not a
// half-wired session.
func TestAntigravityFailedConfigWriteKeepsTheHomeConsistent(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()

	scratch := t.TempDir()
	// Pre-create the card home so every spawn below shares one home
	// object — the union the error paths must not corrupt.
	if _, err := ag.homeFor(SessionOpts{ScratchDir: scratch}); err != nil {
		t.Fatal(err)
	}
	badConfig := filepath.Join(scratch, "agy-home", filepath.FromSlash(antigravityMCPConfigRelPath))
	opts := func(sock string) SessionOpts {
		return SessionOpts{
			WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
			FeatureID: "FD-1", MCPSockPath: sock,
		}
	}

	// The config path is occupied by a directory, so the write fails for
	// a wave of concurrent spawns on the same home — enough of them that
	// an unlocked unregister in the error path cannot help but overlap a
	// sibling's locked access to the same map.
	if err := os.MkdirAll(badConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	const failing = 32
	errc := make(chan error, failing)
	for i := 0; i < failing; i++ {
		go func(i int) {
			_, err := ag.NewSession(context.Background(), opts(fmt.Sprintf("/tmp/agy-fail-%d.sock", i)))
			errc <- err
		}(i)
	}
	for i := 0; i < failing; i++ {
		if err := <-errc; err == nil || !strings.Contains(err.Error(), "mcp_config") {
			t.Errorf("failing spawn %d: err = %v, want the config-write failure", i, err)
		}
	}

	// The home still serves a spawn: with the blocker gone, a real
	// session opens, and the union it writes carries its entry alone —
	// the failed spawns' registrations never leaked into it.
	if err := os.Remove(badConfig); err != nil {
		t.Fatal(err)
	}
	sess, err := ag.NewSession(context.Background(), opts("/tmp/agy-live.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitAgyIdle(t, sess)
	got := antigravityReadMCPConfig(t, filepath.Join(scratch, "agy-home"))
	if len(got) != 1 {
		t.Errorf("union after failed spawns = %v, want the live session's entry alone", got)
	}
	if _, ok := got[gummiMCPEntryName("/tmp/agy-live.sock")]; !ok {
		t.Errorf("union missing the live session's entry: %v", got)
	}
}

// TestAntigravityMCPConfigWrittenBeforeSpawn: the fake logs the config
// file's contents at startup — a spawn whose file was not yet in place
// would log null. This session's own entry is already there when its
// child starts.
func TestAntigravityMCPConfigWrittenBeforeSpawn(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_LOG", log)
	prev := antigravityExecPath
	antigravityExecPath = func() (string, error) { return "/opt/gummi-stub", nil }
	t.Cleanup(func() { antigravityExecPath = prev })

	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	scratch := t.TempDir()
	sess, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		FeatureID: "FD-1", MCPSockPath: "/tmp/mcp/FD-1.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for {
		var err error
		raw, err = os.ReadFile(log)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never logged its startup: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var sawEntry bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if mcp, ok := rec["mcp"].(string); ok && strings.Contains(mcp, "gummi-FD-1") {
			sawEntry = true
		}
	}
	if !sawEntry {
		t.Fatalf("the child started before its mcp_config.json entry was in place:\n%s", raw)
	}
}

// TestAntigravitySkillLinks: forwarded dirs are symlinked into the card
// home's skill root before the child spawns, each resolving through to
// the target's SKILL.md; a collision is first-wins; a dead link is
// re-pointed; a session with no SkillDirs touches nothing (INV-8).
func TestAntigravitySkillLinks(t *testing.T) {
	_ = antigravityTokenFixture(t, "tok")
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+fakeAgyArgvEcho), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := NewAntigravity(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()

	// two skill dirs outside the worktree, one shared basename
	skillA := t.TempDir()
	skillB := t.TempDir()
	deployDir := filepath.Join(skillA, "deploy")
	sharedDir := filepath.Join(skillB, "shared")
	writeSkill(t, skillA, "deploy", "deploy-first")
	writeSkill(t, skillB, "shared", "shared-B")
	writeSkill(t, filepath.Join(dir, "kept"), "solo", "solo-body")

	scratch := t.TempDir()
	home := filepath.Join(scratch, "agy-home")
	s1, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		SkillDirs: []string{deployDir, sharedDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".gemini", "config", "skills")
	if got, err := os.Readlink(filepath.Join(root, "deploy")); err != nil || got != deployDir {
		t.Errorf("deploy link = (%q, %v), want %q", got, err, deployDir)
	}
	if got, err := os.Readlink(filepath.Join(root, "shared")); err != nil || got != sharedDir {
		t.Errorf("shared link = (%q, %v), want %q", got, err, sharedDir)
	}
	if b, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")); err != nil || !strings.Contains(string(b), "deploy-first") {
		t.Errorf("SKILL.md not reachable through the link: %q, %v", b, err)
	}

	// first-wins: a later session forwarding a DIFFERENT dir with the
	// same basename does not re-point the live link.
	other := t.TempDir()
	writeSkill(t, other, "deploy", "deploy-second")
	otherDeploy := filepath.Join(other, "deploy")
	s2, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		SkillDirs: []string{otherDeploy},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")); err != nil || !strings.Contains(string(b), "deploy-first") {
		t.Errorf("collision re-pointed a live link: %q, %v", b, err)
	}

	// a session with no SkillDirs touches nothing.
	entries := antigravitySkillEntries(t, root)
	s3, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := antigravitySkillEntries(t, root); !slicesEqual(after, entries) {
		t.Errorf("a skill-less session changed the root: %v → %v", entries, after)
	}

	// the only removal: a link whose target has vanished is re-pointed
	// when a later session forwards a dir of the same basename.
	_ = os.RemoveAll(skillA)
	s4, err := ag.NewSession(context.Background(), SessionOpts{
		WorkDir: dir, Permission: PermissionAllowAll, ScratchDir: scratch,
		SkillDirs: []string{otherDeploy},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(root, "deploy")); err != nil || got != otherDeploy {
		t.Errorf("dead link not re-pointed: (%q, %v), want %q", got, err, otherDeploy)
	}
	if b, err := os.ReadFile(filepath.Join(root, "deploy", "SKILL.md")); err != nil || !strings.Contains(string(b), "deploy-second") {
		t.Errorf("re-pointed link does not resolve: %q, %v", b, err)
	}

	for _, s := range []*antigravitySession{s1.(*antigravitySession), s2.(*antigravitySession), s3.(*antigravitySession), s4.(*antigravitySession)} {
		_ = s.Close()
	}
	// the solo skill the card never forwarded is untouched — its
	// creation was out of gummi's hands.
	if _, err := os.Stat(filepath.Join(home, ".gemini", "config", "skills", "solo")); !os.IsNotExist(err) {
		t.Errorf("an unforwarded skill appeared: %v", err)
	}
}

func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func antigravitySkillEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAntigravityCatalogParsesIdsAndLeavesTheRealHomeUntouched: the
// probe's temp home is seeded, removed after the probe, and the
// operator's real home is byte-identical afterwards (INV-1/INV-2). The
// fake logs the HOME it actually saw, pinning that the probe ran under
// the temp home — a stale duplicate HOME entry in the child's
// environment would leave the operator's home first in line.
func TestAntigravityCatalogParsesIdsAndLeavesTheRealHomeUntouched(t *testing.T) {
	realToken := antigravityTokenFixture(t, "tok-catalog")
	realHome := filepath.Dir(realToken)
	before := antigravityHomeSnapshot(t, realHome)

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "agy")
	// a real-enough `agy models`: tab-separated id/description lines,
	// and a log of the HOME the probe's child resolved
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env python3\n"+agyLogHook+`
rec({"home": os.environ.get("HOME", "")})
print('gemini-3.1-pro-high\tGemini 3.1 Pro (High)')
print('gemini-3.1-pro-low\tGemini 3.1 Pro (Low)')
print()
`), 0o700); err != nil {
		t.Fatal(err)
	}
	// the probe must run with the binary on PATH
	path := dir
	if p := os.Getenv("PATH"); p != "" {
		path = p + string(os.PathListSeparator) + dir
	}
	t.Setenv("PATH", path)
	t.Setenv("AGY_LOG", log)

	ids, err := antigravityModelCatalog(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "gemini-3.1-pro-high" || ids[1] != "gemini-3.1-pro-low" {
		t.Errorf("ids = %v, want the id column", ids)
	}
	if after := antigravityHomeSnapshot(t, realHome); !antigravitySnapshotsEqual(before, after) {
		t.Errorf("the catalog probe touched the operator's home: %v → %v", before, after)
	}
	// the probe's own temp home is gone with it.
	if leftovers := antigravityTempHomes(); len(leftovers) != 0 {
		t.Errorf("probe temp homes left behind: %v", leftovers)
	}
	// and the child ran under a fresh seeded temp home, never the
	// operator's own home.
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var sawHome []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if h, ok := rec["home"].(string); ok {
			sawHome = append(sawHome, h)
		}
	}
	if len(sawHome) != 1 {
		t.Fatalf("fake agy logged %d home records, want 1:\n%s", len(sawHome), raw)
	}
	if sawHome[0] == realHome {
		t.Fatalf("the probe ran under the operator's home %q (INV-1)", sawHome[0])
	}
	if base := filepath.Base(sawHome[0]); !strings.HasPrefix(base, "gummi-agy-home-") {
		t.Fatalf("the probe's HOME = %q, want a seeded temp home", sawHome[0])
	}
}

// antigravityTempHomes lists gummi's temp agy homes in the system temp
// dir (sequential tests, so a leftover is this package's).
func antigravityTempHomes() []string {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gummi-agy-home-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestAntigravityCatalogEmptyMeansNoCatalog: output with no ids is an
// error (the picker falls back), and a binary that is missing fails.
func TestAntigravityCatalogEmptyMeansNoCatalog(t *testing.T) {
	if _, err := antigravityModelCatalog(context.Background(), filepath.Join(t.TempDir(), "no-such-agy")); err == nil {
		t.Error("a missing binary cataloged without error")
	}
}

// TestAntigravityToolEnvPinsTheRealHomesSettings: the redirected HOME is
// agy's alone — git, gpg and Go are pointed back at the operator's real
// settings and caches, an operator-set value always wins, and agy's own
// XDG roots are never pinned to the real home.
func TestAntigravityToolEnvPinsTheRealHomesSettings(t *testing.T) {
	opHome := t.TempDir()
	for _, d := range []string{".gnupg", filepath.Join(".config", "go")} {
		if err := os.MkdirAll(filepath.Join(opHome, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(opHome, ".gitconfig"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	goenv := filepath.Join(opHome, ".config", "go", "env")
	if err := os.WriteFile(goenv, []byte("GOPROXY=direct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(env []string) map[string]string {
		m := map[string]string{}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v // last wins, as for exec
		}
		return m
	}

	got := lookup(antigravityToolEnv([]string{"HOME=/card", "PATH=/bin"}, opHome))
	want := map[string]string{
		"HOME":                "/card",
		"GIT_CONFIG_GLOBAL":   filepath.Join(opHome, ".gitconfig"),
		"GNUPGHOME":           filepath.Join(opHome, ".gnupg"),
		"GOENV":               goenv,
		"GOPATH":              filepath.Join(opHome, "go"),
		"GOCACHE":             filepath.Join(opHome, ".cache", "go-build"),
		"GOLANGCI_LINT_CACHE": filepath.Join(opHome, ".cache", "golangci-lint"),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s pinned to %q; agy reads it, so it must stay redirected", k, got[k])
		}
	}

	// operator-set values pass through untouched; an operator XDG cache
	// root already covers the Go caches.
	got = lookup(antigravityToolEnv([]string{
		"HOME=/card", "GIT_CONFIG_GLOBAL=/mine", "GOPATH=/gp", "XDG_CACHE_HOME=/xc",
	}, opHome))
	if got["GIT_CONFIG_GLOBAL"] != "/mine" || got["GOPATH"] != "/gp" {
		t.Errorf("operator values overridden: %v", got)
	}
	if _, ok := got["GOCACHE"]; ok {
		t.Errorf("GOCACHE pinned despite an operator XDG_CACHE_HOME: %v", got)
	}

	// a go env file that sets GOPATH/GOCACHE itself is honored, not
	// overridden by a pinned default.
	if err := os.WriteFile(goenv, []byte("GOPATH=/from-file\nGOCACHE=/c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = lookup(antigravityToolEnv([]string{"HOME=/card"}, opHome))
	if _, ok := got["GOPATH"]; ok {
		t.Errorf("GOPATH pinned over the go env file's own: %v", got)
	}
	if _, ok := got["GOCACHE"]; ok {
		t.Errorf("GOCACHE pinned over the go env file's own: %v", got)
	}

	// nothing to pin to: no gitconfig, no gnupg — nothing invented.
	got = lookup(antigravityToolEnv([]string{"HOME=/card"}, t.TempDir()))
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GNUPGHOME", "GOENV"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s pinned to %q with nothing at the opHome home", k, got[k])
		}
	}
}
