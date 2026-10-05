package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/childproc"
	"github.com/morphis/gummi/internal/rmtree"
)

// antigravityhome.go is the per-card redirected HOME the antigravity
// adapter spawns every agy child under. agy has no config-dir flag or
// environment variable — `$HOME/.gemini` is hardcoded — so isolation is
// bought at the process boundary: the child gets a HOME of gummi's
// choosing, and everything it writes (the OAuth token copy, settings,
// mcp_config.json, its conversation databases) lands inside that tree,
// never inside the operator's own config. gummi only ever READS the
// operator's real token (INV-2); it never writes or moves it.
//
// Two home shapes exist:
//
//   - the card home, `<SessionOpts.ScratchDir>/agy-home`: created lazily,
//     kept across restarts (agy's conversations live under it, so resume
//     survives), and removed with the card's own cleanup pass, which
//     deletes the scratch-files directory the home sits under. A consult
//     session's is `agy-home-consult` beside it, so it never loads the
//     stage session's MCP endpoint (Antigravity.homeFor);
//   - the temp home, one per session: the intended home for one-shot
//     session kinds (they never resume), for doctor probes, and for the
//     model-catalog probe; removed when the session closes.
//
// Whatever the shape, a home gummi creates is seeded from the operator's
// OAuth token when it lacks one, so a redirected home can authenticate.

const (
	// antigravityTokenRelPath is the OAuth token file agy keeps under its
	// config tree — not in the OS keyring — which is what makes a
	// redirected home seedable at all.
	antigravityTokenRelPath = ".gemini/antigravity-cli/antigravity-oauth-token" //nolint:gosec // a file path, not a credential literal
	// antigravityMCPConfigRelPath is where agy reads its MCP servers
	// config from — the migrated `config/` path (verified on agy 1.2.16;
	// the legacy `.gemini/mcp_config.json` is what an old build's toggle
	// wrote to by mistake). The file's shape is Claude Code's mcpServers
	// map, honoring each entry's args and env.
	antigravityMCPConfigRelPath = ".gemini/config/mcp_config.json"
	// antigravitySkillsRelDir is agy's global skill customization root,
	// scanned alongside a project's own skill directories. Forwarded
	// workspace skills are symlinked here, named by their basename.
	antigravitySkillsRelDir = ".gemini/config/skills"
)

// operatorAntigravityTokenPath locates the operator's own OAuth token —
// the one file of the real home gummi ever reads.
func operatorAntigravityTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("antigravity adapter: locating the operator's home: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(antigravityTokenRelPath)), nil
}

// seedAntigravityToken copies the operator's OAuth token into a gummi
// home that lacks one, so the redirected home can authenticate. A home
// that already has a copy keeps it (agy refreshes it in place there; the
// card home's copy is its own afterwards). An operator with no token at
// all is left alone: the child reports the auth failure itself, which is
// the visible failure INV-7 asks for, not a silent one.
func seedAntigravityToken(home string) error {
	src, err := operatorAntigravityTokenPath()
	if err != nil {
		return err
	}
	dst := filepath.Join(home, filepath.FromSlash(antigravityTokenRelPath))
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("antigravity adapter: reading the operator's OAuth token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("antigravity adapter: creating %s: %w", filepath.Dir(dst), err)
	}
	// 0o600: this is a credential; it must not land group/world-readable
	// on a shared host.
	if err := os.WriteFile(dst, b, 0o600); err != nil { //nolint:gosec // dst is a path under the adapter's own home, never operator input
		return fmt.Errorf("antigravity adapter: seeding %s: %w", dst, err)
	}
	return nil
}

// antigravityAuthMarkers are the phrases agy uses when it cannot
// authenticate, matched case-insensitively against a failure's
// diagnostic to decide whether the card home's token copy is stale and
// re-seeding might fix it. Deliberately narrow — each is a credential
// phrase, not a general failure word — because a false positive would
// overwrite a card home's (possibly refreshed) token with an older one.
var antigravityAuthMarkers = []string{
	"unauthorized",
	"not authenticated",
	"authentication",
	"authenticate",
	"401",
	"invalid_grant",
	"login required",
	"credential",
}

