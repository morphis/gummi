package ui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// A person's switch for one session's workflows and specs. Both faces call
// Engine.SetSessionWorkflows; neither decides anything. The web page gets
// the card menu's entries through the same actions.

const (
	// sessionWorkflowsID is the card menu's entry that turns workflows on
	// or off for the session; sessionWorkflowsResetID drops that switch.
	sessionWorkflowsID      = "sessionworkflows"
	sessionWorkflowsResetID = "sessionworkflowsreset"
)

func sessionWorkflowsLabel(on bool) string {
	if on {
		return "turn workflows off for this session"
	}
	return "turn workflows on for this session"
}

func sessionWorkflowsDetail(on bool) string {
	if on {
		return "stop offering this session specs and workflow runs; an open run carries on"
	}
	return "offer this session specs and workflow runs: spec_*, workflow_* and /workflow from its next start"
}

// sessionWorkflowsOffered is whether the card has the switch at all.
func sessionWorkflowsOffered(in nextInput, r featureRow) bool {
	return in.agentWired && engine.MayHaveWorkflowsSwitch(r.F) && !r.watchOnly()
}

func sessionWorkflowsNotice(name string, on *bool) string {
	switch {
	case on == nil:
		return name + ": workflows follow the workspace setting again"
	case *on:
		return name + ": workflows are on for this session, from its next start"
	}
	return name + ": workflows are off for this session"
}

func sessionWorkflowsError(err error) string {
	if errors.Is(err, engine.ErrWorkflowsBusy) {
		return err.Error()
	}
	return sanitize(err.Error())
}

func (m *Shell) setSessionWorkflows(f domain.Feature, flip bool) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: f.Name() + ": no agent engine to carry the switch", isErr: true}
		return nil
	}
	eng := m.engine
	return func() tea.Msg {
		ctx := context.Background()
		var on *bool
		if flip {
			v := !eng.WorkflowsOn(ctx, f.ID)
			on = &v
		}
		if err := eng.SetSessionWorkflows(ctx, f.ID, on, ""); err != nil {
			return noticeMsg{text: f.Name() + ": " + sessionWorkflowsError(err), isErr: true}
		}
		return noticeMsg{text: sessionWorkflowsNotice(f.Name(), on), reload: true}
	}
}

// toggleSessionWorkflows is the TUI's: flip the session's effective answer.
func (m *Shell) toggleSessionWorkflows(f domain.Feature) tea.Cmd {
	return m.setSessionWorkflows(f, true)
}

// resetSessionWorkflows drops the session's own switch.
func (m *Shell) resetSessionWorkflows(f domain.Feature) tea.Cmd {
	return m.setSessionWorkflows(f, false)
}

// SetSessionWorkflows is the web's: set the switch the page asked for (nil
// clears it).
func (d *WebDocs) SetSessionWorkflows(ctx context.Context, on *bool, by string) webapi.SessionWorkflowsResult {
	if d.eng == nil {
		return webapi.SessionWorkflowsResult{Error: "no agent engine on this board"}
	}
	res := func() webapi.SessionWorkflowsResult {
		r := webapi.SessionWorkflowsResult{On: d.eng.WorkflowsOn(ctx, d.f.ID)}
		r.Override = overrideWord(d.eng.SessionWorkflows(ctx, d.f.ID))
		return r
	}
	if err := d.eng.SetSessionWorkflows(ctx, d.f.ID, on, by); err != nil {
		r := res()
		r.Error = sessionWorkflowsError(err)
		return r
	}
	return res()
}

func overrideWord(on *bool) string {
	switch {
	case on == nil:
		return ""
	case *on:
		return "on"
	}
	return "off"
}

// webWorkflowsOf is a session's workflows answer for the web card: nil on a
// card with no such switch.
func (m *Shell) webWorkflowsOf(f domain.Feature) *webapi.SessionWorkflows {
	if m.engine == nil || !engine.MayHaveWorkflowsSwitch(f) {
		return nil
	}
	ctx := context.Background()
	return &webapi.SessionWorkflows{On: m.engine.WorkflowsOn(ctx, f.ID), Override: overrideWord(m.engine.SessionWorkflows(ctx, f.ID))}
}

// workflowsOffered is whether the session f is offered workflow runs: with
// no engine the keys stay, and the engine refuses.
func (m *Shell) workflowsOffered(f domain.Feature) bool {
	return m.engine == nil || m.engine.WorkflowsOn(context.Background(), f.ID)
}
