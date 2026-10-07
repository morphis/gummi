package ui

// The schedule dialog: one form that creates a schedule or heartbeat and
// edits one (DESIGN §19.9). Its rows are the definition the store keeps —
// name, kind, cadence, prompt, and, for a mint, the repository and the
// agent pair it runs on — so a person no longer has to know the exact
// backend and model strings or wait for the first fire to learn a cadence
// was wrong: the pair is picked from the session picker's catalog, and
// the cadence previews as it is typed, straight through the schedule
// package's own Compile/Parse/Next (no second cron reader lives here).
//
// The form never stores anything itself. Save builds the same
// request-shaped definition the web route builds and hands it to the
// Shell's shared front door, so a pair no session could run, a repository
// the board does not serve, a cadence the package refuses, and the
// store's structural rules (a mint always carries a brake; a heartbeat
// names a freeform card) are caught with the dialog still open — not as
// a notice after it has closed. The store's own bookkeeping (an edit
// forces the row off) stays behind it.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

// The dialog's focus stops. Not every stop is live at once: a heartbeat
// has a target and no repository, pair or brake of its own, and a mint's
// model row only exists when a backend is picked. stops() is the live
// list; focus always names one of its entries.
const (
	schedStopName = iota
	schedStopKind
	schedStopTarget
	schedStopRepo
	schedStopEvery
	schedStopCron
	schedStopZone
	schedStopPrompt
	schedStopBackend
	schedStopModel
	schedStopEnvelope
	schedStopButtons
)

// The kind row's cycle, labels beside it.
var (
	scheduleKinds      = []domain.ScheduleKind{domain.ScheduleMint, domain.ScheduleHeartbeat}
	scheduleKindLabels = []string{"mint a card", "heartbeat a session"}
)

// scheduleZones is the timezone row's shortlist: the zones a schedule is
// plausibly set in, with "local" — the host's zone, stored as no timezone
// at all — first. Any other IANA name types in freely; the seam validates
// it, so the shortlist is a courtesy, not a gate.
var scheduleZones = []string{
	"local", "UTC",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Sao_Paulo", "Europe/London", "Europe/Paris", "Europe/Berlin",
	"Europe/Helsinki", "Europe/Moscow", "Asia/Dubai", "Asia/Kolkata",
	"Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "Australia/Sydney",
	"Pacific/Auckland",
}

// scheduleZoneStored maps what the row holds to what the store keeps:
// "local" and empty both mean the host's zone, which the store spells as
// no timezone. The literal "local" is never validated as a zone.
func scheduleZoneStored(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "local" {
		return ""
	}
	return v
}

// scheduleBackendRow is one agent the backend row offers, in cycle order:
// the profile default first (no name — the profile's implementer runs),
// then every backend a session could be pointed at, with whether this
// host can start it and the ids the workspace's profiles run there.
type scheduleBackendRow struct {
	name      string
	installed bool
	suggest   []string
}

// scheduleForm is the dialog.
type scheduleForm struct {
	m    *Shell
	edit *domain.Schedule // nil creates

	name, every, cron, zone, prompt, target textinput.Model
	model, env                              textinput.Model
	repo                                    repoPicker

	kind int // an index into scheduleKinds

	backends []scheduleBackendRow
	backend  int
	// models are the picked backend's rows: the catalog's profile ids
	// seeded when the backend cycles or the dialog prefills, replaced by
	// the probe's merged rows when it lands (setModels). The merged rows
	// are the session picker's own offer; the seeded ids are what the
	// row shows while the probe runs.
	models   []string
	modelIdx int // -1 while the row's text is not one of models

	zoneIdx int // -1 while the row's text is not one of scheduleZones

	preview schedule.Result
	envHint string // the budget row's guidance, recomputed with the preview
	focus   int
	errText string
	buttons *buttonRow
	// onSave is the dialog's way out: the opener decides create and edit.
	onSave func(*domain.Schedule) tea.Cmd
}