// antigravityAuthFailure reports whether a diagnostic reads as an
// authentication failure.
func antigravityAuthFailure(diag string) bool {
	lower := strings.ToLower(diag)
	for _, m := range antigravityAuthMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// reseedAntigravityToken overwrites a card home's token copy with the
// operator's current one, the repair path after agy reported an auth
// failure (the copy may be stale — the operator re-logged in since). It
// runs only when both files exist and differ, so a genuinely dead
// operator token is never papered over by rewriting it onto itself.
func reseedAntigravityToken(home string) error {
	src, err := operatorAntigravityTokenPath()
	if err != nil {
		return err
	}
	dst := filepath.Join(home, filepath.FromSlash(antigravityTokenRelPath))
	b, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing better to seed from; the error stands
		}
		return fmt.Errorf("antigravity adapter: reading the operator's OAuth token: %w", err)
	}
	if cur, err := os.ReadFile(dst); err == nil && string(cur) == string(b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("antigravity adapter: creating %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil { //nolint:gosec // dst is a path under the adapter's own home, never operator input
		return fmt.Errorf("antigravity adapter: re-seeding %s: %w", dst, err)
	}
	return nil
}

// antigravityHome is one redirected HOME and the live sessions sharing
// it: a card home (temp false, kept for cross-restart resume) or a
// per-session temp home (temp true, removed at that session's Close).
type antigravityHome struct {
	dir string
	// socks holds one entry per live session bound to this home with an
	// MCP endpoint: socket path → the feature id that session's server
	// dials. The union writer renders exactly these; a closed session
	// unregisters, so the next spawn's union prunes it.
	socks map[string]string
	// temp marks a home the adapter must remove itself (at the owning
	// session's Close); a card home is removed with the card's cleanup.
	temp bool
}

// newAntigravityHome creates dir (0o700, matching the engine's
// scratch-files creation) and seeds its token.
func newAntigravityHome(dir string, temp bool) (*antigravityHome, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("antigravity adapter: creating card home %s: %w", dir, err)
	}
	if err := seedAntigravityToken(dir); err != nil {
		return nil, err
	}
	return &antigravityHome{dir: dir, temp: temp, socks: map[string]string{}}, nil
}

// newAntigravityTempHome creates a seeded temp home for one session.
func newAntigravityTempHome() (*antigravityHome, error) {
	dir, err := os.MkdirTemp("", "gummi-agy-home-*")
	if err != nil {
		return nil, fmt.Errorf("antigravity adapter: creating temp home: %w", err)
	}
	// MkdirTemp honors no mode argument before Go 1.24's ...WithTempDir;
	// force 0o700 so the seeded token's directory is private regardless.
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a home directory is 0700 by design; the token inside it is 0600
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("antigravity adapter: restricting temp home: %w", err)
	}
	if err := seedAntigravityToken(dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &antigravityHome{dir: dir, temp: true, socks: map[string]string{}}, nil
}

// register adds a session's MCP entry to the home's union. sockPath ""
// registers nothing (a session gummi never wired tools for has no entry
// to keep alive).
func (h *antigravityHome) register(sockPath, featureID string) {
	if sockPath == "" {
		return
	}
	h.socks[sockPath] = featureID
}

// unregister drops a session's entry — its child is gone, and the union
// written at the next spawn must not carry it.
func (h *antigravityHome) unregister(sockPath string) {
	delete(h.socks, sockPath)
}

