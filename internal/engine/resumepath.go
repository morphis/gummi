package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// resumeSessionPath returns the durable transcript path for one (feature,
// role, flavor) triple, under the workspace's state dir. The flavor suffix
// is emitted only for the borrowed passes (critique, rebase) — a stage's
// own transcript keeps a bare, stable name across the life of the card, so
// a rebase or critique pass borrowing that stage's role never collides
// with the stage's own conversation. Empty return means the workspace
// can't host a resume path (never in production; retained for future
// non-file workspaces).
func resumeSessionPath(w state.Workspace, id domain.FeatureID, role agent.Role, flavor runFlavor) string {
	dir := w.StateDir()
	if dir == "" {
		return ""
	}
	var suffix string
	switch flavor {
	case flavorStage:
		suffix = ""
	case flavorCritique:
		suffix = "-critique"
	case flavorRebase:
		suffix = "-rebase"
	default:
		panic(fmt.Sprintf("resumeSessionPath: unknown flavor %d", flavor))
	}
	return filepath.Join(dir, "sessions", string(id)+"-"+string(role)+suffix+".jsonl")
}

// clearResumeTranscript removes any pre-existing transcript at the derived
// path so a FRESH session starts clean. IsNotExist is silent; any other
// error is appended to the session's activity so it stays visible without
// blocking the spawn — the worst case is that the FRESH session continues
// an old transcript, and the very next turn's session_id event overwrites
// it anyway.
func (e *Engine) clearResumeTranscript(s *Session, flavor runFlavor) {
	path := resumeSessionPath(e.cfg.Workspace, s.Feature.ID, s.Role, flavor)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.appendActivity(fmt.Sprintf("could not clear prior session transcript at %s: %v", path, err))
	}
}