// newScheduleForm builds the dialog, prefilled from edit when it edits.
func newScheduleForm(m *Shell, edit *domain.Schedule) *scheduleForm {
	in := func(ph string, limit, width int) textinput.Model {
		t := textinput.New()
		t.Placeholder = ph
		t.CharLimit = limit
		t.SetWidth(width)
		return t
	}
	d := &scheduleForm{
		m:       m,
		edit:    edit,
		name:    in("nightly triage", 100, 32),
		every:   in("1h, 15m, @daily", 24, 14),
		cron:    in("0 5 * * *", 64, 16),
		zone:    in("local", 48, 18),
		prompt:  in("what the session is asked", 2000, 40),
		target:  in("FF-001", 16, 10),
		model:   in("the agent's default", 120, 22),
		env:     in("100", 9, 7),
		zoneIdx: -1,
		focus:   schedStopName,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Save"}),
	}
	d.name.Focus()
	d.env.SetValue("100")
	// the backend cycle: the profile default first, then every backend a
	// session could be pointed at, with the static frame of the session
	// picker's catalog — the install flags and the profile ids
	var cat webapi.SessionModels
	if m.engine != nil {
		cat = m.webSessionModels()
	}
	d.backends = append(d.backends, scheduleBackendRow{name: "", installed: true})
	for _, name := range engine.SessionBackends {
		installed := (m.engine != nil && m.engine.HasAgent(name)) || agentInstalled(name)
		row := scheduleBackendRow{name: name, installed: installed}
		for _, a := range cat.Agents {
			if a.Name == name {
				row.suggest = a.Models
			}
		}
		d.backends = append(d.backends, row)
	}
	d.repo = newRepoPicker(m.repoNames, m.repoHasDefault())
	if edit != nil {
		d.prefill(edit)
	}
	d.refresh()
	return d
}

// prefill seeds the rows from the stored definition. The cron field
// carries the stored expression — the canonical form the row keeps — and
// the preset field stays empty, because the stored form is already what
// the preset would have compiled to.
func (d *scheduleForm) prefill(sc *domain.Schedule) {
	d.name.SetValue(sc.Name)
	for i, k := range scheduleKinds {
		if k == sc.Kind {
			d.kind = i
		}
	}
	d.target.SetValue(string(sc.Target))
	if sc.Repo != "" {
		for i, name := range d.repo.options() {
			if name == sc.Repo {
				d.repo.idx = i
			}
		}
	}
	d.cron.SetValue(sc.Cron)
	if sc.Timezone == "" {
		d.zone.SetValue("local")
	} else {
		d.zone.SetValue(sc.Timezone)
	}
	d.prompt.SetValue(sc.Prompt)
	for i, b := range d.backends {
		if b.name == sc.Backend {
			d.backend = i
			d.models = append([]string{}, b.suggest...)
		}
	}
	d.env.SetValue(strconv.Itoa(sc.Envelope))
}

// kindIsHeartbeat reports whether the kind row currently names one.
func (d *scheduleForm) kindIsHeartbeat() bool {
	return scheduleKinds[d.kind] == domain.ScheduleHeartbeat
}

// backendName is the picked backend, empty for the profile default.
func (d *scheduleForm) backendName() string {
	return d.backends[d.backend].name
}

// stops is the live focus list: the kind decides which rows exist. An
// edit keeps the kind — the row renders, frozen — so it is not a stop
// there, the way the web form shows the kind fixed on edit.
func (d *scheduleForm) stops() []int {
	out := []int{schedStopName}
	if d.edit == nil {
		out = append(out, schedStopKind)
	}
	out = append(out, schedStopEvery, schedStopCron, schedStopZone, schedStopPrompt)
	if d.kindIsHeartbeat() {
		out = append(out, schedStopTarget)
	} else {
		out = append(out, schedStopRepo, schedStopBackend)
		if d.backendName() != "" {
			out = append(out, schedStopModel)
		}
		out = append(out, schedStopEnvelope)
	}
	return append(out, schedStopButtons)
}

// advanceFocus moves focus by dir (±1) over the live stops, wrapping.
func (d *scheduleForm) advanceFocus(dir int) {
	stops := d.stops()
	at := 0
	for i, s := range stops {
		if s == d.focus {
			at = i
			break
		}
	}
	d.setFocus(stops[(at+dir+len(stops))%len(stops)])
}

