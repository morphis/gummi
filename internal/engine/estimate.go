package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// estimatePrompt asks the scribe for a single machine-readable line.
const estimatePrompt = "Estimate the total cost to implement this feature, in credits " +
	"(1 credit ≈ $0.01). Read the spec/plan, and weigh the files likely touched, the test " +
	"surface, and the overall complexity. Reply with exactly one line and nothing else:\n" +
	"ESTIMATE: <number>"

var estimateRe = regexp.MustCompile(`(?i)ESTIMATE:\s*\$?\s*([0-9]+(?:\.[0-9]+)?)`)

// backendCostFactor scales the scribe's raw estimate per agent backend.
// The scribe prices work in credits as if a mid-tier hosted model were
// doing it; backends that run heavier models with side-calls burn a
// multiple of that for the same feature, and an envelope sized to the
// raw guess gates almost immediately.
var backendCostFactor = map[string]float64{"claude": 2.5}

// costFactor returns the estimate multiplier for the given backend name
// (1 when unknown). Empty backend resolves to the engine default.
func (e *Engine) costFactor(backend string) float64 {
	a := e.agentFor(backend)
	if a == nil {
		return 1
	}
	if f, ok := backendCostFactor[a.Name()]; ok {
		return f
	}
	return 1
}

// parseScribeEstimate extracts the credits from a scribe's reply (the last
// ESTIMATE: line wins), or (0,false) if none is present.
func parseScribeEstimate(text string) (float64, bool) {
	m := estimateRe.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// Estimate runs a one-shot scribe-role pass over the feature's spec and
// returns its proposed envelope in credits (DESIGN §5.1 plan-time
// estimation, the agent signal). The transient session is not tracked on
// the board. Returns (0,nil) when the scribe declines or its reply can't
// be parsed — estimation is advisory and never fatal.
func (e *Engine) Estimate(ctx context.Context, f domain.Feature) (float64, error) {
	model, backend, _ := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return 0, nil
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return 0, err
	}
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:         workDir,
		ArtifactPath:    specPath,
		Role:            agent.RoleScribe,
		Model:           model,
		Permission:      e.cfg.Permission,
		SystemHints:     []string{fmt.Sprintf("The feature's spec is at %s; read it first.", specPath)},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, estimatePrompt); err != nil {
		return 0, err
	}
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				v, _ := parseScribeEstimate(text.String())
				return v * e.costFactor(backend), nil
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				v, _ := parseScribeEstimate(text.String())
				return v * e.costFactor(backend), nil
			case agent.EventError:
				return 0, ev.Err
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}
