package ui

// The schedules view (DESIGN §19.9): what comes back on a clock without
// a person typing — a schedule that mints a freeform card on a cron
// cadence, and a heartbeat that sends a recurring turn into one session.
// The list carries the whole of the verbs: define (`n`, the dialog),
// edit (enter, the same dialog prefilled), enable and disable (enabling
// asks, because it is the switch that starts spending), run now (a
// forced fire, the one a disabled row may take), and delete.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/ui/theme"
)

// schedulesDialog is the view: the rows, a cursor, and nothing else.
type schedulesDialog struct {
	m      *Shell
	rows   []domain.Schedule
	cursor int
	err    string
}

func (m *Shell) openSchedules() tea.Cmd {
	d := &schedulesDialog{m: m}
	m.Overlay.Push(d)
	return m.loadSchedules()
}

// schedulesLoadedMsg carries one read of the rows to the open view.
type schedulesLoadedMsg struct {
	rows []domain.Schedule
	err  error
}

// loadSchedules reads every schedule off the render loop.
func (m *Shell) loadSchedules() tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if store == nil {
			return schedulesLoadedMsg{err: fmt.Errorf("this board has no store to keep schedules in")}
		}
		rows, err := store.ListSchedules(context.Background())
		return schedulesLoadedMsg{rows: rows, err: err}
	}
}

func (d *schedulesDialog) ID() string { return "schedules" }

// HandleKey answers the view's own verbs. esc closes; the confirm steps
// ride confirmDialog's onConfirm callbacks, which run their store and
// engine work as commands and report back as notices; n and enter open
// the schedule dialog — create, or edit the selected row.
func (d *schedulesDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		return true, nil
	case "j", "down":
		if len(d.rows) > 0 {
			d.cursor = clamp(d.cursor+1, 0, len(d.rows)-1)
		}
	case "k", "up":
		if len(d.rows) > 0 {
			d.cursor = clamp(d.cursor-1, 0, len(d.rows)-1)
		}
	case "n":
		return true, d.m.openScheduleForm(nil)
	case "enter":
		if d.cursor < len(d.rows) {
			return true, d.m.openScheduleForm(&d.rows[d.cursor])
		}
	case "e":
		if d.cursor < len(d.rows) {
			return true, d.m.confirmScheduleToggle(d.rows[d.cursor])
		}
	case "r":
		if d.cursor < len(d.rows) {
			d.m.confirmScheduleRun(d.rows[d.cursor])
			return true, nil
		}
	case "x":
		if d.cursor < len(d.rows) {
			d.m.confirmScheduleDelete(d.rows[d.cursor])
			return true, nil
		}
	}
	return false, nil
}

// confirmScheduleToggle asks before enabling: an enabled schedule is the
// switch that starts spending. Disabling is safe, and does not ask.
func (m *Shell) confirmScheduleToggle(sc domain.Schedule) tea.Cmd {
	if !sc.Enabled {
		next, err := schedule.NextRun(sc.Cron, sc.Timezone, m.now())
		if err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			return nil
		}
		m.Overlay.Push(&confirmDialog{
			id:           "confirm-schedule-enable",
			question:     fmt.Sprintf("enable %s?", sc.ID),
			detail:       fmt.Sprintf("%s — first fire %s", sc.Cron, scheduleNextWords(sc.NextRun, next, m.now())),
			confirmLabel: "Enable",
			onConfirm:    func() tea.Cmd { return m.scheduleEnable(sc.ID, next) },
		})
		return nil
	}
	return m.scheduleDisable(sc.ID)
}

// confirmScheduleRun asks before a forced fire: it starts a session now,
// envelope and all, and it is the one fire a disabled row may take.
func (m *Shell) confirmScheduleRun(sc domain.Schedule) {
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-schedule-run",
		question:     fmt.Sprintf("run %s now?", sc.ID),
		detail:       fmt.Sprintf("fires once now — %s", sc.Cron),
		confirmLabel: "Run",
		onConfirm:    func() tea.Cmd { return m.scheduleRunNow(sc.ID) },
	})
}

// confirmScheduleDelete asks before deleting: the row goes, and the
// cards it minted stay on the board like any freeform card.
func (m *Shell) confirmScheduleDelete(sc domain.Schedule) {
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-schedule-delete",
		question:     fmt.Sprintf("delete %s?", sc.ID),
		detail:       sc.Name + " — the cards it minted stay on the board",
		confirmLabel: "Delete",
		onConfirm:    func() tea.Cmd { return m.scheduleDelete(sc.ID) },
	})
}

