package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestBG075ThreadScrollMarkerNamesItsKey is BG-075's regression test.
// The thread's clipped-window markers read "↑ N more" / "↓ N more", the
// same shape the backlog list uses — but in a list the arrow IS the key,
// and in the thread it is not: ↑ and ↓ belong to the composer's line,
// where they walk a pinned decision's options and open the action
// inventory. A reader who followed the marker moved the highlight
// instead of the window, and the bar's own "pgup/pgdn scroll" row is one
// of the first things width pressure sheds.
func TestBG075ThreadScrollMarkerNamesItsKey(t *testing.T) {
	m := populatedShell(90, 20)
	m.cardOpen = true
	id := m.rows[m.sel].F.ID

	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "implementer", "model": "m"})
	evs := []state.CardEvent{{
		Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: string(enter),
	}}
	for i := range 60 {
		evs = append(evs, state.CardEvent{
			Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK, At: at,
			Payload: fmt.Sprintf(`{"label":"filler-%d"}`, i),
		})
	}
	m.cardEvents[id] = evs

	w, h := m.threadSize()
	m.scrollThread(true) // one page up, so both ends are clipped
	body := ansi.Strip(m.threadView(w, h))

	for _, want := range []struct{ marker, key string }{{"↑", "pgup"}, {"↓", "pgdn"}} {
		var line string
		for _, l := range strings.Split(body, "\n") {
			if strings.Contains(l, want.marker+" ") && strings.Contains(l, "more") {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("no %q marker in the clipped thread:\n%s", want.marker, body)
		}
		if !strings.Contains(line, want.key) {
			t.Errorf("the %q marker does not name the key that moves the window: %q", want.marker, strings.TrimSpace(line))
		}
	}
}
