package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	ct domain.CardType

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

	// base is the branch the card's work forks from, and baseCands the
	// branches the chosen repo actually has. An empty base means "what
	// the checkout has out", which is the default and what every card
	// did before bases were selectable.
	base       string
	baseCands  []string
	baseCursor int
	// adopt is an existing branch to mint this card ONTO rather than
	// cutting one for it (DESIGN §10 D22). Empty — the default, and what
	// every card does — means gummi cuts the branch.
	//
	// It shares baseCands: the branches on offer to adopt are the same
	// branches on offer to fork from, because they are simply the
	// repository's branches. What differs is what the card then does with
	// the one it is given, which the row's own label says.
	adopt string
	// stackOnto names the card this one is being stacked on top of, and
	// stackCands the cards the row can name. stackInto names an existing
	// stack to join instead. All empty is a standalone card.
	//
	// They live in the expanded options rather than the main flow because
	// the overwhelming majority of cards are standalone, and a row every
	// reader has to tab past to say "no" is a row that costs more than it
	// gives. What keeps the T gesture legible is the `becomes` readout,
	// which is always visible and names the branch this card will fork
	// from — so the one case where stacking matters says so without the
	// reader opening anything.
	//
	// The row cycles its candidates rather than opening a searchable list
	// like `runs after` does, and that is the whole of keeping the two
	// apart on sight: a stack position is topology and a dependency is
	// scheduling (DESIGN §18.1), so the row that sets one must not look
	// like the row that sets the other. T stays the one-key path — it
	// preselects a card here — but a reader who opened the dialog with
	// `n` and only then thought of the card below is no longer told to
	// cancel and start again.
	stackOnto  domain.FeatureID
	stackLabel string
	stackInto  domain.StackID
	stackCands []stackCand
	// stackOffer remembers the card T named, so it stays on the row even
	// when the candidate list would not otherwise carry it.
	stackOffer domain.FeatureID

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

// stackCand is one card the `stack` row can fork from. Touched orders
// the row; Repo decides whether it is on offer at all, since a stack is
// one repository (DESIGN §18.1) and the repo row can still move after a
// card has been chosen here.
type stackCand struct {
	ID      domain.FeatureID
	Title   string
	Repo    string
	Touched time.Time
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
	cardStopBase
	cardStopAdopt
	cardStopStack
	cardStopButtons
)

// The kind row's choices are domain.CardTypes, which is five entries for
// four kinds: research appears twice, once as the survey and once as the
// diagnosis. They share the RS prefix because they are one kind, so the
// `becomes` line's prefix does not tell them apart — the kind word
// beside it does.

// newCardForm builds the door. kind presets the kind row; repos and
// hasDefault shape the repo row as in every other creation dialog;
// lastRepo preselects the repo chosen last time this session (a name
// that is not configured is ignored); cands are the cards the `after`
// row may name; defaultEnvelope prefills the budget.
func newCardForm(ct domain.CardType, profiles, repos []string, hasDefault bool, lastRepo string, cands []afterCand, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *cardForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	if !ct.Valid() {
		ct = domain.CardType{Kind: domain.KindFeature}
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
		ct: ct, repo: repo, origins: map[string]repoOrigin{},
		text: text, env: env, profiles: profiles,
		afterCands: cands, afterFilter: filter,
		buttons:  newButtonRow(button{label: "Cancel"}, button{label: "Create"}, button{label: "Create & autopilot"}),
		onSubmit: onSubmit,
	}
	d.buttons.SetCursor(1)
	// the repository is the one field with no default, so a form that
	// still needs one opens on it; everything else opens in the text.
	// A goal is never asked (asksRepo), so it opens in the text like any
	// card in a workspace with one repository.
	if d.asksRepo() && d.repo.needsChoice() {
		d.setFocus(cardStopRepo)
	} else {
		d.setFocus(cardStopText)
	}
	d.text.Placeholder = cardPlaceholderFor(ct)
	return d
}

// cardPlaceholderFor is the box's manual for one kind, shown only while
// the box is empty. It used to be one constant that listed a bug's
// headings and a feature's `## Acceptance` line together regardless of
// which kind was selected — a research card saw both hints and neither
// applied, and a feature card saw the bug headings it would never use.
// setKind refreshes this whenever the kind row moves, so only the
// selected kind's own hint is ever on screen.
func cardPlaceholderFor(c domain.CardType) string {
	head := "Describe it. The first line is the title.\n\n" +
		"  #123 or an issue link on the first line: alt+g imports it\n"
	if c.Mode == domain.ModeDiagnosis {
		return head + "  the rest becomes the symptom — what you saw, where, how often"
	}
	switch c.Kind {
	case domain.KindBug:
		return head + "  headings: Steps to reproduce · Expected · Actual · Environment"
	case domain.KindResearch:
		return head + "  the rest becomes the research brief"
	case domain.KindGoal:
		return "Describe the outcome you want. The first line is the title.\n\n" +
			"  next, the architect agrees with you what done means and which\n" +
			"  cards get there; then the goal runs them and comes back when ready"
	default: // domain.KindFeature
		return head + "  ## Acceptance seeds the verification plan"
	}
}

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

// Kind is the kind the row's current choice mints.
func (d *cardForm) Kind() domain.Kind { return d.ct.Kind }

