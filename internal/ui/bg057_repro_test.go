package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// TestBG057ReproResizeMovesScrolledUpWindow is BG-057's regression test.
// threadScroll is a raw row count into the composed thread body
// (thread.go's own doc comment on the field), and the body is
// width-dependent — every long message is hard-wrapped to the thread's
// inner width before being counted as rows. A width change rewraps the
// body, so the same threadScroll no longer names the same row: paging up
// once, mid-document, and then only resizing the terminal's width used to
// move the visible window with no key pressed.
//
// The history is built so the reader lands among 40 short, individually
// distinguishable filler lines well clear of both scroll clamps — a page
// up from the very bottom (threadScroll == 0) can't show the bug, since
// there is nothing above it to shift underneath the reader.
func TestBG057ReproResizeMovesScrolledUpWindow(t *testing.T) {
	m := populatedShell(50, 25)
	m.cardOpen = true
	id := m.rows[m.sel].F.ID

	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	enter, _ := json.Marshal(map[string]string{"role": "architect", "model": "m"})

	var evs []state.CardEvent
	evs = append(evs, state.CardEvent{
		Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: string(enter),
	})
	for i := range 40 {
		evs = append(evs, state.CardEvent{
			Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK, At: at,
			Payload: fmt.Sprintf(`{"label":"old-filler-%d"}`, i),
		})
	}
	longMsg, _ := json.Marshal(map[string]string{
		"author":  string(engine.AuthorAssistant),
		"content": strings.Repeat("this turn explains what changed, why it changed, and where the risk sat. ", 4),
	})
	evs = append(evs, state.CardEvent{
		Stage: domain.StageImplement, Kind: state.EventMessage, At: at, Payload: string(longMsg),
	})
	for i := 40; i < 50; i++ {
		evs = append(evs, state.CardEvent{
			Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK, At: at,
			Payload: fmt.Sprintf(`{"label":"old-filler-%d"}`, i),
		})
	}
	m.cardEvents[id] = evs

	// topFillerLine returns the first rendered row naming a filler event,
	// and the row's own index — the "top of the window" as far as this
	// test can observe it from outside.
	topFillerLine := func() (line string, ok bool) {
		w, h := m.threadSize()
		for _, l := range strings.Split(ansi.Strip(m.threadView(w, h)), "\n") {
			if strings.Contains(l, "old-filler-") {
				return strings.TrimSpace(l), true
			}
		}
		return "", false
	}

	m.scrollThread(true) // one PageUp
	if m.threadScroll == 0 {
		t.Fatal("precondition: the thread did not scroll")
	}
	scrollBefore := m.threadScroll
	before, ok := topFillerLine()
	if !ok {
		t.Fatal("precondition: no filler line visible after paging up")
	}

	model, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 16}) // width only
	m = model.(*Shell)

	after, ok := topFillerLine()
	if !ok {
		t.Fatal("no filler line visible after the resize")
	}
	if before != after {
		t.Errorf("top filler line was %q before a width change (50->220 cols, threadScroll %d), %q after (threadScroll %d) — a resize moved the scrolled-up window with no key pressed",
			before, scrollBefore, after, m.threadScroll)
	}
}
