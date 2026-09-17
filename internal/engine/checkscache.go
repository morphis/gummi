package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// The workspace's memory of what builds, tests and lints the repository it
// manages.
//
// Check discovery is a scribe session that surveys the repo — Makefile, CI
// workflows, lint config, a probe or two — and writes the commands into the
// card's spec. It is good work and it is not cheap: measured on
// canonical/lxd, 215 s and 29 requests for one card. It is also the same
// work for every card in that repository, because the answer is a property
// of the repo's build files, not of the change. Two cards in one repo ran
// it twice and got two DIFFERENT command sets, so the second card shipped
// against a different definition of "green" than the first.
//
// The cache turns the survey into a per-repo fact with an explicit
// invalidation: the fingerprint below covers the files that decide the
// answer, so a repo that changes how it builds gets surveyed again and a
// repo that merely gained commits does not.
const checksCacheFile = "checks-cache.json"

// checksFingerprintFiles are the files whose CONTENT decides what the
// repo's build/test/lint commands are. A change to any of them is a reason
// to survey again; a change to anything else is not.
//
// Deliberately a fixed list rather than a scan: the point is a cheap,
// reproducible hash, and a repo whose build is decided by a file not
// listed here still re-surveys whenever one of its listed neighbours moves
// (a new dependency, a new CI job) — and, failing that, when the operator
// deletes the cache. The cost of a stale entry is bounded because the
// checks block still rides the plan gate for a human to read.
var checksFingerprintFiles = []string{
	"Makefile", "makefile", "GNUmakefile", "Justfile", "justfile", "Taskfile.yml",
	"go.mod", "package.json", "pyproject.toml", "setup.cfg", "tox.ini",
	"Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts", "CMakeLists.txt",
	".golangci.yml", ".golangci.yaml", ".eslintrc.json", ".eslintrc.js", "ruff.toml",
	".pre-commit-config.yaml", "AGENTS.md", "CLAUDE.md",
}

// checksFingerprintGlobs extend the fixed list with the CI definitions,
// which are the most direct statement a repo makes about what its checks
// are.
var checksFingerprintGlobs = []string{
	".github/workflows/*.yml", ".github/workflows/*.yaml",
	".gitlab-ci.yml", ".circleci/config.yml",
}

// checksCacheEntry is one repository's remembered survey.
type checksCacheEntry struct {
	// Fingerprint is the hash of the build-defining files at the time the
	// survey ran. A mismatch re-runs discovery.
	Fingerprint string `json:"fingerprint"`
	// Checks is what the scribe found, in the order it named them.
	Checks []domain.Check `json:"checks"`
}

// checksCachePath is where the workspace keeps the cache. Empty when the
// engine has no workspace to keep it in (tests, ephemeral engines), which
// disables caching rather than failing.
func (e *Engine) checksCachePath() string {
	dir := e.cfg.Workspace.GummiDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, checksCacheFile)
}

// checksFingerprint hashes the build-defining files under root. Returns ""
// when root is empty or no such file exists — a repository that states
// nothing about its own build has nothing stable to key a cache on, so it
// is surveyed every time rather than remembered wrongly.
func checksFingerprint(root string) string {
	if root == "" {
		return ""
	}
	var paths []string
	for _, name := range checksFingerprintFiles {
		p := filepath.Join(root, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			paths = append(paths, p)
		}
	}
	for _, glob := range checksFingerprintGlobs {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		if err != nil {
			continue
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = filepath.Base(p)
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadChecksCache reads the whole cache, keyed by repository root. A
// missing or corrupt file reads as empty: the cache is an optimisation and
// must never be the reason a card cannot start.
func (e *Engine) loadChecksCache() map[string]checksCacheEntry {
	path := e.checksCachePath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]checksCacheEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// cachedChecks returns the remembered survey for root when its fingerprint
// still matches what is on disk — and, failing that, any other root in the
// cache whose fingerprint is identical.
//
// The fallback is the whole point. gummi's model is a worktree per card, so
// two cards in one repository are two different absolute paths, and a cache
// keyed on the path alone can only ever hit for a card that runs where an
// earlier one ran. A goal makes this certain rather than likely: its
// children resolve their repo root through the goal's own worktree
// (PLAN-goal.md decision 1) while the goal's own gate resolves the
// workspace root, so on the lxd autopilot drive the cache ended the run
// holding two entries with IDENTICAL fingerprints and different keys, and
// the second survey — 79.3 credits — re-derived a byte-identical answer.
//
// The fingerprint is what decides whether a survey is still valid, so it is
// what decides reuse; the path is only where it was learned. Selection is
// deterministic (shortest key, then lexicographic) because two entries
// sharing a fingerprint are two independent surveys that need not have
// agreed, and "the second card gets the SAME floor as the first" is the
// property this cache exists for — a map-order pick would hand out
// whichever of them came up first. Shortest wins because the root nearest
// the workspace is the one a per-card worktree is a copy of.
func (e *Engine) cachedChecks(root string) ([]domain.Check, bool) {
	fp := checksFingerprint(root)
	if fp == "" {
		return nil, false
	}
	cache := e.loadChecksCache()
	if entry, ok := cache[root]; ok && entry.Fingerprint == fp && len(entry.Checks) > 0 {
		return entry.Checks, true
	}
	best, found := "", false
	for key, entry := range cache {
		if key == root || entry.Fingerprint != fp || len(entry.Checks) == 0 {
			continue
		}
		if !found || len(key) < len(best) || (len(key) == len(best) && key < best) {
			best, found = key, true
		}
	}
	if !found {
		return nil, false
	}
	return cache[best].Checks, true
}

// rememberChecks records a fresh survey for root. Best-effort: a cache
// that cannot be written costs the next card a survey it would have run
// anyway, so an error here is never surfaced.
func (e *Engine) rememberChecks(root string, checks []domain.Check) {
	path := e.checksCachePath()
	if path == "" || len(checks) == 0 {
		return
	}
	fp := checksFingerprint(root)
	if fp == "" {
		return
	}
	cache := e.loadChecksCache()
	if cache == nil {
		cache = map[string]checksCacheEntry{}
	}
	cache[root] = checksCacheEntry{Fingerprint: fp, Checks: checks}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	_ = atomicfile.Write(path, raw, 0o600)
}
