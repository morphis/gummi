package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// After a restart there is no consult session, but the card's log keeps
// what was asked and answered: the TUI thread draws the exchange from it,
// in the slot the live one used, instead of forgetting it.
func TestTheTUIDrawsARecordedConsultAfterARestart(t *testing.T) {
	m := populatedShell(160, 50)
	id := m.rows[0].F.ID
	payload := func(author, content string) string {
		b, _ := json.Marshal(map[string]string{"author": author, "content": content})
		return string(b)
	}
	m.cardEvents[id] = []state.CardEvent{
		{Seq: 1, Feature: id, Stage: domain.StageTodo, Kind: state.EventConsult, At: fixedTime, Payload: payload("user", "is this already done?")},
		{Seq: 2, Feature: id, Stage: domain.StageTodo, Kind: state.EventConsult, At: fixedTime, Payload: payload("assistant", "Not yet — nothing touches it.")},
	}
	m.sel, m.cardOpen = 0, true
	view := ansi.Strip(m.threadView(157, 46))
	// The caption makes no read-only claim here: the log does not record
	// which backend answered, so it cannot know whether that one was
	// confined.
	if strings.Contains(view, "read-only") || strings.Contains(view, "not confined") {
		t.Errorf("a recorded consult claimed a confinement the log does not record:\n%s", view)
	}
	for _, want := range []string{"┄┄ asked ┄", "is this already done?", "Not yet — nothing touches it."} {
		if !strings.Contains(view, want) {
			t.Errorf("the thread lost the recorded consult (%q missing):\n%s", want, view)
		}
	}
}

// On the web the thread draws every consult turn from the log, where it
// was asked; the live block keeps only what the log does not hold yet, so
// a settled exchange is never drawn twice — once in place, once at the
// bottom.
func TestTheWebLiveConsultHoldsOnlyWhatIsInFlight(t *testing.T) {
	b, _, eng, f, _ := headlessBoard(t, agent.NewFake("Not yet."))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	c, err := eng.OpenConsult(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "is this already done?"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for c.Snapshot().Busy || len(c.Snapshot().Transcript) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the consult never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var live webapi.Live
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { live, _ = m.WebLive(string(f.ID)); return nil }); err != nil {
		t.Fatal(err)
	}
	if live.Consult != nil {
		t.Errorf("a settled consult is still in the live block: %+v", live.Consult)
	}
	evs, err := func() ([]state.CardEvent, error) {
		var s *state.Store
		_ = b.Do(ctx, func(m *Shell) tea.Cmd { s = m.store; return nil })
		return s.Events(ctx, f.ID)
	}()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind == state.EventConsult {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the log holds %d consult turns, want the question and its answer", n)
	}
}

// The web's live consult carries the engine's notice when the backend it
// runs on cannot confine it, so the page says so where the TUI does; a
// confined one carries none.
func TestTheWebLiveConsultCarriesTheConfinementNotice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enforce bool
		want    string
	}{
		{"unconfined", false, engine.ConsultNotice("fake")},
		{"confined", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := agent.NewFake("Not yet.")
			ag.Caps.ReadOnlyEnforce = tc.enforce
			b, _, eng, f, _ := headlessBoard(t, ag)
			waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
			ctx := context.Background()
			if _, err := eng.OpenConsult(ctx, f); err != nil {
				t.Fatal(err)
			}
			var live webapi.Live
			if err := b.Do(ctx, func(m *Shell) tea.Cmd {
				// a line on its way keeps the live block drawn
				m.consultSending[f.ID] = "is this already done?"
				live, _ = m.WebLive(string(f.ID))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if live.Consult == nil {
				t.Fatal("no live consult block for a line in flight")
			}
			if live.Consult.Notice != tc.want {
				t.Errorf("Notice = %q, want %q", live.Consult.Notice, tc.want)
			}
		})
	}
}