// gummiMCPEntryName renders one live session's stable mcpServers key:
// `gummi-<socket basename>` — unique per session (the engine's socket
// names carry the card and endpoint kind), stable across that session's
// own spawns, and never colliding between concurrently live sessions on
// one card.
func gummiMCPEntryName(sockPath string) string {
	base := strings.TrimSuffix(filepath.Base(sockPath), ".sock")
	var b strings.Builder
	b.WriteString("gummi-")
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// unionMCPServers renders the mcpServers map for a card home's
// mcp_config.json: one entry per live session, each a `gummi __mcp`
// stdio server the shape buildGummiMCPServerConfig established — with
// agy's own timeout key, which reads seconds (`timeoutSeconds`), not
// Claude Code's milliseconds (`timeout`). Sorted by socket path so two
// writers over the same live set produce the same bytes.
func unionMCPServers(exe string, socks map[string]string) map[string]any {
	paths := make([]string, 0, len(socks))
	for p := range socks {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	servers := make(map[string]any, len(socks))
	for _, p := range paths {
		// The entry shape is gummiMCPServerEntry's, with agy's own
		// timeout key — it reads seconds (`timeoutSeconds`), not Claude
		// Code's milliseconds (`timeout`).
		servers[gummiMCPEntryName(p)] = gummiMCPServerEntry(exe, socks[p], p,
			"timeoutSeconds", int64(mcpCallTimeout/time.Second))
	}
	return servers
}

// writeMCPConfig renders and writes the home's mcp_config.json from its
// live sessions. Called between spawns under the adapter's mutex — the
// file has exactly one writer — and always before a child spawns, since
// agy reads the config once at startup.
func (h *antigravityHome) writeMCPConfig(exe string) error {
	cfg := map[string]any{"mcpServers": unionMCPServers(exe, h.socks)}
	b, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("antigravity adapter: rendering %s: %w", antigravityMCPConfigRelPath, err)
	}
	path := filepath.Join(h.dir, filepath.FromSlash(antigravityMCPConfigRelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("antigravity adapter: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("antigravity adapter: writing %s: %w", path, err)
	}
	return nil
}

// materializeAntigravitySkills symlinks a session's forwarded skill
// directories into the home's skill customization root, named by their
// basename — agy discovers them there and reads SKILL.md (and reference
// files) through the link, so the targets keep their real paths.
//
// First-wins on a basename collision: an existing link pointing at a
// live directory stays. The only removal is a link whose target
// directory has vanished, which is re-pointed at the incoming dir. A
// session with no skills touches nothing, so a session that forwards
// none cannot strip the links of another session sharing its home.
func materializeAntigravitySkills(home string, dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	root := filepath.Join(home, filepath.FromSlash(antigravitySkillsRelDir))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("antigravity adapter: creating %s: %w", root, err)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			// A forwarded dir that does not resolve is the engine's
			// warning to raise (skills.go), not this loop's job; leave the
			// root as it is.
			continue
		}
		target := filepath.Join(root, filepath.Base(dir))
		if cur, err := os.Readlink(target); err == nil {
			if st, err := os.Stat(cur); err == nil && st.IsDir() {
				continue // first-wins
			}
			_ = os.Remove(target) // dead target: re-point below
		} else if _, err := os.Lstat(target); err == nil {
			continue // a real file/dir with that name: first-wins, never removed
		}
		if err := os.Symlink(dir, target); err != nil && !os.IsExist(err) {
			return fmt.Errorf("antigravity adapter: linking skill %s: %w", dir, err)
		}
	}
	return nil
}

// agyModelsTimeout bounds one `agy models` probe: a catalog probe that
// hangs must not hang a picker open behind it.
const antigravityModelsTimeout = 15 * time.Second

// antigravityModelCatalog is AntigravityModelCatalog's body: spawn
// `agy models` under a freshly created, seeded temp home, parse the
// `id<TAB>description` lines, and remove the home. INV-1 covers probes
// as much as sessions: an unseeded temp home cannot authenticate (the
// picker's antigravity catalog would silently always be empty), and
// probing the operator's inherited environment would let agy write token
// refreshes into the real config.
func antigravityModelCatalog(ctx context.Context, bin string) ([]string, error) {
	if bin == "" {
		bin = "agy"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("antigravity binary %q not found: %w", bin, err)
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityModelsTimeout)
	defer cancel()
	home, err := newAntigravityTempHome()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rmtree.RemoveAll(home.dir) }()
	cmd := exec.CommandContext(ctx, resolved, "models")
	// INV-1 covers probes: the child's HOME is the temp home, and the
	// real HOME entry is filtered out first — appending after an
	// unfiltered environ would leave the operator's HOME in the child's
	// environment (first match wins), pointing agy at the real config.
	cmd.Env = envWithAntigravityHome(os.Environ(), home.dir)
	childproc.Group(cmd)
	out, err := childproc.Output(cmd)
	if err != nil {
		return nil, fmt.Errorf("antigravity catalog: %w", err)
	}
	return parseAntigravityModels(string(out)), nil
}

// parseAntigravityModels takes the id column of `agy models` output:
// tab-separated `id<TAB>description` lines, blank lines dropped.
func parseAntigravityModels(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, _, _ := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
