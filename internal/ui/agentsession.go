package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/state"
)

// agentsession.go remembers which hosted-CLI backend last ran in this
// workspace, in .gummi/agent-session.json. ensureAgent (agenttab.go)
// reads it before spawning and writes it after: reading it is what lets
// a same-backend spawn resume its own last conversation (agentResumeArgs)
// instead of always starting fresh, and writing it is what makes the
// *next* spawn able to tell.
//
// It is deliberately not part of domain/state: it names a CLI process on
// this machine, not a card or a feature, and it has no business surviving
// a workspace move to another machine the way the store's own records do.

// agentSessionRecord is the file's whole contents: which backend
// (agentcli's stable name, or the resolved binary itself for a raw
// GUMMI_ATTACH_CMD — see resolveAgentAttach) last spawned, and when.
type agentSessionRecord struct {
	Backend string    `json:"backend"`
	At      time.Time `json:"at"`
}

// agentSessionPath is where the record lives: directly under .gummi,
// alongside the other small workspace-scoped files (config.yaml), not
// under state/ — it is worth a human glancing at, not machinery to hide.
func agentSessionPath(ws state.Workspace) string {
	return filepath.Join(ws.GummiDir(), "agent-session.json")
}

// loadAgentSession reads the last-spawned record, or ok=false when there
// is none yet (a fresh workspace, or one that has never hosted an agent
// tab) or the workspace isn't wired to a real root at all (a detached
// shell in tests). Any read or parse error is folded into ok=false rather
// than surfaced: a missing or corrupt resume record just means the next
// spawn starts fresh, which is always a safe fallback.
func loadAgentSession(ws state.Workspace) (rec agentSessionRecord, ok bool) {
	if ws.Root == "" {
		return agentSessionRecord{}, false
	}
	raw, err := os.ReadFile(agentSessionPath(ws))
	if err != nil {
		return agentSessionRecord{}, false
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return agentSessionRecord{}, false
	}
	return rec, rec.Backend != ""
}

// saveAgentSession records that backend just spawned, at "at". A
// detached workspace (ws.Root == "") is a silent no-op — tests and any
// caller with nowhere to write have nothing to persist to, the same
// shape as SetAgentConfig's empty configPath.
func saveAgentSession(ws state.Workspace, backend string, at time.Time) error {
	if ws.Root == "" {
		return nil
	}
	raw, err := json.Marshal(agentSessionRecord{Backend: backend, At: at})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ws.GummiDir(), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(agentSessionPath(ws), raw, 0o600)
}
