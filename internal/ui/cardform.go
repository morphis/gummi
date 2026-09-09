package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// cardForm is the one dialog for a new card of any kind — feature, bug or
// research — reached by `n`, with `B`, `R` and `G` as presets that open
// it with a row already set. It asks three questions in the order a
// person can answer them (where, what kind, what), then reads back what
// will be created and how it will run.
//
// Rule 0: the dialog never acts on its own. A pasted issue reference is
// an offer on the `from` line; alt+g accepts it; enter only ever creates.
// Labels an import brings back are reported, never applied. Nothing
// fetches, moves a row or rewrites the box without a key.
//
// PROPOSAL-new-card.md holds the rationale and the settled decisions.
type cardForm struct {
	kind domain.Kind

	repo repoPicker
	// origins caches each repository's parsed origin, resolved through
	// originFor the first time its row is chosen. nil originFor (a test
	// scaffold, or a shell with no pool) renders no readout at all.
	origins   map[string]repoOrigin
	originFor func(repo string) repoOrigin

	text textarea.Model
	env  textinput.Model

	profiles []string
	profile  int
	sev      int

	// after is the dependency chips; afterCands the cards on offer (every
	// card not yet done, any repo), filtered live by afterFilter while the
	// row has focus.
	after       []domain.FeatureID
	afterCands  []afterCand
	afterFilter textinput.Model
	afterCursor int

	// expanded shows the run options as rows instead of one readout line.
	expanded bool

	// imported is the issue whose title and body the box currently holds,
	// once alt+g has accepted an offer; fetching marks the round trip.
	imported *importedIssue
	fetching bool
	// fromPicker marks a form filled from the browse picker: Create then
	// returns to the picker rather than the board.
	fromPicker bool

	focus   int // a cardStop, always one of stops()
	errText string
	buttons *buttonRow

	onSubmit func(formResult) tea.Cmd
	// onImport fetches ref (never bare: the caller resolved it against the
	// chosen repo's origin) and delivers the result back through
	// applyIssue. onBrowse opens the issue picker for the chosen repo; the
	// form is popped while it is open and pushed back when it returns.
	onImport func(ref domain.IssueRef, repo string) tea.Cmd
	onBrowse func(repo string) tea.Cmd
	// onCancel runs when the form is abandoned — esc, or the Cancel
	// button. Nil for the plain new-card door, where abandoning it loses
	// nothing that was not typed into it. It is set by the caller that
	// seeded the box from somewhere else: a re-entry's "not this card's
	// work" opens this form already holding a line the reader typed on a
	// card page, and dropping the form has to give that line back rather
	// than being the one gesture in the package that discards prose.
	onCancel func() tea.Cmd
}

// afterCand is one card the `after` row can name.
type afterCand struct {
	ID    domain.FeatureID
	Title string
	Repo  string
	Stage domain.Stage
}

// importedIssue is what alt+g brought into the box: the proposal, the
// resolved reference, and the exact text inserted, so a refetch replaces
// that text and nothing else.
type importedIssue struct {
	prop domain.BugProposal
	ref  domain.IssueRef
	text string
}

// repoOrigin is a repository's `origin` remote, parsed. An empty host is
// no origin at all.
type repoOrigin struct {
	host, ownerRepo string
}

func (o repoOrigin) github() bool { return o.host == "github.com" }

// label is the readout at the right end of the repo row.
func (o repoOrigin) label() string {
	switch {
	case o.host == "":
		return "no origin"
	case o.github():
		return "github.com/" + o.ownerRepo
	default:
		return o.host + " · not github"
	}
}

// The dialog's focus stops. Not every stop is live at once: repo needs a
// choice to be one, runs is the collapsed options line and expands the
// moment focus reaches it, severity exists for bugs only. stops() is the
// live list; focus always names one of its entries.
const (
	cardStopKind = iota
	cardStopRepo
	cardStopText
	cardStopRuns
	cardStopEnvelope
	cardStopProfile
	cardStopSeverity
	cardStopAfter
	cardStopButtons
)

var cardKinds = []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch}

// cardIDPrefix is what the `becomes` line shows for each kind: the id's
// prefix without a number, since the number is minted only at Create.
var cardIDPrefix = map[domain.Kind]string{domain.KindFeature: "FD", domain.KindBug: "BG", domain.KindResearch: "RS"}