// scheduleEnable turns a schedule on with the first fire the cadence
// computes from now.
func (m *Shell) scheduleEnable(id domain.ScheduleID, next time.Time) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if err := store.SetScheduleEnabled(context.Background(), id, true, next); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("schedule %s enabled — next fire %s", id, next.Format(time.DateTime))}
	}
}

// scheduleDisable turns a schedule off.
func (m *Shell) scheduleDisable(id domain.ScheduleID) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if err := store.SetScheduleEnabled(context.Background(), id, false, time.Time{}); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("schedule %s disabled", id)}
	}
}

// scheduleRunNow forces one fire through the engine. The engine's
// outcome is the notice; the alert (bell, web push) rides the same path
// the poll's fires take.
func (m *Shell) scheduleRunNow(id domain.ScheduleID) tea.Cmd {
	eng := m.engine
	return func() tea.Msg {
		if eng == nil {
			return noticeMsg{text: m.noAgent(" — a fire needs one"), isErr: true}
		}
		fire, err := eng.RunSchedule(context.Background(), id, time.Now())
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return scheduleFiredMsg{fire: fire}
	}
}

// scheduleDelete removes the row.
func (m *Shell) scheduleDelete(id domain.ScheduleID) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if err := store.DeleteSchedule(context.Background(), id); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("schedule %s deleted", id)}
	}
}

// scheduleFiredMsg carries a forced fire's result to the update loop —
// the same notice and alert the poll's tick produces.
type scheduleFiredMsg struct{ fire engine.ScheduleFire }

// View renders the list: name, kind, target, cadence, on/off, next run,
// and the last outcome.
func (d *schedulesDialog) View(s *theme.Styles, w, _ int) string {
	var b strings.Builder
	if d.err != "" {
		return closeOutFrame(s, "schedules", s.Base.Render(sanitize(d.err)))
	}
	if len(d.rows) == 0 {
		return closeOutFrame(s, "schedules", s.Base.Render(
			"no schedules. `n` defines one, or `gummi schedule add` —\n"+
				"cron mints a freeform card on a cadence, or sends a recurring\n"+
				"turn into a session. Off until enabled."))
	}
	b.WriteString("\n")
	for i, r := range d.rows {
		sel := i == d.cursor
		mark := "  "
		style := s.Base
		if sel {
			mark = s.BandMarker(true)
			style = s.Subtle
		}
		name := r.Name
		if name == "" {
			name = string(r.ID)
		}
		state := s.Faint.Render("off")
		if r.Enabled {
			state = s.CardTitle.Render("on")
		}
		line := mark + s.CardID.Render(string(r.ID)) + "  " + style.Render(name) + "  " +
			s.Faint.Render(scheduleKindWord(r.Kind)) + "  " + s.Base.Render(r.Cron) + "  " + state
		if r.Enabled && !r.NextRun.IsZero() {
			line += "  " + s.Faint.Render(scheduleNextWords(r.NextRun, time.Time{}, d.m.now()))
		}
		if tail := scheduleLastWords(r); tail != "" {
			line += "  " + s.Faint.Render(tail)
		}
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	b.WriteString("\n   " + s.Faint.Render("n new · enter edit · j/k select · e on/off · r run now · x delete · esc close"))
	return closeOutFrame(s, "schedules · "+strconv.Itoa(len(d.rows)), b.String())
}

// scheduleKindWord is a row's kind as a person reads it.
func scheduleKindWord(k domain.ScheduleKind) string {
	if k == domain.ScheduleHeartbeat {
		return "heartbeat"
	}
	return "mint"
}

// scheduleNextWords is a row's next fire as a relative phrase: the row
// renders what the cadence means ("in 45m"), not an absolute clock that
// would need a timezone of its own to be believed.
func scheduleNextWords(current, planned, now time.Time) string {
	next := planned
	if next.IsZero() {
		next = current
	}
	if next.IsZero() {
		return ""
	}
	d := next.Sub(now)
	switch {
	case d <= 0:
		return "due now"
	case d < time.Minute:
		return "in under a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()+0.5))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24+0.5))
	}
}

// scheduleLastWords is the last outcome as a row's tail: the status, and
// the detail only when there is one worth reading. Truncation is
// rune-aware (ansi.Truncate), not a byte slice — a detail can name a
// card or a repo path with non-ASCII text.
func scheduleLastWords(r domain.Schedule) string {
	if r.LastStatus == "" {
		return ""
	}
	out := string(r.LastStatus)
	if r.LastDetail != "" {
		detail := ansi.Truncate(sanitize(r.LastDetail), 58, "…")
		out += " — " + detail
	}
	return out
}
