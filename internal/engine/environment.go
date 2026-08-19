package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxEnvironmentCard caps the workspace environment card that is prepended
// to every stage session's hints. The card rides every system prompt, so an
// unbounded file would be a per-turn token tax; the cap is the guardrail.
const maxEnvironmentCard = 8 << 10

// environmentCard returns the workspace's environment card — the operator-
// authored description of machinery outside the repo (dev VMs, hardware,
// services) that stage agents must know exists. It reads
// <workspaceRoot>/.gummi/environment.md once per Engine lifetime; edits to
// the file require an Engine restart. A missing or unreadable file yields ""
// and never returns an error.
func (e *Engine) environmentCard() string {
	e.envOnce.Do(func() {
		path := filepath.Join(e.cfg.Workspace.GummiDir(), "environment.md")
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()

		raw, err := io.ReadAll(io.LimitReader(f, maxEnvironmentCard+1))
		if err != nil {
			return
		}
		if len(raw) > maxEnvironmentCard {
			e.envCard = string(raw[:maxEnvironmentCard])
			if e.envWarn != nil {
				e.envWarn(fmt.Sprintf("environment card %s exceeds %d bytes; using the first %d", path, maxEnvironmentCard, maxEnvironmentCard))
			}
		} else {
			e.envCard = string(raw)
		}
	})
	return e.envCard
}

// flushEnvNotices drains buffered environment-card warnings onto s's activity
// feed. The swap-to-nil under envMu means concurrent callers cannot duplicate
// or lose a warning. It is safe to call with a nil session.
func (e *Engine) flushEnvNotices(s *Session) {
	if s == nil {
		return
	}
	e.envMu.Lock()
	notices := e.envNotices
	e.envNotices = nil
	e.envMu.Unlock()
	for _, n := range notices {
		s.appendActivity(n)
	}
}