// newCardForm builds the door. kind presets the kind row; repos and
// hasDefault shape the repo row as in every other creation dialog;
// lastRepo preselects the repo chosen last time this session (a name
// that is not configured is ignored); cands are the cards the `after`
// row may name; defaultEnvelope prefills the envelope.
func newCardForm(kind domain.Kind, profiles, repos []string, hasDefault bool, lastRepo string, cands []afterCand, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *cardForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	if !kind.Valid() {
		kind = domain.KindFeature
	}
	text := textarea.New()
	text.CharLimit = 4096
	text.ShowLineNumbers = false
	text.SetWidth(descWidthMin)
	text.SetHeight(descHeightMin)
	env := textinput.New()
	env.SetWidth(12)
	env.CharLimit = 12
	env.SetValue(strconv.Itoa(defaultEnvelope))
	filter := textinput.New()
	filter.Placeholder = "id or title…"
	filter.CharLimit = 60
	filter.SetWidth(24)

	repo := newRepoPicker(repos, hasDefault)
	for i, name := range repo.options() {
		if lastRepo != "" && name == lastRepo && repo.multi() {
			repo.idx = i
		}
	}
	d := &cardForm{
		kind: kind, repo: repo, origins: map[string]repoOrigin{},
		text: text, env: env, profiles: profiles,
		afterCands: cands, afterFilter: filter,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Create"}, button{label: "Create & start"}),
		onSubmit: onSubmit,
	}
	d.buttons.SetCursor(1)
	// the repository is the one field with no default, so a form that
	// still needs one opens on it; everything else opens in the text.
	if d.repo.needsChoice() {
		d.setFocus(cardStopRepo)
	} else {
		d.setFocus(cardStopText)
	}
	d.text.Placeholder = cardPlaceholder
	return d
}

// cardPlaceholder is the whole manual: what the box accepts, in three
// lines, shown only while it is empty.
const cardPlaceholder = "Describe it. The first line is the title.\n\n" +
	"  #123 or an issue link on the first line: alt+g imports it\n" +
	"  bug headings: Steps to reproduce · Expected · Actual · Environment\n" +
	"  feature: ## Acceptance seeds the verification plan"

// ID implements overlay.Dialog.
func (d *cardForm) ID() string { return "new-card" }

// cancel is the abandon path, both keys that reach it (esc and the
// Cancel button) routed through one place so they cannot diverge on what
// leaving without creating anything does.
func (d *cardForm) cancel() tea.Cmd {
	if d.onCancel == nil {
		return nil
	}
	return d.onCancel()
}

// SetText replaces the box's content (a preset's seed, a test's fixture).
func (d *cardForm) SetText(s string) { d.text.SetValue(s) }

// Text is the box's current content.
func (d *cardForm) Text() string { return d.text.Value() }

// Kind is the kind row's current choice.
func (d *cardForm) Kind() domain.Kind { return d.kind }

// stops is the live focus ring, in tab order.
func (d *cardForm) stops() []int {
	s := []int{cardStopKind}
	if d.repo.multi() {
		s = append(s, cardStopRepo)
	}
	s = append(s, cardStopText)
	if d.expanded {
		s = append(s, cardStopEnvelope, cardStopProfile)
		if d.kind == domain.KindBug {
			s = append(s, cardStopSeverity)
		}
		s = append(s, cardStopAfter)
	} else {
		s = append(s, cardStopRuns)
	}
	return append(s, cardStopButtons)
}

// advanceFocus moves by dir over stops(), wrapping. Landing on the
// collapsed runs line expands it: the options are reached by tabbing
// onto them, and there is nothing to do on the line itself.
func (d *cardForm) advanceFocus(dir int) {
	stops := d.stops()
	i := 0
	for j, s := range stops {
		if s == d.focus {
			i = j
			break
		}
	}
	next := stops[((i+dir)%len(stops)+len(stops))%len(stops)]
	if next == cardStopRuns {
		d.expanded = true
		if dir > 0 {
			next = cardStopEnvelope
		} else {
			next = cardStopAfter
		}
	}
	d.setFocus(next)
}

func (d *cardForm) setFocus(f int) {
	d.focus = f
	d.text.Blur()
	d.env.Blur()
	d.afterFilter.Blur()
	switch f {
	case cardStopText:
		d.text.Focus()
	case cardStopEnvelope:
		d.env.Focus()
	case cardStopAfter:
		d.afterFilter.Focus()
		d.afterCursor = 0
	}
}

