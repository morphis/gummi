package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// decomposeDir holds one pending-decompose file per RS card awaiting
// --approve/--request-changes — a convenience cache, not a state
// authority: unsettledSliceRows on the doc is always the source of truth,
// which is why a stale or missing pending file is safe to clear or ignore.
func (e *Engine) decomposeDir() string {
	return filepath.Join(e.cfg.Workspace.StateDir(), "decompose")
}

func (e *Engine) pendingDecomposePath(id domain.FeatureID) string {
	return filepath.Join(e.decomposeDir(), string(id)+".json")
}

// SavePendingDecompose persists a decompose pass's proposals + coverage so
// a later --approve/--request-changes can act on them without re-running
// the architect.
func (e *Engine) SavePendingDecompose(id domain.FeatureID, res domain.IngestResult) error {
	if err := os.MkdirAll(e.decomposeDir(), 0o750); err != nil {
		return fmt.Errorf("creating decompose dir: %w", err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encoding pending decompose: %w", err)
	}
	return atomicfile.Write(e.pendingDecomposePath(id), data, 0o600)
}

// LoadPendingDecompose reads a card's pending decompose proposals. A
// missing file returns ok=false, err=nil — there is simply nothing pending.
func (e *Engine) LoadPendingDecompose(id domain.FeatureID) (domain.IngestResult, bool, error) {
	data, err := os.ReadFile(e.pendingDecomposePath(id))
	if os.IsNotExist(err) {
		return domain.IngestResult{}, false, nil
	}
	if err != nil {
		return domain.IngestResult{}, false, fmt.Errorf("reading pending decompose: %w", err)
	}
	var res domain.IngestResult
	if err := json.Unmarshal(data, &res); err != nil {
		return domain.IngestResult{}, false, fmt.Errorf("decoding pending decompose: %w", err)
	}
	return res, true, nil
}

// ClearPendingDecompose removes a card's pending decompose file. Removing
// a file that doesn't exist is an idempotent no-op.
func (e *Engine) ClearPendingDecompose(id domain.FeatureID) error {
	err := os.Remove(e.pendingDecomposePath(id))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing pending decompose: %w", err)
	}
	return nil
}
