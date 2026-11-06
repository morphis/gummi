package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/engine"
)

func TestCardMenuTogglesSessionWorkflows(t *testing.T) {
	b, eng, f := sessionSpecBoard(t, "open")
	ctx := context.Background()
	row := freeformRow(1, "dark mode", true)
	in := nextInput{stage: row.F.Stage, kind: row.F.Kind, agentWired: true, workflows: true}
	find := func(id string) string {
		for _, a := range cardActionsFor(in, row) {
			if a.id == id {
				return a.label
			}
		}
		return ""
	}
	if got := find(sessionWorkflowsID); !strings.Contains(got, "turn workflows off") {
		t.Errorf("label = %q", got)
	}
	if find(sessionWorkflowsResetID) != "" {
		t.Error("reset offered with no switch of its own")
	}
	in.workflowsSet = true
	if find(sessionWorkflowsResetID) == "" {
		t.Error("reset not offered")
	}
	run := func(f func(m *Shell) tea.Cmd) noticeMsg {
		var cmd tea.Cmd
		if err := b.Do(ctx, func(m *Shell) tea.Cmd { cmd = f(m); return nil }); err != nil {
			t.Fatal(err)
		}
		msg, ok := cmd().(noticeMsg)
		if !ok || msg.isErr {
			t.Fatalf("notice = %+v", msg)
		}
		return msg
	}
	run(func(m *Shell) tea.Cmd { return m.toggleSessionWorkflows(f) })
	if eng.WorkflowsOn(ctx, f.ID) {
		t.Fatal("toggle did not turn workflows off")
	}
	run(func(m *Shell) tea.Cmd { return m.toggleSessionWorkflows(f) })
	if !eng.WorkflowsOn(ctx, f.ID) || eng.SessionWorkflows(ctx, f.ID) == nil {
		t.Fatal("second toggle did not turn them on")
	}
	run(func(m *Shell) tea.Cmd { return m.resetSessionWorkflows(f) })
	if eng.SessionWorkflows(ctx, f.ID) != nil {
		t.Fatal("reset left a switch")
	}
	_ = engine.ErrWorkflowsBusy
}

func TestSpecViewProposesNoRunWhenWorkflowsOff(t *testing.T) {
	b, eng, f := sessionSpecBoard(t, "open")
	ctx := context.Background()
	off := false
	if err := eng.SetSessionWorkflows(ctx, f.ID, &off, ""); err != nil {
		t.Fatal(err)
	}
	var notice string
	var cmd tea.Cmd
	var keyOffered bool
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		m.spec = &specView{f: f, n: 1, workflowsOff: !m.workflowsOffered(f)}
		cmd = m.askForRun()
		notice = m.notice.text
		for _, bd := range m.spec.bindings() {
			keyOffered = keyOffered || bd.key == "w"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cmd != nil || !strings.Contains(notice, "workflows are off for this session") || keyOffered {
		t.Fatalf("cmd %v notice %q key offered %v", cmd != nil, notice, keyOffered)
	}
}

func TestComposerOffersNoWorkflowCommandWhenOff(t *testing.T) {
	b, eng, f := sessionSpecBoard(t, "open")
	ctx := context.Background()
	check := func(want bool) {
		t.Helper()
		var got bool
		if err := b.Do(ctx, func(m *Shell) tea.Cmd {
			r, _ := m.selected()
			got = m.isProjectCommand(r, "/workflow")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("/workflow is a command: %v, want %v", got, want)
		}
	}
	check(true)
	off := false
	if err := eng.SetSessionWorkflows(ctx, f.ID, &off, ""); err != nil {
		t.Fatal(err)
	}
	check(false)
}