// setKind moves the kind row. A severity stop that no longer exists
// hands focus to the next row rather than dangling.
func (d *cardForm) setKind(k domain.Kind) {
	d.kind = k
	if d.focus == cardStopSeverity && k != domain.KindBug {
		d.setFocus(cardStopAfter)
	}
}

func (d *cardForm) cycleKind(delta int) {
	n := len(cardKinds)
	i := 0
	for j, k := range cardKinds {
		if k == d.kind {
			i = j
		}
	}
	d.setKind(cardKinds[((i+delta)%n+n)%n])
}

// origin resolves the chosen repo's origin, caching per name. The zero
// origin means "unknown": no seam, or a repo with no remote.
func (d *cardForm) origin() repoOrigin {
	if d.originFor == nil || !d.repo.chosen() {
		return repoOrigin{}
	}
	name := d.repo.name()
	if o, ok := d.origins[name]; ok {
		return o
	}
	o := d.originFor(name)
	d.origins[name] = o
	return o
}

// reference is the issue reference the first line of the box is, if it
// is nothing else.
func (d *cardForm) reference() (domain.IssueRef, bool) {
	return domain.ParseIssueRef(domain.FirstLine(d.text.Value()))
}

// HandleKey implements overlay.Dialog.
func (d *cardForm) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	k := key.String()
	switch k {
	case "esc":
		return true, d.cancel()
	case "tab":
		d.advanceFocus(1)
		return false, nil
	case "shift+tab":
		d.advanceFocus(-1)
		return false, nil
	case "alt+o":
		d.expanded = !d.expanded
		switch {
		case d.expanded && d.focus == cardStopText:
			// expanding from the text keeps the person where they were
		case !d.expanded && (d.focus == cardStopEnvelope || d.focus == cardStopProfile || d.focus == cardStopSeverity || d.focus == cardStopAfter):
			d.setFocus(cardStopText)
		}
		return false, nil
	case "alt+g":
		return d.github()
	case "alt+enter", "ctrl+j":
		if d.focus == cardStopText {
			d.text.InsertString("\n")
			d.errText = ""
		}
		return false, nil
	}

	switch d.focus {
	case cardStopButtons:
		switch k {
		case "left", "h":
			d.buttons.Move(-1)
		case "right", "l":
			d.buttons.Move(1)
		case "enter":
			switch d.buttons.Cursor() {
			case 0:
				return true, d.cancel()
			case 1:
				return d.submit(false)
			default:
				return d.submit(true)
			}
		}
		return false, nil
	case cardStopKind:
		if delta, ok := selectCycleDelta(k); ok {
			d.cycleKind(delta)
			return false, nil
		}
		if k == "enter" {
			return d.submit(false)
		}
		return d.fallThrough(key)
	case cardStopRepo:
		if delta, ok := selectCycleDelta(k); ok {
			d.repo.cycle(delta)
			d.errText = ""
			return false, nil
		}
		if n := digitKey(key); n > 0 && n <= len(d.repo.options()) {
			d.repo.idx = n - 1
			d.errText = ""
			return false, nil
		}
		if k == "enter" {
			return d.submit(false)
		}
		return d.fallThrough(key)
	case cardStopText:
		if k == "enter" {
			return d.submit(false)
		}
		d.text, _ = d.text.Update(key)
		d.errText = ""
	case cardStopEnvelope:
		if k == "enter" {
			return d.submit(false)
		}
		d.env, _ = d.env.Update(key)
		d.errText = ""
	case cardStopProfile:
		if delta, ok := selectCycleDelta(k); ok {
			n := len(d.profiles)
			d.profile = ((d.profile+delta)%n + n) % n
		} else if k == "enter" {
			return d.submit(false)
		}
	case cardStopSeverity:
		if delta, ok := selectCycleDelta(k); ok {
			n := len(bugSeverityChoices)
			d.sev = ((d.sev+delta)%n + n) % n
		} else if k == "enter" {
			return d.submit(false)
		}
	case cardStopAfter:
		if !d.handleAfterKey(key) {
			// enter with nothing to add is enter: create
			return d.submit(false)
		}
	}
	return false, nil
}