// CardType is the row's current choice, kind and research mode together.
func (d *cardForm) CardType() domain.CardType { return d.ct }

// asksRepo reports whether this card's repository is the dialog's to ask
// for. Every kind's is, except a goal's: a goal is not in a repository —
// you describe an outcome, and the cards that meet it name their own
// (DESIGN §17.2). Its own home is derived from those cards at its plan
// gate, so asking here would be asking for an answer nobody has yet.
func (d *cardForm) asksRepo() bool {
	return d.ct.Kind != domain.KindGoal && d.repo.multi()
}

// stops is the live focus ring, in tab order.
func (d *cardForm) stops() []int {
	s := []int{cardStopKind}
	if d.asksRepo() {
		s = append(s, cardStopRepo)
	}
	s = append(s, cardStopText)
	if d.expanded {
		s = append(s, cardStopEnvelope, cardStopProfile)
		if d.ct.Kind == domain.KindBug {
			s = append(s, cardStopSeverity)
		}
		s = append(s, cardStopAfter)
		if len(d.baseCands) > 1 {
			s = append(s, cardStopBase)
		}
		// Adopting needs a branch to adopt, and a research card has no
		// branch of its own to put one on.
		if len(d.baseCands) > 0 && d.ct.Kind != domain.KindResearch {
			s = append(s, cardStopAdopt)
		}
		if d.asksStack() {
			s = append(s, cardStopStack)
		}
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

// firstOptionStop is the option row alt+o lands on: the budget when the
// card has one, and otherwise whatever this kind's first option is.
func (d *cardForm) firstOptionStop() int {
	stops := d.stops()
	for i, stop := range stops {
		if stop == cardStopText && i+1 < len(stops) {
			return stops[i+1]
		}
	}
	return cardStopText
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
// hands focus to the next row rather than dangling. The placeholder is
// re-picked too — it teaches what the free text turns into, and that
// differs by kind (see cardPlaceholderFor).
func (d *cardForm) setKind(c domain.CardType) {
	d.ct = c
	d.text.Placeholder = cardPlaceholderFor(c)
	if d.focus == cardStopSeverity && c.Kind != domain.KindBug {
		d.setFocus(cardStopAfter)
	}
	if d.focus == cardStopRepo && !d.asksRepo() {
		d.setFocus(cardStopText)
	}
}

func (d *cardForm) cycleKind(delta int) {
	n := len(domain.CardTypes)
	i := 0
	for j, c := range domain.CardTypes {
		if c == d.ct {
			i = j
		}
	}
	d.setKind(domain.CardTypes[((i+delta)%n+n)%n])
}

// formRepo is the repository the card is created in: the chosen one, and
// nothing at all for a goal, whose home the plan gate settles.
func (d *cardForm) formRepo() string {
	if d.ct.Kind == domain.KindGoal {
		return ""
	}
	return d.repo.name()
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
			// The row this opens is labelled "alt+o edit" and its first
			// field is a text input, which draws its "> " prompt whether
			// or not it has focus. Leaving focus in the description made
			// that prompt a lie: the next keystrokes edited the card's
			// text, out of sight below the panel that had just opened,
			// and a person correcting the budget deleted four characters
			// of their own brief before noticing. Opening the options
			// means editing them.
			d.setFocus(d.firstOptionStop())
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
	case cardStopBase:
		if delta, ok := selectCycleDelta(k); ok {
			d.cycleBase(delta)
		} else if k == "enter" {
			return d.submit(false)
		}
	case cardStopAdopt:
		if delta, ok := selectCycleDelta(k); ok {
			d.cycleAdopt(delta)
		} else if k == "enter" {
			return d.submit(false)
		}
	case cardStopStack:
		if delta, ok := selectCycleDelta(k); ok {
			d.cycleStack(delta)
		} else if k == "enter" {
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

// submit validates and fires onSubmit. start marks Create & autopilot.
func (d *cardForm) submit(start bool) (bool, tea.Cmd) {
	if d.asksRepo() && d.repo.needsChoice() {
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
	// A stack is one repository (DESIGN §18.1), and the repo row can move
	// after a card has been chosen here. Refusing is the answer rather
	// than quietly dropping the choice: the store refuses it too
	// (ErrStackRepoMismatch), and that refusal arrives after the card has
	// already been created, as a warning about a stack that did not
	// happen.
	if c, ok := d.stackCandFor(d.stackOnto); ok && c.Repo != d.formRepo() {
		d.errText = string(c.ID) + " is in " + stackRepoLabel(c.Repo) + " — a stack is one repository"
		d.setFocus(cardStopStack)
		return false, nil
	}
	var env *int
	trimmed := strings.TrimSpace(d.env.Value())
	if trimmed == "" && d.ct.Kind == domain.KindGoal {
		d.errText = "budget required — a goal's budget is the ceiling for everything it runs"
		return false, nil
	}
	if trimmed == "" && d.ct.Kind == domain.KindResearch {
		// "budget", like the row label and the collapsed "runs as"
		// readout above it. A refusal is the one string in this dialog a
		// reader is guaranteed to stop and read, so it is the last place
		// that should still be calling a spend cap an envelope.
		d.errText = "budget required — a research card carries no default"
		return false, nil
	}
	if trimmed != "" {
		n, err := strconv.Atoi(trimmed)
		if err != nil || n < 0 {
			d.errText = "budget must be a non-negative number of credits"
			return false, nil
		}
		env = &n
	}
	res := formResult{
		Kind: d.ct.Kind, Mode: d.ct.Mode, Desc: desc, Profile: d.profiles[d.profile], Envelope: env,
		Repo: d.formRepo(), Source: "manual", After: append([]domain.FeatureID(nil), d.after...),
		Base: d.base, Adopt: d.adopt, StackOnto: d.stackOnto, StackInto: d.stackInto,
		Start: start, FromPicker: d.fromPicker,
	}
	if d.ct.Kind == domain.KindBug {
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
// budget when those are.
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

// A row of options is a horizontal list, and its length is the
// workspace's to decide: `repos:` names, profiles.yaml presets. On one
// line that list set the dialog's own width — a frame is as wide as its
// widest line no matter how wide the terminal is — so a workspace with a
// handful of repositories opened a new-card dialog wider than the
// screen, and the overlay clipped whatever hung off the edge (including,
// past a certain count, the options themselves and the box's right
// border). Rows that can grow that way fold instead (choiceRowLines):
// as many lines as they need, up to choiceMaxLines, then windowed
// around the selection.
const (
	// choiceSep is the gap between two options laid out side by side.
	choiceSep = "    "
	// choiceMaxLines caps how tall one folded row may get before it
	// windows instead of growing further. Three lines holds a dozen-odd
	// names at a usual dialog width and still leaves the description box
	// its own rows in a 24-row terminal; past that the row scrolls rather
	// than pushing the box under the fold. A short terminal tightens the
	// budget further — see cardForm.View.
	choiceMaxLines = 3
	// choiceMinWidth is the narrowest budget a folded row's options ever
	// get. A dialog drawn into a terminal narrower than its own floor
	// would otherwise hand the row a negative width and fold every option
	// onto a line of its own.
	choiceMinWidth = 16
)

// choiceCells renders each option as its own cell, in cycle order, with
// the unset chip ahead of them while nothing is chosen.
func choiceCells(s *theme.Styles, focused bool, opts []string, idx int, unsetLabel string, numbered bool) []string {
	cells := make([]string, 0, len(opts)+1)
	if idx < 0 {
		if focused {
			cells = append(cells, s.Band(" "+unsetLabel+" ", 0, true))
		} else {
			cells = append(cells, s.Error.Render(unsetLabel))
		}
	}
	for i, o := range opts {
		num := ""
		if numbered && i < 9 {
			num = s.Faint.Render(strconv.Itoa(i+1)) + " "
		}
		switch {
		case i == idx && focused:
			cells = append(cells, num+s.Band(s.BandMarker(true)+s.BandText.Render(o)+" ", 0, true))
		case i == idx:
			cells = append(cells, num+s.KeyHint.Render("▸ ")+s.Base.Render(o))
		default:
			cells = append(cells, num+s.Faint.Render(o))
		}
	}
	return cells
}

// packChoices folds cells into lines at most width columns wide, and
// reports the line each cell landed on. A cell wider than the whole
// budget is truncated rather than allowed to set the width.
func packChoices(cells []string, width int) (lines []string, lineOf []int) {
	if width < 1 {
		width = 1
	}
	lineOf = make([]int, len(cells))
	cur, curW := "", 0
	for i, c := range cells {
		cw := ansi.StringWidth(c)
		if cw > width {
			c = ansi.Truncate(c, width, "…")
			cw = ansi.StringWidth(c)
		}
		switch {
		case cur == "":
			cur, curW = c, cw
		case curW+len(choiceSep)+cw <= width:
			cur += choiceSep + c
			curW += len(choiceSep) + cw
		default:
			lines = append(lines, cur)
			cur, curW = c, cw
		}
		lineOf[i] = len(lines)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines, lineOf
}

// foldChoices lays cells out over at most maxLines lines of width
// columns. When they don't all fit it keeps the window around the cell
// at sel — the selected option is the one that must always be on screen
// — and tails the last line with how many the window leaves off, so a
// short row never reads as the whole list.
func foldChoices(s *theme.Styles, cells []string, sel, width, maxLines int) []string {
	if len(cells) == 0 {
		return nil
	}
	lines, lineOf := packChoices(cells, width)
	if len(lines) <= maxLines {
		return lines
	}
	// the tail's room has to come out of the cells' budget, so pack again
	// against the narrower width before choosing the window.
	lines, lineOf = packChoices(cells, width-ansi.StringWidth(choiceTail(s, len(cells))))
	if len(lines) <= maxLines {
		return lines
	}
	start := clamp(lineOf[clamp(sel, 0, len(cells)-1)]-maxLines/2, 0, len(lines)-maxLines)
	hidden := 0
	for _, l := range lineOf {
		if l < start || l >= start+maxLines {
			hidden++
		}
	}
	out := append([]string(nil), lines[start:start+maxLines]...)
	if hidden > 0 {
		out[len(out)-1] += choiceTail(s, hidden)
	}
	return out
}

// choiceTail names what a windowed row is not showing.
func choiceTail(s *theme.Styles, n int) string {
	return s.Faint.Render(fmt.Sprintf("  +%d", n))
}

// choiceRowLines is choiceRow for a row whose options may not fit beside
// each other in width columns: the row folds, continuation lines
// indented under the first option so the label still reads as one row's.
func choiceRowLines(s *theme.Styles, focused bool, label string, opts []string, idx int, unsetLabel string, numbered bool, width, maxLines int) []string {
	return foldedRow(s, cardLabel(s, label), cardLabelW+2, choiceCells(s, focused, opts, idx, unsetLabel, numbered), idx, width, maxLines)
}

// foldedRow prefixes a folded option row with its already-rendered
// label, indenting the continuation lines by the label cell's width
// (indent, which differs between the top-level rows and the expanded
// option rows under "runs as").
func foldedRow(s *theme.Styles, prefix string, indent int, cells []string, sel, width, maxLines int) []string {
	lines := foldChoices(s, cells, sel, max(width-indent, choiceMinWidth), maxLines)
	if len(lines) == 0 {
		return []string{prefix}
	}
	rows := make([]string, len(lines))
	for i, ln := range lines {
		if i == 0 {
			rows[i] = prefix + ln
			continue
		}
		rows[i] = strings.Repeat(" ", indent) + ln
	}
	return rows
}

// foldReadout folds an already-labelled readout row — `from`, `becomes`,
// `runs as` — to width, indenting its continuation lines under the label
// so the row still reads as one. It wraps rather than truncates: the
// `becomes` line carries the derived title with DeriveTitle's own
// ellipsis where a long first line was cut to maxTitleLen, and that cut
// is the whole reason the title is shown there at all — a second,
// width-driven ellipsis on top of it would hide exactly what the line
// exists to say.
func foldReadout(line string, indent, width int) []string {
	if width < 1 || ansi.StringWidth(line) <= width {
		return []string{line}
	}
	rows := strings.Split(ansi.Wrap(line, max(width-indent, choiceMinWidth), " -·"), "\n")
	for i := 1; i < len(rows); i++ {
		rows[i] = strings.Repeat(" ", indent) + rows[i]
	}
	return rows
}

// wrapHint folds a key line to width. The hints are " · "-separated
// lists of "key does thing", and the text-row hint alone is over a
// hundred columns — on an 80-column terminal it, not the form, was what
// set the dialog's width, and the frame ran off both edges of the
// screen whatever the repo row did. Breaks land only on a separator, so
// a key never ends up on a different line from what it does.
func wrapHint(hint string, width int) []string {
	const sep = " · "
	if width < 1 || ansi.StringWidth(hint) <= width {
		return []string{hint}
	}
	var lines []string
	cur := ""
	for _, part := range strings.Split(hint, sep) {
		switch {
		case cur == "":
			cur = part
		case ansi.StringWidth(cur)+ansi.StringWidth(sep)+ansi.StringWidth(part) <= width:
			cur += sep + part
		default:
			lines = append(lines, cur)
			cur = part
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// optionLabelW is optionLabel's rendered width: the four-column marker
// margin plus the eleven-column label cell. It is what a folded option
// row indents its continuation lines by.
const optionLabelW = 4 + 11

// optionLabel is an expanded option row's label cell: indented under
// "runs as", banded when its row has focus — and, since the band alone
// used to be the only tell, carrying a leading ▸ too. The rows here
// (budget, profile, severity, runs after) each show their own value with
// choices()' own "▸" beside whichever option is picked, on every row,
// focused or not — that answers "what is this row set to", never "which
// row is tab on right now". A reader driving the dialog with no working
// color (or who just can't tell a faint cell from a banded one at a
// glance) needs a mark that only ever appears on the one row focus is
// actually on; the label's own margin is that mark, and it costs no
// width — "  ▸ " and "    " are both four columns.
func optionLabel(s *theme.Styles, focused bool, label string) string {
	// 11, not 10: "runs after" (the old "after") is itself 10 characters,
	// and a %-10s cell would then butt the label straight up against the
	// row's value with no gap at all ("runs after—   tab to..."). Every
	// other label here is shorter than 10, so the extra column only ever
	// shows up as one more space of padding for them.
	cell := fmt.Sprintf("%-11s", label)
	if focused {
		return "  ▸ " + s.Band(cell, 0, true)
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

// becomesLine reads back the card the text would create: the id prefix
// (with the kind word beside it, so FD/BG/RS are spelled out at least
// once, right where the id is minted), the derived title, and a bug's
// severity. No number — that is minted at Create.
//
// This used to show the branch slug instead of the title, which hid a
// live truncation: DeriveTitle caps a title at maxTitleLen characters
// (internal/domain/feature.go), keeping the untruncated text in the
// card's OneLiner — but the truncated title is what the board, the card
// header, the artifact's H1 and every notice actually show, and the
// slug is derived from that same truncated title anyway. Showing the
// title instead means the cut is visible here, before Create, exactly
// where a person can still do something about it: DeriveTitle already
// appends an ellipsis when it cuts, so this line needs no truncation
// logic of its own. The slug stays the least interesting half of the
// line — worth confirming (via Slugify) that Create would accept it,
// not worth a reader's attention — so it is no longer drawn at all.
func (d *cardForm) becomesLine(s *theme.Styles) string {
	desc := strings.TrimSpace(d.text.Value())
	if desc == "" {
		return cardLabel(s, "becomes") + s.Faint.Render("—")
	}
	if _, isRef := d.reference(); isRef && d.imported == nil {
		return cardLabel(s, "becomes") + s.Faint.Render("— import the reference first, or describe the card")
	}
	title, _, _ := domain.SplitFreeform(desc)
	if _, err := domain.Slugify(title); err != nil {
		return cardLabel(s, "becomes") + s.Error.Render("the first line needs a letter or digit to make a title")
	}
	out := d.ct.Prefix() + " (" + d.ct.Name() + ") · " + title
	// Name where the branch comes from whenever it is not the plain
	// default. This is the line that keeps the S gesture honest: the
	// stack row itself is folded away in the options, so this readout is
	// the reader's only chance to see what the card will fork from
	// before pressing enter.
	if from := d.forkFrom(); from != "" {
		out += s.Faint.Render(" · from ") + from
	}
	if d.ct.Kind == domain.KindBug && bugSeverityChoices[d.sev] != "" {
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
	case env == "" && (d.ct.Kind == domain.KindResearch || d.ct.Kind == domain.KindGoal):
		env = s.Error.Render("budget required")
	case env == "":
		env = "default budget"
	default:
		env += " credits"
	}
	out := env + " · " + d.profiles[d.profile]
	if len(d.after) > 0 {
		out += " · runs after " + joinIDs(d.after)
	}
	if st := d.stackSummary(); st != "" {
		out += " · " + st
	}
	if d.base != "" {
		out += " · on " + d.base
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

// optionRows are the expanded options: budget, profile, severity (bugs),
// runs after — and the after list while that row has focus.
func (d *cardForm) optionRows(s *theme.Styles, width, maxLines int) []string {
	hint := envelopeHintCapped
	if d.ct.Kind == domain.KindResearch || d.ct.Kind == domain.KindGoal {
		hint = envelopeHintRequired
	}
	rows := []string{
		cardLabel(s, "runs as"),
		optionLabel(s, d.focus == cardStopEnvelope, "budget") + d.env.View() + " " + s.Faint.Render(hint),
	}
	// profiles.yaml sets how many presets there are, so the profile row
	// folds the same way the repo row does rather than setting the
	// dialog's width from the config file.
	rows = append(rows, foldedRow(s, optionLabel(s, d.focus == cardStopProfile, "profile"), optionLabelW,
		choiceCells(s, d.focus == cardStopProfile, d.profiles, d.profile, "", false), d.profile, width, maxLines)...)
	if d.ct.Kind == domain.KindBug {
		labels := make([]string, len(bugSeverityChoices))
		for i, sev := range bugSeverityChoices {
			labels[i] = string(sev)
			if sev == "" {
				labels[i] = "unset"
			}
		}
		rows = append(rows, foldedRow(s, optionLabel(s, d.focus == cardStopSeverity, "severity"), optionLabelW,
			choiceCells(s, d.focus == cardStopSeverity, labels, d.sev, "", false), d.sev, width, maxLines)...)
	}
	chips := make([]string, len(d.after))
	for i, id := range d.after {
		chips[i] = s.KeyHint.Render(string(id))
	}
	// "after" read as an unlabelled dash to anyone who hadn't already
	// tabbed onto it and found the dependency picker underneath — it
	// named neither what the row was nor what filling it in would do.
	// "runs after" names the relationship on sight; the collapsed hint
	// spells out the effect rather than just the key.
	line := optionLabel(s, d.focus == cardStopAfter, "runs after") + strings.Join(chips, " ")
	switch {
	case d.focus == cardStopAfter:
		if len(chips) > 0 {
			line += "  "
		}
		line += s.KeyHint.Render("/ ") + d.afterFilter.View()
	case len(chips) == 0:
		line += s.Faint.Render("—   tab to wait on another card")
	}
	rows = append(rows, line)
	if d.focus == cardStopAfter {
		rows = append(rows, d.afterListRows(s, width)...)
	}
	// The base row appears only when there is a choice to make: a repo
	// with one branch has nothing to offer, and an unanswerable row is
	// a tab stop that teaches nothing.
	if len(d.baseCands) > 1 {
		rows = append(rows, foldedRow(s, optionLabel(s, d.focus == cardStopBase, "forks from"), optionLabelW,
			choiceCells(s, d.focus == cardStopBase, d.baseChoices(), d.baseIdx(), "", false),
			d.baseIdx(), width, maxLines)...)
	}
	if len(d.baseCands) > 0 && d.ct.Kind != domain.KindResearch {
		rows = append(rows, foldedRow(s, optionLabel(s, d.focus == cardStopAdopt, "works on"), optionLabelW,
			choiceCells(s, d.focus == cardStopAdopt, d.adoptChoices(), d.adoptIdx(), "", false),
			d.adoptIdx(), width, maxLines)...)
	}
	if d.asksStack() {
		label := optionLabel(s, d.focus == cardStopStack, "stack")
		if d.stackInto != "" {
			// Nothing in the TUI sets this; a form carrying one came from
			// somewhere that did, and the row says so rather than
			// rendering a cycle its value is not in.
			rows = append(rows, label+"into "+s.KeyHint.Render(string(d.stackInto)))
			return rows
		}
		rows = append(rows, foldedRow(s, label, optionLabelW,
			choiceCells(s, d.focus == cardStopStack, d.stackChoices(), d.stackIdx(), "", false),
			d.stackIdx(), width, maxLines)...)
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

// repoRows is the repo row: the configured names folded to the dialog's
// width, with the chosen repository's origin read back at the end of the
// last line — or on a line of its own when what is left there is too
// narrow to hold it. The readout used to be appended to the row
// regardless, which pushed the dialog past the width it had just been
// padded to fit.
func (d *cardForm) repoRows(s *theme.Styles, width, maxLines int) []string {
	rows := choiceRowLines(s, d.focus == cardStopRepo, "repo", d.repo.options(), d.repo.idx, repoUnsetLabel, d.focus == cardStopRepo, width, maxLines)
	if d.originFor == nil || !d.repo.chosen() {
		return rows
	}
	origin := s.Faint.Render(ansi.Truncate(d.origin().label(), width, "…"))
	last := len(rows) - 1
	if pad := width - ansi.StringWidth(rows[last]) - ansi.StringWidth(origin); pad > 2 {
		rows[last] += strings.Repeat(" ", pad) + origin
		return rows
	}
	return append(rows, strings.Repeat(" ", max(width-ansi.StringWidth(origin), 0))+origin)
}

// View implements overlay.Dialog.
//
// Every row is built before any of it is written, because none of the
// dialog's parts has a fixed height any more: a repo row folds to as
// many lines as the workspace's `repos:` list needs, a readout wraps
// rather than running off the edge, and the key hint is longer than an
// 80-column terminal on its own. A frame is as wide and as tall as its
// widest and tallest content, so a row that overflows doesn't get
// clipped neatly — it drags the whole dialog off the screen, and the
// overlay centres what's left. Everything is folded to textW first and
// counted, so the box gets exactly the rows that are left.
func (d *cardForm) View(s *theme.Styles, w, h int) string {
	// dialogDescSize's width depends only on the terminal's, never on the
	// row count, so textW is final before a single row is laid out.
	textW, _ := dialogDescSize(w, h, 0)

	// Folding trades width for height, and a short terminal has none to
	// spare — so the layout is tried at a full budget first and gives
	// things up, in the order a reader can most afford to lose them,
	// until the box can still have its floor: first the option rows'
	// extra lines (they window around the selection instead), then the
	// key hint (the one row that only repeats what tab and the arrows
	// already say), then the blank lines between the blocks.
	var rows []string
	var textAt, static int
	for _, try := range []struct {
		fold          int
		hint, spacers bool
	}{
		{choiceMaxLines, true, true},
		{2, true, true},
		{1, true, true},
		{1, false, true},
		{1, false, false},
	} {
		rows, textAt, static = d.layout(s, textW, h, try.fold, try.hint, try.spacers)
		if dialogFrameChromeH+static+descHeightMin <= h-dialogStatusBarMargin {
			break
		}
	}

	_, textH := dialogDescSize(w, h, static)
	d.text.SetWidth(textW)
	d.text.SetHeight(textH)
	rows[textAt] = d.text.View()
	return s.DialogFrame.Render(strings.Join(rows, "\n"))
}

// layout builds every row of the dialog for a content width of textW, a
// draw height of h, at most fold lines per option row, and the hint and
// the blank separators only when asked for. It returns the rows (with
// the text box's own slot left empty), that slot's index, and the row
// count everything but the box takes: title+blank(2), kind, repo?,
// from?, blank, the box, blank, becomes, runs (one readout or the
// expanded options), blank+buttons, blank+error?, blank+hint?.
func (d *cardForm) layout(s *theme.Styles, textW, h, fold int, hint, spacers bool) (rows []string, textAt, static int) {
	gap := func() []string {
		if spacers {
			return []string{""}
		}
		return nil
	}
	kinds := make([]string, len(domain.CardTypes))
	kindIdx := 0
	for i, c := range domain.CardTypes {
		kinds[i] = c.Name()
		if c == d.ct {
			kindIdx = i
		}
	}

	rows = append(rows, s.DialogTitle.Render("new card"), "")
	rows = append(rows, choiceRowLines(s, d.focus == cardStopKind, "kind", kinds, kindIdx, "", false, textW, fold)...)
	if d.repo.shown() && d.ct.Kind != domain.KindGoal {
		rows = append(rows, d.repoRows(s, textW, fold)...)
	}
	if from := d.fromLine(s); from != "" {
		rows = append(rows, foldReadout(from, cardLabelW+2, textW)...)
	}

	// the box always keeps its own blank line above: it is what separates
	// the fields from the prose, and the two run together without it.
	rows = append(rows, "", "")
	textAt = len(rows) - 1
	rows = append(rows, gap()...)

	rows = append(rows, foldReadout(d.becomesLine(s), cardLabelW+2, textW)...)
	if d.expanded {
		rows = append(rows, d.optionRows(s, textW, fold)...)
	} else {
		rows = append(rows, foldReadout(d.runsLine(s), cardLabelW+2, textW)...)
	}
	rows = append(rows, gap()...)
	rows = append(rows, strings.Split(d.buttons.ViewWidth(s, d.focus == cardStopButtons, textW), "\n")...)
	if d.errText != "" {
		rows = append(rows, gap()...)
		for _, row := range strings.Split(ansi.Wrap(d.errText, textW, " -"), "\n") {
			rows = append(rows, s.Error.Render(row))
		}
	}
	// a terminal too short for the hint never had room for it: the rule
	// predates the folding and is why h reaches this far down.
	if hint && h >= 24 {
		rows = append(rows, gap()...)
		for _, row := range wrapHint(d.hint(), textW) {
			rows = append(rows, s.Faint.Render(row))
		}
	}
	return rows, textAt, len(rows) - 1
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
		// shift+tab is named here, not just tab. The composer is where focus
		// starts and `kind` is the row directly ABOVE it — one shift+tab
		// away and five tabs the other way round the cycle. A reader who
		// types a bug description, sees "becomes  FD (feature) · …" and
		// wants to fix it was being shown only the long way (round 3 §6).
		return "tab/shift+tab rows · " + g + " · alt+o options · alt+enter newline · enter create · esc cancel"
	case cardStopEnvelope:
		return "type a number of credits · alt+o collapse · tab next · esc cancel"
	case cardStopProfile:
		return "←/→ choose the profile · alt+o collapse · tab next · esc cancel"
	case cardStopSeverity:
		return "←/→ choose the severity · alt+o collapse · tab next · esc cancel"
	case cardStopAfter:
		return "type to filter · ↑/↓ move · enter add · backspace remove last · alt+o collapse · tab next · esc cancel"
	case cardStopBase:
		return "←/→ choose the branch it forks from · alt+o collapse · tab next · esc cancel"
	case cardStopAdopt:
		return "←/→ work on an existing branch instead of cutting one · alt+o collapse · tab next · esc cancel"
	case cardStopStack:
		// The cells are bare ids, so the title of the chosen card is
		// named here rather than on the row: the hint line is the one
		// place with room for it at 60 columns.
		if d.stackLabel != "" {
			return "on top of " + d.stackLabel + " · ←/→ choose the card below · alt+o collapse · tab next · esc cancel"
		}
		return "←/→ choose a card to stack on, or leave it standalone · alt+o collapse · tab next · esc cancel"
	case cardStopButtons:
		return "←/→ buttons · enter activate · tab next · esc cancel"
	case cardStopRuns:
		// stops() offers this as the collapsed "runs as" line's tab stop,
		// but advanceFocus always converts landing here into
		// cardStopEnvelope or cardStopAfter (expanding the row instead of
		// focusing it bare) before setFocus runs, so d.focus is never
		// actually this value. Named anyway so the switch has no silent
		// gap if that ever stops being true.
		return "tab expands the run options · esc cancel"
	default:
		return "tab rows · esc cancel"
	}
}

// sortAfterCands orders candidates by id for a stable list.
func sortAfterCands(c []afterCand) {
	sort.Slice(c, func(i, j int) bool { return c[i].ID < c[j].ID })
}

// --- base and stack ------------------------------------------------------

// setBaseCands installs the branches the chosen repo has, with cur (the
// branch it currently has out) first so the row opens on the default.
// A repo with one branch offers no choice and the row is not a stop.
func (d *cardForm) setBaseCands(branches []string, cur string) {
	d.baseCands = branches
	d.baseCursor = 0
	for i, b := range branches {
		if b == cur {
			d.baseCursor = i
			break
		}
	}
	// The default is "whatever the checkout has out", which is stored as
	// an empty base rather than as the branch's name: a card that named
	// the branch explicitly would keep forking from it after the reader
	// moved the checkout on, which is not what "the default" means.
	d.base = ""
}

// stackOn presets the form to stack the new card on top of onto. Called
// by the T gesture; the label is what the row and the readouts show.
func (d *cardForm) stackOn(onto domain.FeatureID, label string, into domain.StackID) {
	d.stackOnto, d.stackLabel, d.stackInto = onto, label, into
	d.stackOffer = onto
}

// setStackCands installs the cards the stack row may fork from, in the
// order the row offers them. The sort lives here rather than at the
// caller so the row's order is the row's own business.
func (d *cardForm) setStackCands(c []stackCand) {
	sortStackCands(c)
	d.stackCands = c
}

// asksStack is whether the stack row is worth a tab stop: a research
// card has no branch of its own to fork, and a goal's cards share the
// goal's branch rather than stacking (DESIGN §18.5).
func (d *cardForm) asksStack() bool {
	return d.ct.Kind != domain.KindResearch && d.ct.Kind != domain.KindGoal
}

// stackVisible is the row's cycle: the chosen repository's cards, since
// a stack is one repository, plus whatever the row currently holds even
// when the repo row has since moved past it. Keeping the held card in
// the list is what lets the reader see and undo the mismatch the submit
// refuses, rather than being refused over a card the row stopped
// showing.
func (d *cardForm) stackVisible() []stackCand {
	held := d.stackOnto
	if held == "" {
		held = d.stackOffer
	}
	out := make([]stackCand, 0, len(d.stackCands))
	var carried bool
	for _, c := range d.stackCands {
		if c.ID == held {
			carried = true
		} else if d.repo.needsChoice() || c.Repo != d.repo.name() {
			// Until the repo row is answered there is no repository to
			// be one of, so the row offers nothing rather than offering
			// the unconfigured default's cards under another name.
			continue
		}
		out = append(out, c)
	}
	if held != "" && !carried {
		// T named a card the board's rows no longer carry, or a test
		// scaffold set one with no candidates at all: the offer is still
		// the row's answer and has to be reachable.
		out = append([]stackCand{{ID: held, Title: d.stackLabel, Repo: d.repo.name()}}, out...)
	}
	return out
}

// stackChoices is the cycle's cells: standing alone reads first, because
// it is the default and what the overwhelming majority of cards do.
func (d *cardForm) stackChoices() []string {
	vis := d.stackVisible()
	out := make([]string, 0, len(vis)+1)
	out = append(out, "standalone")
	for _, c := range vis {
		out = append(out, "on "+string(c.ID))
	}
	return out
}

// stackIdx is the selected cell of stackChoices.
func (d *cardForm) stackIdx() int {
	if d.stackOnto == "" {
		return 0
	}
	for i, c := range d.stackVisible() {
		if c.ID == d.stackOnto {
			return i + 1
		}
	}
	return 0
}

// stackCandFor finds a candidate by id, for the title the row and the
// refusal show.
func (d *cardForm) stackCandFor(id domain.FeatureID) (stackCand, bool) {
	for _, c := range d.stackCands {
		if c.ID == id {
			return c, true
		}
	}
	return stackCand{}, false
}

// stackRepoLabel names a repository for the row's refusal. The empty
// name is the workspace default, which has no name to print.
func stackRepoLabel(repo string) string {
	if repo == "" {
		return "the default repository"
	}
	return repo
}

// sortStackCands orders candidates most recently touched first, so the
// card the reader was just looking at is the first one the row offers
// however many the board holds. Ties fall back to the id, which only
// matters for the rows a fresh workspace writes in one tick.
func sortStackCands(c []stackCand) {
	sort.Slice(c, func(i, j int) bool {
		if !c[i].Touched.Equal(c[j].Touched) {
			return c[i].Touched.After(c[j].Touched)
		}
		return c[i].ID < c[j].ID
	})
}

// forkFrom is the phrase the `becomes` readout uses for where this
// card's branch comes from, empty when it is the plain default (the
// checkout's HEAD, unstacked) and there is nothing to say.
func (d *cardForm) forkFrom() string {
	if d.stackOnto != "" {
		return string(d.stackOnto) + "'s branch"
	}
	if d.stackInto != "" {
		return "the top of " + string(d.stackInto)
	}
	if d.base != "" {
		return d.base
	}
	return ""
}

// stackSummary is the stack half of the collapsed options readout.
func (d *cardForm) stackSummary() string {
	switch {
	case d.stackOnto != "":
		return "on top of " + string(d.stackOnto)
	case d.stackInto != "":
		return "into " + string(d.stackInto)
	}
	return ""
}

// baseChoices are the row's cells: the default first, then each branch.
func (d *cardForm) baseChoices() []string {
	out := make([]string, 0, len(d.baseCands)+1)
	out = append(out, "checked-out")
	return append(out, d.baseCands...)
}

// baseIdx is the selected cell of baseChoices.
func (d *cardForm) baseIdx() int {
	if d.base == "" {
		return 0
	}
	for i, b := range d.baseCands {
		if b == d.base {
			return i + 1
		}
	}
	return 0
}

// adoptChoices is the adopt row's cells: cutting a fresh branch, which is
// the default and reads first, then every branch that could be adopted
// instead.
func (d *cardForm) adoptChoices() []string {
	out := make([]string, 0, len(d.baseCands)+1)
	out = append(out, "a new branch")
	return append(out, d.baseCands...)
}

// adoptIdx is the selected cell of adoptChoices.
func (d *cardForm) adoptIdx() int {
	if d.adopt == "" {
		return 0
	}
	for i, b := range d.baseCands {
		if b == d.adopt {
			return i + 1
		}
	}
	return 0
}

// cycleAdopt moves the adopt row by dir over adoptChoices.
func (d *cardForm) cycleAdopt(dir int) {
	choices := d.adoptChoices()
	i := (d.adoptIdx() + dir) % len(choices)
	if i < 0 {
		i += len(choices)
	}
	if i == 0 {
		d.adopt = ""
		return
	}
	d.adopt = choices[i]
}

// cycleBase moves the base row by dir over baseChoices.
func (d *cardForm) cycleBase(dir int) {
	choices := d.baseChoices()
	i := (d.baseIdx()+dir)%len(choices) + 0
	if i < 0 {
		i += len(choices)
	}
	if i == 0 {
		d.base = ""
		return
	}
	d.base = choices[i]
}

// cycleStack moves the stack row by dir over stackChoices: standing
// alone, or on top of one of the cards on offer.
//
// Joining a whole stack (stackInto) is not part of the cycle — nothing
// in the TUI sets it, and `gummi stack add` is where that lives — so a
// form holding one steps off it to standalone and then cycles cards
// like any other.
func (d *cardForm) cycleStack(dir int) {
	if d.stackInto != "" {
		d.stackOnto, d.stackInto, d.stackLabel = "", "", ""
		return
	}
	vis := d.stackVisible()
	if len(vis) == 0 {
		return // no card on the board this one could fork from
	}
	n := len(vis) + 1
	i := ((d.stackIdx()+dir)%n + n) % n
	if i == 0 {
		d.stackOnto, d.stackLabel = "", ""
		return
	}
	c := vis[i-1]
	d.stackOnto, d.stackLabel = c.ID, c.Title
}