// setFocus moves the text cursors: one input focused at a time.
func (d *scheduleForm) setFocus(f int) {
	d.focus = f
	for _, in := range []*textinput.Model{&d.name, &d.every, &d.cron, &d.zone, &d.prompt, &d.target, &d.model, &d.env} {
		in.Blur()
	}
	switch f {
	case schedStopName:
		d.name.Focus()
	case schedStopEvery:
		d.every.Focus()
	case schedStopCron:
		d.cron.Focus()
	case schedStopZone:
		d.zone.Focus()
	case schedStopPrompt:
		d.prompt.Focus()
	case schedStopTarget:
		d.target.Focus()
	case schedStopModel:
		d.model.Focus()
	case schedStopEnvelope:
		d.env.Focus()
	}
}

// nearestStop lands focus on the live stop closest to want — the row the
// kind switch just removed is not one to sit on.
func (d *scheduleForm) nearestStop(want int) int {
	stops := d.stops()
	best, bestd := stops[0], 1<<30
	for _, s := range stops {
		v := s - want
		if v < 0 {
			v = -v
		}
		if v < bestd {
			best, bestd = s, v
		}
	}
	return best
}

// ID implements overlay.Dialog.
func (d *scheduleForm) ID() string { return "schedule-form" }

// HandleKey implements overlay.Dialog. enter saves from any row; tab
// walks; the cycle rows answer left/right; the suggestion rows (model,
// timezone) answer up/down by filling the input with the next row, and
// typing anything leaves the cycle.
func (d *scheduleForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab":
		d.advanceFocus(1)
		return false, nil
	case "shift+tab":
		d.advanceFocus(-1)
		return false, nil
	}
	if d.focus == schedStopButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
		case "right", "l":
			d.buttons.Move(1)
		case "enter":
			if d.buttons.Cursor() == 0 {
				return true, nil
			}
			return d.submit()
		}
		return false, nil
	}
	if key.String() == "enter" {
		return d.submit()
	}
	switch d.focus {
	case schedStopKind:
		// an edit keeps the kind, so the row does not cycle there
		if delta, ok := selectCycleDelta(key.String()); ok && d.edit == nil {
			d.kind = (d.kind + delta + len(scheduleKinds)) % len(scheduleKinds)
			d.setFocus(d.nearestStop(d.focus))
		}
	case schedStopRepo:
		if delta, ok := selectCycleDelta(key.String()); ok {
			d.repo.cycle(delta)
		}
	case schedStopBackend:
		if delta, ok := selectCycleDelta(key.String()); ok {
			n := len(d.backends)
			d.backend = (d.backend + delta + n) % n
			// the picked backend's own ids show at once, so the row is
			// not empty while the probe runs; its merged answer replaces
			// them when it lands
			d.models = append([]string{}, d.backends[d.backend].suggest...)
			d.modelIdx = -1
			return false, d.probeModels()
		}
	case schedStopModel:
		switch key.String() {
		case "up":
			d.cycleModel(-1)
		case "down":
			d.cycleModel(1)
		default:
			d.model, _ = d.model.Update(key)
			d.modelIdx = -1
			d.refresh()
		}
	case schedStopZone:
		switch key.String() {
		case "up":
			d.cycleZone(-1)
		case "down":
			d.cycleZone(1)
		default:
			d.zone, _ = d.zone.Update(key)
			d.zoneIdx = -1
			d.refresh()
		}
	case schedStopName:
		d.name, _ = d.name.Update(key)
		d.errText = ""
	case schedStopEvery:
		d.every, _ = d.every.Update(key)
		d.refresh()
	case schedStopCron:
		d.cron, _ = d.cron.Update(key)
		d.refresh()
	case schedStopPrompt:
		d.prompt, _ = d.prompt.Update(key)
	case schedStopTarget:
		d.target, _ = d.target.Update(key)
	case schedStopEnvelope:
		d.env, _ = d.env.Update(key)
	}
	return false, nil
}

// HandlePaste implements overlay.Paster: pasted text lands in the
// focused input.
func (d *scheduleForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	rows := map[int]*textinput.Model{
		schedStopName: &d.name, schedStopEvery: &d.every, schedStopCron: &d.cron,
		schedStopZone: &d.zone, schedStopPrompt: &d.prompt, schedStopTarget: &d.target,
		schedStopModel: &d.model, schedStopEnvelope: &d.env,
	}
	if in, ok := rows[d.focus]; ok {
		*in, _ = in.Update(msg)
		d.refresh()
	}
	return nil
}