// fallThrough is the kind and repo rows' answer to a printable key: it
// belongs to the text, so focus moves there and the key types. A person
// who starts describing the card on the wrong row never cycles or
// filters anything by accident.
func (d *cardForm) fallThrough(key tea.KeyPressMsg) (bool, tea.Cmd) {
	if key.Text == "" || key.Mod != 0 {
		return false, nil
	}
	d.setFocus(cardStopText)
	d.text, _ = d.text.Update(key)
	d.errText = ""
	return false, nil
}

// digitKey returns 1–9 for a bare digit key, else 0.
func digitKey(key tea.KeyPressMsg) int {
	if key.Mod != 0 || len(key.Text) != 1 || key.Text[0] < '1' || key.Text[0] > '9' {
		return 0
	}
	return int(key.Text[0] - '0')
}

// handleAfterKey drives the dependency row: typing filters the list,
// up/down move over it, enter adds the highlighted card as a chip,
// backspace with an empty filter removes the last chip. It reports
// false for the one key it leaves alone: enter with no card to add.
func (d *cardForm) handleAfterKey(key tea.KeyPressMsg) bool {
	vis := d.afterVisible()
	switch key.String() {
	case "up", "ctrl+p":
		d.afterCursor = max(d.afterCursor-1, 0)
		return true
	case "down", "ctrl+n":
		d.afterCursor = min(d.afterCursor+1, max(len(vis)-1, 0))
		return true
	case "enter":
		if len(vis) == 0 {
			return false
		}
		c := vis[min(d.afterCursor, len(vis)-1)]
		for _, id := range d.after {
			if id == c.ID {
				return true
			}
		}
		d.after = append(d.after, c.ID)
		d.afterFilter.SetValue("")
		d.afterCursor = 0
		return true
	case "backspace":
		if d.afterFilter.Value() == "" {
			if n := len(d.after); n > 0 {
				d.after = d.after[:n-1]
			}
			return true
		}
	}
	d.afterFilter, _ = d.afterFilter.Update(key)
	d.afterCursor = 0
	return true
}

// afterVisible is the candidate list as the row shows it: the chosen
// repo's cards first, then the rest, both filtered by id or title and
// with already-chosen cards left out.
func (d *cardForm) afterVisible() []afterCand {
	q := strings.ToLower(strings.TrimSpace(d.afterFilter.Value()))
	chosen := map[domain.FeatureID]bool{}
	for _, id := range d.after {
		chosen[id] = true
	}
	var mine, others []afterCand
	for _, c := range d.afterCands {
		if chosen[c.ID] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(string(c.ID)), q) && !strings.Contains(strings.ToLower(c.Title), q) {
			continue
		}
		if c.Repo == d.repo.name() {
			mine = append(mine, c)
		} else {
			others = append(others, c)
		}
	}
	return append(mine, others...)
}

// github is alt+g: refetch an import, import the offered reference, or
// browse — whichever the form's state calls for. Every branch that needs
// a repository and has none moves focus to the repo row and says so.
func (d *cardForm) github() (bool, tea.Cmd) {
	if d.fetching {
		return false, nil
	}
	if d.imported != nil {
		if d.onImport == nil {
			return false, nil
		}
		if !strings.Contains(d.text.Value(), d.imported.text) {
			d.errText = "the imported text was edited — clear it to fetch the issue again"
			return false, nil
		}
		d.fetching = true
		return false, d.onImport(d.imported.ref, d.repo.name())
	}
	ref, ok := d.reference()
	if !ok {
		if d.repo.needsChoice() {
			d.errText = "choose a repository to browse its issues"
			d.setFocus(cardStopRepo)
			return false, nil
		}
		if d.onBrowse == nil {
			return false, nil
		}
		return true, d.onBrowse(d.repo.name())
	}
	if ref.Bare() {
		if d.repo.needsChoice() {
			d.errText = fmt.Sprintf("choose a repository to resolve %s", ref)
			d.setFocus(cardStopRepo)
			return false, nil
		}
		o := d.origin()
		if !o.github() {
			d.errText = fmt.Sprintf("%s has no GitHub origin to resolve %s against — paste the issue's link instead", d.repo.label(), ref)
			return false, nil
		}
		owner, repo, _ := strings.Cut(o.ownerRepo, "/")
		ref.Owner, ref.Repo = owner, repo
	}
	if d.onImport == nil {
		return false, nil
	}
	d.fetching = true
	d.errText = ""
	return false, d.onImport(ref, d.repo.name())
}

