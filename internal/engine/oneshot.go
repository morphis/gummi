package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// A one-shot card pass is a single scribe-role turn asked about one
// card: open a read-only session in the card's worktree, send one
// prompt, take the reply, close. It is the shape Estimate and
// DiscoverChecks already had — this file is that shape named, shared,
// and metered.
//
// METERING IS THE PART THAT WAS MISSING. Estimate and DiscoverChecks
// each spend real credits and neither books them: their transient
// sessions are not on the board and their usage events go nowhere, so
// the card's masthead reports a number smaller than the card actually
// cost. That was tolerable while the only one-shots were sized once at
// approval; it is not tolerable for a pass that runs at every approve-
// shaped stop. Every turn through here books its usage against the
// card's CURRENT stage — stamped when the pass opens, the way
// ConsultSession stamps its own — so the envelope the reader is looking
// at is the money that was actually spent.

// ErrNoScribe reports that there is no backend to run a one-shot card
// pass on at all: no agent configured, or none the profile's scribe role
// resolves to.
//
// It is deliberately distinct from a pass that ran and said nothing. A
// caller must tell the two apart, because they mean opposite things: a
// pass that answered nothing is a statement about the card (there is
// nothing to say, so say nothing), while no backend at all is a
// statement about the machine, and the caller keeps whatever it would
// have done without a model.
var ErrNoScribe = errors.New("no agent backend available for a one-shot card pass")

// oneShot runs one scribe-role turn about f and returns the assistant's
// text. hints are appended to the session's system prompt after the two
// this always sets (read-only, and where the artifact is).
//
// The session is read-only by instruction and by shape: it is given the
// card's worktree as its cwd and the artifact as an extra read allow,
// and it is closed the moment the reply lands. Nothing here writes.
func (e *Engine) oneShot(ctx context.Context, f domain.Feature, prompt string, hints ...string) (string, error) {
	rc, backend := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return "", ErrNoScribe
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", err
	}
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:      workDir,
		ArtifactPath: specPath,
		Role:         agent.RoleScribe,
		Model:        rc.Model,
		Provider:     rc.Provider,
		Think:        rc.Think,
		Permission:   e.cfg.Permission,
		SystemHints: append([]string{
			"You are reading this card read-only; do not modify any file.",
			fmt.Sprintf("The card's artifact is at %s.", specPath),
		}, hints...),
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, prompt); err != nil {
		return "", err
	}
	// The stage is read once, here, and every sample is booked against
	// it — not against whatever the card's stage has become by the time
	// the reply lands. A pass that started at verify and finished after
	// something bounced the card is still verify's spend.
	stage := f.Stage
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return text.String(), nil
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventUsage:
				e.recordOneShotUsage(f.ID, stage, ev.Usage)
			case agent.EventIdle, agent.EventBudgetExhausted:
				// Budget exhaustion is a soft stop: the in-flight reply
				// is done and no further turn will run, so waiting for an
				// idle that is not coming would hang the caller.
				return text.String(), nil
			case agent.EventError:
				return "", ev.Err
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// recordOneShotUsage books one usage sample from a one-shot pass against
// the card's overall spend and its (stage, role, model) breakdown, under
// the scribe role.
//
// It is recordUsage's shape without the pending-estimate bookkeeping,
// and the omission is deliberate rather than a shortcut: that machinery
// exists to retire an adapter's earlier estimates when a later settle
// event corrects them, and a one-shot session is closed before any
// settle could arrive. Booking the estimate and never correcting it is
// the honest account of a pass whose real price is never reported.
//
// The credit equivalent is taken at the default rate for the same
// reason: the per-session rate lives on a *Session this pass does not
// have, and a token-only backend would otherwise contribute zero to a
// credits-denominated envelope.
func (e *Engine) recordOneShotUsage(id domain.FeatureID, stage domain.Stage, u agent.Usage) {
	if !e.cfg.Persist || e.cfg.Store == nil {
		return
	}
	credits := domain.Spend{
		Credits: u.Credits, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
	}.CreditEquivalent()
	// Estimated is booked on exactly the rule recordUsage uses, and the
	// match matters more than the rule: the masthead prefixes "~" and
	// labels a figure "est." from this field, so the same usage sample
	// booked by a stage and by a one-shot has to be labelled the same
	// way or one card reads as estimated purely because of which code
	// path spent the credits. A positive credit figure the adapter did
	// not flag as an estimate is real; a token-priced one is not.
	var estimated float64
	if u.Credits <= 0 || u.Estimate {
		estimated = credits
	}
	if credits == 0 && estimated == 0 && u.InputTokens == 0 && u.OutputTokens == 0 {
		return
	}
	ctx := context.Background()
	_ = e.cfg.Store.AddSpend(ctx, id, credits, estimated, u.InputTokens, u.OutputTokens)
	_ = e.cfg.Store.RecordStageSpend(ctx, id, stage, string(agent.RoleScribe), u.Model,
		credits, estimated, u.InputTokens, u.CachedTokens, u.OutputTokens)
}