// cycleModel fills the model row with the next suggestion, wrapping.
func (d *scheduleForm) cycleModel(delta int) {
	if len(d.models) == 0 {
		return
	}
	d.modelIdx += delta
	if d.modelIdx < 0 {
		d.modelIdx = len(d.models) - 1
	}
	if d.modelIdx >= len(d.models) {
		d.modelIdx = 0
	}
	d.model.SetValue(d.models[d.modelIdx])
	d.refresh()
}

// cycleZone fills the zone row with the next shortlist entry, wrapping;
// one step below the first lands back on free text.
func (d *scheduleForm) cycleZone(delta int) {
	d.zoneIdx += delta
	if d.zoneIdx < -1 {
		d.zoneIdx = len(scheduleZones) - 1
	}
	if d.zoneIdx >= len(scheduleZones) {
		d.zoneIdx = -1
	}
	if d.zoneIdx >= 0 {
		d.zone.SetValue(scheduleZones[d.zoneIdx])
	}
	d.refresh()
}

// probeModels asks the picked backend for its merged model rows — the
// session picker's own pattern, off the loop, arriving as
// scheduleModelsMsg. webSessionModels' static frame alone carries no
// live catalog, so the probe is what makes the rows the picker's.
func (d *scheduleForm) probeModels() tea.Cmd {
	backend := d.backendName()
	d.refresh()
	if backend == "" || d.m.engine == nil {
		return nil
	}
	eng := d.m.engine
	return func() tea.Msg {
		return scheduleModelsMsg{backend: backend, models: eng.SessionModelChoices(context.Background(), backend)}
	}
}

// setModels lands the probe's answer, only for the backend it named —
// the row may have moved on while the probe ran.
func (d *scheduleForm) setModels(backend string, models []string) {
	if d.backendName() != backend {
		return
	}
	d.models = models
	d.modelIdx = -1
}

// scheduleModelsMsg is the probe's outcome: the merged model list for
// one backend, for the open schedule dialog.
type scheduleModelsMsg struct {
	backend string
	models  []string
}

// refresh recomputes the cadence preview and, with it, the budget row's
// guidance — the pair's priced rate or, when the workspace cannot price
// it, what the brake is for. Both are pure and cheap over the state they
// read — the same composition the write boundary applies — so they run
// per keystroke rather than per submit.
func (d *scheduleForm) refresh() {
	d.preview = schedule.Preview(d.every.Value(), d.cron.Value(), scheduleZoneStored(d.zone.Value()), d.m.now())
	d.envHint = scheduleEnvelopeHint(d.m.engine, d.backendName(), strings.TrimSpace(d.model.Value()))
}

// submit validates and saves through the Shell's shared front door — the
// same checks the web route runs — and hands the definition to the
// opener's save. The preview is the first gate: a cadence the package
// refuses is refused here, in the form, not as a notice after the fact.
func (d *scheduleForm) submit() (bool, tea.Cmd) {
	d.refresh()
	if d.preview.Err != nil {
		d.errText = sanitize(d.preview.Err.Error())
		return false, nil
	}
	req := webapi.ScheduleRequest{
		Name:     strings.TrimSpace(d.name.Value()),
		Kind:     string(scheduleKinds[d.kind]),
		Target:   strings.ToUpper(strings.TrimSpace(d.target.Value())),
		Repo:     d.repo.name(),
		Cron:     strings.TrimSpace(d.cron.Value()),
		Timezone: scheduleZoneStored(d.zone.Value()),
		Prompt:   strings.TrimSpace(d.prompt.Value()),
		Backend:  d.backendName(),
		Model:    strings.TrimSpace(d.model.Value()),
	}
	if req.Cron == "" {
		req.Every = strings.TrimSpace(d.every.Value())
	}
	if !d.kindIsHeartbeat() {
		if v, err := strconv.Atoi(strings.TrimSpace(d.env.Value())); err == nil {
			req.Envelope = &v
		}
	}
	sc, err := d.m.scheduleFromForm(req)
	if err != nil {
		d.errText = sanitize(err.Error())
		return false, nil
	}
	if d.edit != nil {
		// the identity is not what an edit changes, and a heartbeat keeps
		// its target unless the row now names another freeform card
		sc.ID = d.edit.ID
		sc.Kind = d.edit.Kind
		sc.CreatedAt = d.edit.CreatedAt
		if sc.Kind == domain.ScheduleHeartbeat && sc.Target == "" {
			sc.Target = d.edit.Target
		}
	} else {
		sc.CreatedAt = d.m.now()
	}
	return true, d.onSave(sc)
}