// applyIssue is the import's landing: the reference line in the box is
// replaced by the issue's title and body — and only that line; anything
// else the person typed stays — and the `from` line turns from offer to
// provenance. Severity is read from the labels; the kind row is not
// touched, the labels are reported beside the reference instead. A
// refetch replaces exactly the text the previous import inserted.
func (d *cardForm) applyIssue(p domain.BugProposal, ref domain.IssueRef) {
	d.fetching = false
	d.errText = ""
	inserted := strings.TrimSpace(p.Title)
	if body := strings.TrimSpace(p.Body); body != "" {
		inserted += "\n\n" + body
	}
	cur := d.text.Value()
	switch {
	case d.imported != nil && strings.Contains(cur, d.imported.text):
		cur = strings.Replace(cur, d.imported.text, inserted, 1)
	default:
		first := domain.FirstLine(cur)
		if first != "" {
			if _, isRef := domain.ParseIssueRef(first); isRef {
				cur = strings.Replace(cur, first, inserted, 1)
			} else {
				cur = inserted + "\n\n" + strings.TrimSpace(cur)
			}
		} else {
			cur = inserted
		}
	}
	d.text.SetValue(cur)
	d.text.MoveToBegin()
	d.imported = &importedIssue{prop: p, ref: ref, text: inserted}
	if p.Severity != "" {
		for i, s := range bugSeverityChoices {
			if s == p.Severity {
				d.sev = i
			}
		}
	}
}

// fill is the browse picker's landing: the same as an import, with the
// form marked so Create returns to the picker.
func (d *cardForm) fill(p domain.BugProposal) {
	ref, _ := domain.ParseIssueRef(p.ExternalRef)
	if ref.Number == 0 {
		ref.Number = p.Number
	}
	d.applyIssue(p, ref)
	d.fromPicker = true
	d.setFocus(cardStopText)
}

// failImport is the import's other landing.
func (d *cardForm) failImport(err error) {
	d.fetching = false
	d.errText = "import: " + sanitize(err.Error())
}

// submit validates and fires onSubmit. start marks Create & start.
func (d *cardForm) submit(start bool) (bool, tea.Cmd) {
	if d.repo.needsChoice() {
		d.errText = repoUnchosenErr
		d.setFocus(cardStopRepo)
		return false, nil
	}
	desc := strings.TrimSpace(d.text.Value())
	if desc == "" {
		d.errText = "description must not be empty"
		return false, nil
	}
	if _, isRef := d.reference(); isRef && d.imported == nil {
		d.errText = "that is an issue reference — alt+g imports it, or describe the card instead"
		return false, nil
	}
	title, _, _ := domain.SplitFreeform(desc)
	if _, err := domain.Slugify(title); err != nil {
		d.errText = "the first line needs a letter or digit to make a title"
		return false, nil
	}
	var env *int
	trimmed := strings.TrimSpace(d.env.Value())
	if trimmed == "" && d.kind == domain.KindResearch {
		d.errText = "envelope required — a research card carries no default budget"
		return false, nil
	}
	if trimmed != "" {
		n, err := strconv.Atoi(trimmed)
		if err != nil || n < 0 {
			d.errText = "envelope must be a non-negative number of credits"
			return false, nil
		}
		env = &n
	}
	res := formResult{
		Kind: d.kind, Desc: desc, Profile: d.profiles[d.profile], Envelope: env,
		Repo: d.repo.name(), Source: "manual", After: append([]domain.FeatureID(nil), d.after...),
		Start: start, FromPicker: d.fromPicker,
	}
	if d.kind == domain.KindBug {
		res.Severity = bugSeverityChoices[d.sev]
	}
	if d.imported != nil {
		res.Source = d.imported.prop.Source
		res.ExternalRef = d.imported.prop.ExternalRef
		res.Discussion = d.imported.prop.Report.Discussion
	}
	if d.onSubmit == nil {
		return true, nil
	}
	return true, d.onSubmit(res)
}

// HandlePaste implements overlay.Paster: pasted text goes into the text
// while it's focused, newlines intact — and into the after filter or the
// envelope when those are.
func (d *cardForm) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	switch d.focus {
	case cardStopText:
		d.text, _ = d.text.Update(msg)
		d.errText = ""
	case cardStopEnvelope:
		d.env, _ = d.env.Update(msg)
	case cardStopAfter:
		d.afterFilter, _ = d.afterFilter.Update(msg)
	case cardStopKind, cardStopRepo:
		d.setFocus(cardStopText)
		d.text, _ = d.text.Update(msg)
		d.errText = ""
	}
	return nil
}

