package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// Workflows are opt-in: a workspace setting, and a switch of a session's
// own that wins over it in either direction. A session with workflows off
// is neither offered the spec_* and workflow_* tools, their hints, gummi's
// workflows and workflow-recipes skills, /workflow nor triage's recipe
// read; every gate asks WorkflowsOn, so the faces and the engine cannot
// disagree.

// ErrWorkflowsBusy refuses turning workflows on mid-turn: the tools could
// not reach the backend until it restarts. Turning them off never waits.
var ErrWorkflowsBusy = errors.New("the session is mid-turn; turn workflows on when the turn ends")

// workflowsOffRefusal answers a spec or workflow tool call on a session
// with workflows off.
const workflowsOffRefusal = "workflows and specs are off for this session: a person turns them on from its card menu or the workspace settings. Do the work here, in the conversation."

// MayHaveWorkflowsSwitch is whether a card has a workflows switch: an open
// session, an agent included, that is no step's card.
func MayHaveWorkflowsSwitch(f domain.Feature) bool {
	return isSession(f) && f.Stage != domain.StageDone
}

// WorkflowsDefault is the workspace's setting as this process holds it.
func (e *Engine) WorkflowsDefault() bool { return e.workflowsOn.Load() }

// SetWorkflowsDefault changes the workspace's setting live. A session
// running now keeps the tool list it started with until its backend next
// starts; the backstop refuses its workflow and spec calls at once.
func (e *Engine) SetWorkflowsDefault(on bool) { e.workflowsOn.Store(on) }

// SessionWorkflows is the session's own switch: nil when it follows the
// workspace.
func (e *Engine) SessionWorkflows(ctx context.Context, id domain.FeatureID) *bool {
	if e.cfg.Store == nil {
		return nil
	}
	on, err := e.cfg.Store.SessionWorkflows(ctx, id)
	if err != nil {
		return nil
	}
	return on
}

// WorkflowsOn reports whether the session id is offered workflows and
// specs: its own switch when a person set one, else the workspace's
// setting. It reads the store every time, so a tool call is never answered
// from a value read when the backend started.
func (e *Engine) WorkflowsOn(ctx context.Context, id domain.FeatureID) bool {
	if on := e.SessionWorkflows(ctx, id); on != nil {
		return *on
	}
	return e.WorkflowsDefault()
}

// SetSessionWorkflows records a person's switch (nil clears it) and
// restarts the session's live backend so its tool list is the one it is
// now owed. by names the person for the thread ("" is the terminal's "you").
func (e *Engine) SetSessionWorkflows(ctx context.Context, id domain.FeatureID, on *bool, by string) error {
	if e.cfg.Store == nil {
		return errors.New("no store")
	}
	e.freeformMu.Lock()
	defer e.freeformMu.Unlock()
	f, err := e.feature(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case !isSession(f):
		return fmt.Errorf("%s is not a session: only a session has workflows", f.Name())
	case f.Stage == domain.StageDone:
		return fmt.Errorf("%s is closed", f.Name())
	}
	was := e.WorkflowsOn(ctx, id)
	now := e.WorkflowsDefault()
	if on != nil {
		now = *on
	}
	ff := e.Freeform(id)
	if ff != nil && ff.Busy() && now && !was {
		return ErrWorkflowsBusy
	}
	if err := e.cfg.Store.SetSessionWorkflows(ctx, id, on); err != nil {
		return err
	}
	who := "The person"
	if by != "" {
		who = by
	}
	var line string
	switch {
	case on == nil && now:
		line = who + " has this session follow the workspace setting again (workflows on): the spec and workflow tools are yours from the next start."
	case on == nil:
		line = who + " has this session follow the workspace setting again (workflows off): the spec and workflow tools are refused from now on and gone from its next start."
	case now:
		line = who + " turns workflows on for this session: the spec and workflow tools are yours from the next start."
	default:
		line = who + " turns workflows off for this session: the spec and workflow tools are refused from now on and gone from its next start."
	}
	var sess *Session
	if ff != nil {
		ff.mu.Lock()
		sess = ff.sess
		ff.mu.Unlock()
	}
	if sess == nil {
		e.cardNote(ctx, id, f.Stage, line)
		return nil
	}
	sess.appendSystem(line)
	if now == was {
		return nil
	}
	if ff.Busy() {
		// turning off mid-turn: the backstop refuses from the next call
		// and the tool list is corrected when the backend next starts
		return nil
	}
	// The conversation id the old backend kept is dropped so the next
	// spawn replays the transcript with the tool list it is owed.
	sess.setAgentSessionID("")
	if sess.Live() {
		ff.settle()
		sess.setState(StateDone)
		sess.stop()
		ff.dropLock()
	} else {
		e.persist(sess)
	}
	return nil
}

// workflowOrSpecTool is whether a tool name is one of a session's spec or
// workflow tools.
func workflowOrSpecTool(name string) bool {
	switch name {
	case specStartToolName, specListToolName, specViewToolName, specReplaceSectionToolName, annotateToolName:
		return true
	}
	for _, t := range workflowTools() {
		if t.Name == name {
			return true
		}
	}
	return false
}