// openScheduleForm pushes the dialog — nil creates, a row edits — and
// probes the edited row's backend, so the model rows are the merged
// catalog by the time the row is reached.
func (m *Shell) openScheduleForm(edit *domain.Schedule) tea.Cmd {
	if m.store == nil {
		m.notice = noticeMsg{text: "this board has no store to keep schedules in", isErr: true}
		return nil
	}
	d := newScheduleForm(m, edit)
	if edit == nil {
		d.onSave = m.scheduleSaveCreate
	} else {
		d.onSave = m.scheduleSaveEdit
	}
	m.Overlay.Push(d)
	return d.probeModels()
}

// openHeartbeatForm pushes the dialog already a heartbeat aimed at f, so
// the card's own menu is a way into the Schedules view's form rather than
// a second one. Saving turns it on: the person asked for this card to be
// come back to, and has just read the cadence and the prompt.
func (m *Shell) openHeartbeatForm(f domain.Feature) tea.Cmd {
	if m.store == nil {
		m.notice = noticeMsg{text: "this board has no store to keep schedules in", isErr: true}
		return nil
	}
	d := newScheduleForm(m, nil)
	for i, k := range scheduleKinds {
		if k == domain.ScheduleHeartbeat {
			d.kind = i
		}
	}
	d.target.SetValue(string(f.ID))
	d.name.SetValue(strings.ToLower(string(f.ID)) + " heartbeat")
	d.every.SetValue("1h")
	d.onSave = m.scheduleSaveAndEnable
	d.refresh()
	m.Overlay.Push(d)
	return nil
}

// scheduleSaveAndEnable stores a new definition and turns it on with the
// first fire its cadence computes from now.
func (m *Shell) scheduleSaveAndEnable(sc *domain.Schedule) tea.Cmd {
	store, now := m.store, m.now()
	return func() tea.Msg {
		ctx := context.Background()
		if err := store.CreateSchedule(ctx, sc); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		next, err := schedule.NextRun(sc.Cron, sc.Timezone, now)
		if err == nil {
			err = store.SetScheduleEnabled(ctx, sc.ID, true, next)
		}
		if err != nil {
			return noticeMsg{text: sanitize(fmt.Sprintf("schedule %s added but not enabled: %v", sc.ID, err)), isErr: true, reload: true}
		}
		return noticeMsg{text: sanitize(fmt.Sprintf("schedule %s enabled — first fire %s", sc.ID, next.Format(time.DateTime))), reload: true}
	}
}

// scheduleSaveCreate stores a new definition. The store inserts it
// disabled — off by default is the rule, and the notice says what comes
// next.
func (m *Shell) scheduleSaveCreate(sc *domain.Schedule) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if err := store.CreateSchedule(context.Background(), sc); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: sanitize(fmt.Sprintf("schedule %s added — off until you enable it", sc.ID)), reload: true}
	}
}

// scheduleSaveEdit writes an edited definition. The store forces the row
// off — a cadence the person has not re-approved is a cadence that must
// not fire — so the notice says to enable it again.
func (m *Shell) scheduleSaveEdit(sc *domain.Schedule) tea.Cmd {
	store := m.store
	return func() tea.Msg {
		if err := store.UpdateScheduleDefinition(context.Background(), sc); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: sanitize(fmt.Sprintf("schedule %s edited — off until you enable it again", sc.ID)), reload: true}
	}
}