// --- rendering ---

const cardLabelW = 9 // "becomes" plus two spaces, the widest label

func cardLabel(s *theme.Styles, label string) string {
	return "  " + s.Faint.Render(fmt.Sprintf("%-*s", cardLabelW, label))
}

// choiceRow renders a side-by-side option row: the chosen option carries
// the marker, and the focused row bands its chosen option.
func choiceRow(s *theme.Styles, focused bool, label string, opts []string, idx int, unsetLabel string) string {
	return cardLabel(s, label) + choices(s, focused, opts, idx, unsetLabel, false)
}

// choices renders the options of a row. numbered prefixes each with its
// digit — the repo row's hint that 1–9 choose there.
func choices(s *theme.Styles, focused bool, opts []string, idx int, unsetLabel string, numbered bool) string {
	parts := make([]string, 0, len(opts)+1)
	if idx < 0 {
		if focused {
			parts = append(parts, s.Band(" "+unsetLabel+" ", 0, true))
		} else {
			parts = append(parts, s.Error.Render(unsetLabel))
		}
	}
	for i, o := range opts {
		num := ""
		if numbered && i < 9 {
			num = s.Faint.Render(strconv.Itoa(i+1)) + " "
		}
		switch {
		case i == idx && focused:
			parts = append(parts, num+s.Band(s.BandMarker(true)+s.BandText.Render(o)+" ", 0, true))
		case i == idx:
			parts = append(parts, num+s.KeyHint.Render("▸ ")+s.Base.Render(o))
		default:
			parts = append(parts, num+s.Faint.Render(o))
		}
	}
	return strings.Join(parts, "    ")
}

// optionLabel is an expanded option row's label cell: indented under
// "runs as", banded when its row has focus.
func optionLabel(s *theme.Styles, focused bool, label string) string {
	cell := fmt.Sprintf("%-10s", label)
	if focused {
		return "    " + s.Band(cell, 0, true)
	}
	return "    " + s.Faint.Render(cell)
}

// fromLine is the offer or the provenance, or "" when the box holds
// neither an import nor a reference.
func (d *cardForm) fromLine(s *theme.Styles) string {
	if d.imported != nil {
		p := d.imported.prop
		parts := []string{s.Success.Render(p.Source), d.imported.ref.String()}
		if p.State != "" {
			parts = append(parts, p.State)
		}
		if len(p.Labels) > 0 {
			parts = append(parts, s.Faint.Render("labelled "+strings.Join(p.Labels, ", ")))
		}
		if n := strings.Count(p.Report.Discussion, "**"); n > 0 {
			parts = append(parts, fmt.Sprintf("%d comments", n/2))
		}
		parts = append(parts, fmt.Sprintf("%d lines", strings.Count(strings.TrimSpace(d.imported.text), "\n")+1))
		tail := s.KeyHint.Render("↻ alt+g")
		if d.fetching {
			tail = s.Faint.Render("fetching…")
		}
		return cardLabel(s, "from") + strings.Join(parts, " · ") + "   " + tail
	}
	ref, ok := d.reference()
	if !ok {
		return ""
	}
	if d.fetching {
		return cardLabel(s, "from") + s.Faint.Render("fetching "+ref.String()+"…")
	}
	if ref.Bare() {
		if d.repo.needsChoice() {
			return cardLabel(s, "from") + s.Faint.Render("choose a repository to resolve ") + ref.String() + s.Faint.Render(" · ") + s.KeyHint.Render("alt+g")
		}
		o := d.origin()
		if !o.github() && d.originFor != nil {
			return cardLabel(s, "from") + s.Faint.Render(d.repo.label()+": no GitHub origin to resolve ") + ref.String() + s.Faint.Render(" against")
		}
		if o.github() {
			ref.Owner, ref.Repo, _ = strings.Cut(o.ownerRepo, "/")
		}
	}
	return cardLabel(s, "from") + s.Faint.Render("looks like ") + ref.String() + s.Faint.Render(" · ") + s.KeyHint.Render("alt+g") + s.Faint.Render(" import it")
}

