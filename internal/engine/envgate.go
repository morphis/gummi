package engine

import (
	"os"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// gateVerifyVerdict stamps a verdict floor when a bug's autonomous Verify
// finish has a prerequisite present but the artifact tags no [env:] live
// checks and carries no human waiver. It only ever downgrades a raw Pass to
// Blocked; the actual downgrade happens in verdict.SessionVerdict from the
// stamped floor.
func (e *Engine) gateVerifyVerdict(s *Session) {
	if s.Feature.Stage != domain.StageVerify {
		return
	}
	if s.Feature.Kind != domain.KindBug {
		return
	}
	if !hasCleanPresentProbe(s) {
		return
	}

	content, err := os.ReadFile(s.SpecPath())
	if err != nil {
		s.appendActivity("Omission gate skipped: artifact could not be read")
		return
	}

	reason := omissionGateReason(s.Feature.Kind, true, string(content))
	if reason == "" {
		return
	}

	s.setVerdictFloor("blocked", reason)
	s.appendActivity("Pass downgraded to blocked: present prerequisite + zero [env:] live checks + no waiver")
}

// omissionGateReason is the shared predicate behind both the live Verify
// verdict floor and Advance's verify→done omission gate. It reports a
// human-facing reason when the omission condition is met (bug kind, a clean
// present env prerequisite probe, zero [env:] live checks in the artifact,
// and no %% @user: no-live-check waiver). An empty return means the gate
// does not block.
func omissionGateReason(kind domain.Kind, cleanPresentProbe bool, content string) string {
	if kind != domain.KindBug {
		return ""
	}
	if !cleanPresentProbe {
		return ""
	}

	tags := spec.EnvTags(content, kind)
	if len(tags) > 0 {
		return ""
	}
	if _, ok := spec.NoLiveCheckWaiver(content, kind); ok {
		return ""
	}

	return "Verify passed without exercising any [env:] live check, but a prerequisite probed present. Add a live [env:] check or a human %% @user: no-live-check waiver."
}

// hasCleanPresentProbe reports whether any env prerequisite probed cleanly
// present (Err nil and Present true). ABSENT and errored probes never arm
// the omission gate.
func hasCleanPresentProbe(s *Session) bool {
	s.mu.Lock()
	probes := s.envProbes
	s.mu.Unlock()
	for _, r := range probes {
		if r.Err == nil && r.Present {
			return true
		}
	}
	return false
}