// View renders the rows, the live preview, and the error. Every row is
// folded to the dialog's content width first — a long prompt or a wide
// preview must not set the frame's width.
func (d *scheduleForm) View(s *theme.Styles, w, h int) string {
	width, _ := dialogDescSize(w, h, 12)
	var b strings.Builder
	title := "new schedule"
	if d.edit != nil {
		title = "edit " + string(d.edit.ID)
	}
	b.WriteString(s.DialogTitle.Render(title) + "\n\n")

	cycle := func(focus int, label string, opts []string, idx int) {
		b.WriteString(strings.Join(choiceRowLines(s, d.focus == focus, label, opts, idx, "", false, width, 2), "\n") + "\n")
	}
	input := func(focus int, label string, in textinput.Model) {
		line := fieldRow(s, d.focus == focus, label) + " " + in.View()
		b.WriteString(strings.Join(foldReadout(line, 12, width), "\n") + "\n")
	}

	input(schedStopName, "name", d.name)
	if d.edit != nil {
		// an edit keeps the kind: the row is a readout, off the walk —
		// the same fixed field the web form shows
		line := fieldRow(s, false, "kind") + " " + scheduleKindLabels[d.kind] +
			"  " + s.Faint.Render("· an edit keeps the kind")
		b.WriteString(strings.Join(foldReadout(line, 12, width), "\n") + "\n")
	} else {
		cycle(schedStopKind, "kind", scheduleKindLabels, d.kind)
	}
	if d.kindIsHeartbeat() {
		input(schedStopTarget, "target", d.target)
	} else {
		if d.repo.shown() {
			b.WriteString(strings.Join(choiceRowLines(s, d.focus == schedStopRepo, "repo", d.repo.options(), d.repo.idx, repoUnsetLabel, false, width, 2), "\n") + "\n")
		}
		backend := fieldRow(s, d.focus == schedStopBackend, "agent") + " " + d.backendRow()
		b.WriteString(strings.Join(foldReadout(backend, 12, width), "\n") + "\n")
		if d.backendName() != "" {
			input(schedStopModel, "model", d.model)
		}
		input(schedStopEnvelope, "budget", d.env)
		b.WriteString(s.Faint.Render(ansi.Wrap(d.envHint, width, "  ")) + "\n")
	}
	input(schedStopEvery, "every", d.every)
	input(schedStopCron, "cron", d.cron)
	input(schedStopZone, "zone", d.zone)
	input(schedStopPrompt, "prompt", d.prompt)

	b.WriteString("\n" + d.previewLine(s, width) + "\n")
	b.WriteString("\n" + d.buttons.ViewWidth(s, d.focus == schedStopButtons, width) + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(ansi.Wrap(d.errText, width, " -")))
	}
	hint := "tab next · ←/→ change · ↑/↓ suggest · enter save · esc cancel"
	if d.focus == schedStopButtons {
		hint = "←/→ buttons · enter activate · tab next · esc cancel"
	}
	b.WriteString("\n" + s.Faint.Render(strings.Join(wrapHint(hint, width), "\n")))
	return s.DialogFrame.Render(b.String())
}

// backendRow is the agent row's value: the picked backend, with the
// install flag a row that cannot start here wears.
func (d *scheduleForm) backendRow() string {
	b := d.backends[d.backend]
	if b.name == "" {
		return "profile default"
	}
	if !b.installed {
		return b.name + " — not installed"
	}
	return b.name
}

// previewLine is the live answer under the cadence rows: what the store
// would hold, and when it would fire — or why it would not.
func (d *scheduleForm) previewLine(s *theme.Styles, width int) string {
	if d.preview.Err != nil {
		return s.Error.Render(ansi.Wrap(sanitize(d.preview.Err.Error()), width, " -"))
	}
	var parts []string
	parts = append(parts, s.Faint.Render("stores as ")+s.Base.Render(d.preview.Cron))
	if len(d.preview.Next) > 0 {
		var fires []string
		for _, t := range d.preview.Next {
			fires = append(fires, t.Format("Jan 2 15:04"))
		}
		parts = append(parts, s.Faint.Render("next fires ")+s.Base.Render(strings.Join(fires, ", ")))
	}
	return ansi.Wrap(strings.Join(parts, "  ·  "), width, "  ")
}

// setRepoChoices re-offers the repositories after a rescan (repoChoices).
func (d *scheduleForm) setRepoChoices(names []string, _ map[string]string, _ map[string][]string) {
	d.repo.rechoose(names)
}