// becomesLine reads back the card the text would create: prefix and
// slug, with a bug's severity. No number — that is minted at Create.
func (d *cardForm) becomesLine(s *theme.Styles) string {
	desc := strings.TrimSpace(d.text.Value())
	if desc == "" {
		return cardLabel(s, "becomes") + s.Faint.Render("—")
	}
	if _, isRef := d.reference(); isRef && d.imported == nil {
		return cardLabel(s, "becomes") + s.Faint.Render("— import the reference first, or describe the card")
	}
	title, _, _ := domain.SplitFreeform(desc)
	slug, err := domain.Slugify(title)
	if err != nil {
		return cardLabel(s, "becomes") + s.Error.Render("the first line needs a letter or digit to make a title")
	}
	out := cardIDPrefix[d.kind] + " · " + slug
	if d.kind == domain.KindBug && bugSeverityChoices[d.sev] != "" {
		out += " · severity " + string(bugSeverityChoices[d.sev])
		if d.imported != nil && d.imported.prop.Severity == bugSeverityChoices[d.sev] {
			out += s.Faint.Render(" from label")
		}
	}
	return cardLabel(s, "becomes") + out
}

// runsLine is the collapsed options readout.
func (d *cardForm) runsLine(s *theme.Styles) string {
	env := strings.TrimSpace(d.env.Value())
	switch {
	case env == "" && d.kind == domain.KindResearch:
		env = s.Error.Render("envelope required")
	case env == "":
		env = "default envelope"
	default:
		env += " credits"
	}
	out := env + " · " + d.profiles[d.profile]
	if len(d.after) > 0 {
		out += " · after " + joinIDs(d.after)
	}
	return cardLabel(s, "runs as") + out + "   " + s.KeyHint.Render("alt+o") + s.Faint.Render(" edit")
}

func joinIDs(ids []domain.FeatureID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return strings.Join(parts, ", ")
}

// optionRows are the expanded options: envelope, profile, severity (bugs),
// after — and the after list while that row has focus.
func (d *cardForm) optionRows(s *theme.Styles, width int) []string {
	hint := envelopeHintCapped
	if d.kind == domain.KindResearch {
		hint = envelopeHintRequired
	}
	rows := []string{
		cardLabel(s, "runs as"),
		optionLabel(s, d.focus == cardStopEnvelope, "envelope") + d.env.View() + " " + s.Faint.Render(hint),
		optionLabel(s, d.focus == cardStopProfile, "profile") + choices(s, d.focus == cardStopProfile, d.profiles, d.profile, "", false),
	}
	if d.kind == domain.KindBug {
		labels := make([]string, len(bugSeverityChoices))
		for i, sev := range bugSeverityChoices {
			labels[i] = string(sev)
			if sev == "" {
				labels[i] = "unset"
			}
		}
		rows = append(rows, optionLabel(s, d.focus == cardStopSeverity, "severity")+choices(s, d.focus == cardStopSeverity, labels, d.sev, "", false))
	}
	chips := make([]string, len(d.after))
	for i, id := range d.after {
		chips[i] = s.KeyHint.Render(string(id))
	}
	line := optionLabel(s, d.focus == cardStopAfter, "after") + strings.Join(chips, " ")
	switch {
	case d.focus == cardStopAfter:
		if len(chips) > 0 {
			line += "  "
		}
		line += s.KeyHint.Render("/ ") + d.afterFilter.View()
	case len(chips) == 0:
		line += s.Faint.Render("—   tab to add")
	}
	rows = append(rows, line)
	if d.focus == cardStopAfter {
		rows = append(rows, d.afterListRows(s, width)...)
	}
	return rows
}

// afterListRows renders the filtered candidates under the after row:
// the chosen repo's first, a divider, then the rest; at most five rows
// windowed around the cursor.
func (d *cardForm) afterListRows(s *theme.Styles, width int) []string {
	vis := d.afterVisible()
	if len(vis) == 0 {
		return []string{"               " + s.Faint.Render("no open card matches")}
	}
	const maxRows = 5
	d.afterCursor = min(d.afterCursor, len(vis)-1)
	start := max(0, min(d.afterCursor-maxRows/2, len(vis)-maxRows))
	var rows []string
	dividerDone := false
	for i := start; i < len(vis) && i < start+maxRows; i++ {
		c := vis[i]
		if !dividerDone && c.Repo != d.repo.name() && (i == 0 || vis[i-1].Repo == d.repo.name()) {
			rows = append(rows, "               "+s.Faint.Render("── other repos ──"))
			dividerDone = true
		}
		marker := "  "
		style := s.Faint
		if i == d.afterCursor {
			marker = s.KeyHint.Render("▸ ")
			style = s.Base
		}
		meta := string(c.Stage)
		if c.Repo != "" {
			meta = c.Repo + " · " + meta
		}
		title := ansi.Truncate(c.Title, max(width-cardLabelW-len(c.ID)-len(meta)-14, 8), "…")
		rows = append(rows, "             "+marker+style.Render(string(c.ID)+"  "+title)+"  "+s.Faint.Render(meta))
	}
	return rows
}

