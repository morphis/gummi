package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxEnvironmentCard caps the workspace environment card that is prepended
// to every stage session's hints. The card rides every system prompt, so an
// unbounded file would be a per-turn token tax; the cap is the guardrail.
const maxEnvironmentCard = 8 << 10

// environmentCard returns the workspace's environment card — the operator-
// authored description of machinery outside the repo (dev VMs, hardware,
// services) that stage agents must know exists. It reads
// <workspaceRoot>/.gummi/environment.md once per Engine lifetime, then
// appends any configured instruction files. Edits to these files require an
// Engine restart. A missing or unreadable file yields "" and never returns
// an error.
func (e *Engine) environmentCard() string {
	e.envOnce.Do(func() {
		var b strings.Builder

		path := filepath.Join(e.cfg.Workspace.GummiDir(), "environment.md")
		if raw, err := os.ReadFile(path); err == nil {
			b.Write(raw)
		}

		for _, inst := range e.cfg.Instructions {
			raw, err := os.ReadFile(inst)
			if err != nil {
				if e.envWarn != nil {
					e.envWarn(fmt.Sprintf("instruction file %s could not be read: %v", inst, err))
				}
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.Write(raw)
		}

		raw := b.String()
		if len(raw) > maxEnvironmentCard {
			e.envCard = raw[:maxEnvironmentCard]
			if e.envWarn != nil {
				e.envWarn(fmt.Sprintf("environment card exceeds %d bytes; using the first %d", maxEnvironmentCard, maxEnvironmentCard))
			}
		} else {
			e.envCard = raw
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
