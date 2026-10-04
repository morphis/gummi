package ui

// Schedules on the board (DESIGN §19.9). A schedule or heartbeat is a
// store row fired on a cron cadence; the board's part is the clock — its
// update loop polls the engine, the engine decides and acts, and the
// board turns the recorded outcomes into notices and alerts. The same
// division of labour the stack poll has (stackboard.go): the engine
// decides, the board notices.
//
// Nothing here fires anything by itself and nothing here queues a fire:
// skipped-busy is recorded by the engine and shown in the schedules view,
// but never notified — a five-minute heartbeat against a working session
// would otherwise ring every five minutes.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// schedulePollInterval is how often the board asks the engine whether a
// cadence came due. Cadences are minute-grained, so 15s is well inside
// one; the cost of a tick with nothing due is one list read.
const schedulePollInterval = 15 * time.Second

// schedulePollMsg is the periodic wake for fires no engine event
// announces: a heartbeat's turn announces itself, but a mint that failed
// before any card existed does not, and neither does a schedule that was
// merely due and paused.
type schedulePollMsg struct{}

// schedulePoll arms the next poll.
func schedulePoll() tea.Cmd {
	return subscription(tea.Tick(schedulePollInterval, func(time.Time) tea.Msg {
		return schedulePollMsg{}
	}))
}

// scheduleTickMsg carries one poll's fires back to the update loop.
type scheduleTickMsg struct {
	fires []engine.ScheduleFire
	err   error
}

// scheduleTick asks the engine to advance every due schedule at now.
func (m *Shell) scheduleTick() tea.Cmd {
	eng := m.engine
	if eng == nil {
		return schedulePoll()
	}
	return func() tea.Msg {
		fires, err := eng.ScheduleTick(context.Background(), time.Now())
		return scheduleTickMsg{fires: fires, err: err}
	}
}

// onScheduleTick reports what fired. Every outcome except skipped-busy
// becomes a board notice and an alert (bell plus every attention
// notifier, web push included): a person wants to know a session came
// back on its own, and — more — that one failed or paused. The poll
// rearms here rather than at the poll message, so a fire's own cost
// does not change the cadence of the checks — and it rearms on an
// error too: a transient store failure must not end the poll chain
// and silently stop every schedule while the board keeps running.
func (m *Shell) onScheduleTick(msg scheduleTickMsg) tea.Cmd {
	if msg.err != nil {
		return tea.Batch(
			func() tea.Msg {
				return noticeMsg{text: sanitize("schedules: " + msg.err.Error()), isErr: true}
			},
			schedulePoll())
	}
	var lines []string
	failed := false
	for _, fire := range msg.fires {
		if fire.Outcome.Status == domain.ScheduleSkippedBusy {
			continue
		}
		lines = append(lines, scheduleFireNotice(fire))
		if fire.Outcome.Status == domain.ScheduleFailed {
			failed = true
		}
		m.alert(domain.FeatureID("schedule:"+string(fire.ID)), scheduleFireNotice(fire))
	}
	if len(lines) > 0 {
		text := strings.Join(lines, "\n")
		// The notice and the rearm both go out: the clock must keep
		// ticking after a fire, whatever it said.
		return tea.Batch(
			func() tea.Msg { return noticeMsg{text: sanitize(text), isErr: failed, reload: true} },
			schedulePoll())
	}
	return schedulePoll()
}

// scheduleFireNotice is the one line a fire says on the board.
func scheduleFireNotice(fire engine.ScheduleFire) string {
	name := fire.Name
	if name == "" {
		name = string(fire.ID)
	}
	o := fire.Outcome
	switch o.Status {
	case domain.ScheduleOK:
		if fire.Kind == domain.ScheduleMint && o.Card != "" {
			return fmt.Sprintf("schedule %s: minted %s", name, o.Card)
		}
		if o.Card != "" {
			return fmt.Sprintf("schedule %s: sent its turn to %s", name, o.Card)
		}
		return fmt.Sprintf("schedule %s: fired", name)
	case domain.ScheduleSkippedBusy:
		return fmt.Sprintf("schedule %s: skipped — %s", name, o.Detail)
	case domain.SchedulePausedExhausted:
		return fmt.Sprintf("schedule %s paused: %s", name, o.Detail)
	case domain.ScheduleDisabledTargetClosed:
		return fmt.Sprintf("schedule %s turned off: %s", name, o.Detail)
	default:
		return fmt.Sprintf("schedule %s failed: %s", name, o.Detail)
	}
}

// updateSchedule answers the schedule messages before the main switch
// sees them, the way updateStack does for stacks.
func (m *Shell) updateSchedule(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case schedulePollMsg:
		return m.scheduleTick(), true
	case scheduleTickMsg:
		return m.onScheduleTick(msg), true
	case schedulesLoadedMsg:
		if d, ok := m.Overlay.Top().(*schedulesDialog); ok {
			d.rows = msg.rows
			d.err = ""
			if msg.err != nil {
				d.err = msg.err.Error()
			}
			d.cursor = clamp(d.cursor, 0, max(len(d.rows)-1, 0))
		}
		return nil, true
	case scheduleFiredMsg:
		// A forced fire reports the way the poll's fires do: notice and
		// alert for anything that is not a busy skip.
		text := scheduleFireNotice(msg.fire)
		if msg.fire.Outcome.Status != domain.ScheduleSkippedBusy {
			m.alert(domain.FeatureID("schedule:"+string(msg.fire.ID)), text)
		}
		isErr := msg.fire.Outcome.Status == domain.ScheduleFailed
		return func() tea.Msg { return noticeMsg{text: sanitize(text), isErr: isErr, reload: true} }, true
	case scheduleModelsMsg:
		// The schedule dialog's model probe landed. Only the top dialog
		// takes it, and only for the backend it named — the row may have
		// moved on while the probe ran.
		if d, ok := m.Overlay.Top().(*scheduleForm); ok {
			d.setModels(msg.backend, msg.models)
		}
		return nil, true
	}
	return nil, false
}