// View implements overlay.Dialog.
func (d *cardForm) View(s *theme.Styles, w, h int) string {
	kinds := make([]string, len(cardKinds))
	kindIdx := 0
	for i, k := range cardKinds {
		kinds[i] = string(k)
		if k == d.kind {
			kindIdx = i
		}
	}
	from := d.fromLine(s)
	showHint := h >= 24

	// static rows: title+blank(2), kind(1), repo(1)?, from(1)?, blank(1),
	// blank-after-text(1), becomes(1), runs(1 or expanded), blank+buttons(2),
	// error(1)?, blank+hint(2)?
	static := 2 + 1 + 1 + 1 + 1
	if d.repo.shown() {
		static++
	}
	if from != "" {
		static++
	}
	var opts []string
	textW, _ := dialogDescSize(w, h, static)
	if d.expanded {
		opts = d.optionRows(s, textW)
		static += len(opts)
	} else {
		static++
	}
	static += 2
	if d.errText != "" {
		static++
	}
	if showHint {
		static += 2
	}
	textW, textH := dialogDescSize(w, h, static)
	d.text.SetWidth(textW)
	d.text.SetHeight(textH)

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("new card") + "\n\n")
	b.WriteString(choiceRow(s, d.focus == cardStopKind, "kind", kinds, kindIdx, "") + "\n")
	if d.repo.shown() {
		row := cardLabel(s, "repo") + choices(s, d.focus == cardStopRepo, d.repo.options(), d.repo.idx, repoUnsetLabel, d.focus == cardStopRepo)
		if d.originFor != nil && d.repo.chosen() {
			origin := s.Faint.Render(d.origin().label())
			if pad := textW - ansi.StringWidth(row) - ansi.StringWidth(origin); pad > 2 {
				row += strings.Repeat(" ", pad)
			} else {
				row += "  "
			}
			row += origin
		}
		b.WriteString(row + "\n")
	}
	if from != "" {
		b.WriteString(from + "\n")
	}
	b.WriteString("\n" + d.text.View() + "\n\n")
	b.WriteString(d.becomesLine(s) + "\n")
	if d.expanded {
		b.WriteString(strings.Join(opts, "\n") + "\n")
	} else {
		b.WriteString(d.runsLine(s) + "\n")
	}
	b.WriteString("\n" + d.buttons.View(s, d.focus == cardStopButtons) + "\n")
	if d.errText != "" {
		b.WriteString("\n" + s.Error.Render(d.errText))
	}
	if showHint {
		b.WriteString("\n" + s.Faint.Render(d.hint()))
	}
	return s.DialogFrame.Render(strings.TrimRight(b.String(), "\n"))
}

// hint is the key line for the focused row.
func (d *cardForm) hint() string {
	switch d.focus {
	case cardStopRepo:
		return "←/→ or 1–9 choose a repo · typing goes to the text · tab next · esc cancel"
	case cardStopKind:
		return "←/→ choose the kind · typing goes to the text · tab next · esc cancel"
	case cardStopText:
		g := "alt+g browse issues"
		if d.imported != nil {
			g = "alt+g refetch"
		} else if _, ok := d.reference(); ok {
			g = "alt+g import"
		}
		return "tab rows · " + g + " · alt+o options · alt+enter newline · enter create · esc cancel"
	case cardStopAfter:
		return "type to filter · ↑/↓ move · enter add · backspace remove last · tab next · esc cancel"
	case cardStopButtons:
		return "←/→ buttons · enter activate · tab next · esc cancel"
	default:
		return "tab rows · ←/→ choose · type a number · alt+o collapse · enter create · esc cancel"
	}
}

// sortAfterCands orders candidates by id for a stable list.
func sortAfterCands(c []afterCand) {
	sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
}
