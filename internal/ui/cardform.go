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

// cardForm is the model of a new session: what it asks, where it runs,
// what it forks from, its budget and profile, and the one set of rules
// that refuses or creates it (submit). It draws nothing and reads no
// keys: the terminal's draft in the board pane (sessiondraft.go) and the
// web page's create (webcreate.go's webFill) both drive it, so the two
// faces cannot disagree about what a new session may be.
//
// Rule 0: it never acts on its own. A pasted issue reference is an offer;
// alt+g accepts it (github); enter only ever creates. Labels an import
// brings back are reported, never applied.
type cardForm struct {
	// sessionBackend and sessionModel are the agent and model a freeform
	// card's session will run on. The form has no row for them — the web
	// face's session draft picks them in its composer and webFill sets
	// them — so a card made in the terminal leaves both empty and runs on
	// its profile's implementer, as it always did.
	sessionBackend, sessionModel string

	repo repoPicker
	// origins caches each repository's parsed origin, resolved through
	// originFor the first time its row is chosen. nil originFor (a test
	// scaffold, or a shell with no pool) renders no readout at all.
	origins   map[string]repoOrigin
	originFor func(repo string) repoOrigin

	// name is the session's title when the person gives it one before it
	// starts; empty, the text's first line is the title.
	name textinput.Model
	text textarea.Model
	env  textinput.Model

	profiles []string
	profile  int

	// base is the branch the card's work forks from, and baseCands the
	// branches the chosen repo actually has. An empty base means "what
	// the checkout has out", which is the default and what every card
	// did before bases were selectable.
	base       string
	baseCands  []string
	baseCursor int
	// checkedOut and branchesOf are the board's per-repo branch facts
	// (setRepoData), kept so a repo picked by hand offers its own branches.
	checkedOut map[string]string
	branchesOf map[string][]string
	// stackOnto names the card this one is being stacked on top of, and
	// stackCands the cards the draft's "from" setting can name. stackInto
	// names an existing stack to join instead. All empty is a standalone
	// card. T presets stackOnto; the setting can change it after.
	stackOnto  domain.FeatureID
	stackLabel string
	stackInto  domain.StackID
	stackCands []stackCand
	// stackOffer remembers the card T named, so it stays on the row even
	// when the candidate list would not otherwise carry it.
	stackOffer domain.FeatureID

	// mainCheckout is the freeform kind's "runs in" choice: the session
	// works in the repository's main checkout instead of its own worktree
	// — no branch cut, no worktree created, nothing isolated. Selecting
	// it clears the fork-from and stack rows, which name a branch such a
	// session does not have; moving the kind row off freeform clears it,
	// because the row that sets it goes with the kind.
	mainCheckout bool
	// home is the row's third cell: an agent, working in a home of its
	// own (domain.Feature.Home). It is set with mainCheckout, never
	// without it — the home is the checkout the agent runs in — so every
	// rule above about a session with no branch holds for it unasked.
	home bool

	// imported is the issue whose title and body the box currently holds,
	// once alt+g has accepted an offer; fetching marks the round trip.
	imported *importedIssue
	fetching bool

	// focus names the setting a refusal is about (a cardStop), so the
	// draft can move its own cursor there (sessionDraft.syncFocus)
	focus   int
	errText string

	onSubmit func(formResult) tea.Cmd
	// onImport fetches ref (never bare: the caller resolved it against the
	// chosen repo's origin) and delivers the result back through
	// applyIssue.
	onImport func(ref domain.IssueRef, repo string) tea.Cmd
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

// The settings a refusal can name (cardForm.focus), so a face can move
// its cursor to the one that needs changing.
const (
	cardStopRepo = iota
	cardStopName
	cardStopText
	cardStopEnvelope
	cardStopBase
	cardStopStack
)

// newCardForm builds the model of a new session (an agent is the
// session's "where" choice). repos and hasDefault shape the repository
// setting as in every other creation dialog;
// lastRepo preselects the repo chosen last time this session (a name
// that is not configured is ignored); defaultEnvelope prefills the budget.
func newCardForm(profiles, repos []string, hasDefault bool, lastRepo string, defaultEnvelope int, onSubmit func(formResult) tea.Cmd) *cardForm {
	if len(profiles) == 0 {
		profiles = defaultProfilePresets
	}
	text := textarea.New()
	text.CharLimit = threadInputCharLimit
	text.ShowLineNumbers = false
	text.SetWidth(descWidthMin)
	text.SetHeight(descHeightMin)
	name := textinput.New()
	name.Prompt = ""
	name.CharLimit = 200
	name.Placeholder = "optional — the first line of the text otherwise"
	env := textinput.New()
	env.SetWidth(12)
	env.CharLimit = 12
	env.SetValue(domain.DollarsInput(defaultEnvelope))

	repo := newRepoPicker(repos, hasDefault)
	for i, name := range repo.options() {
		if lastRepo != "" && name == lastRepo && repo.multi() {
			repo.idx = i
		}
	}
	d := &cardForm{
		repo: repo, origins: map[string]repoOrigin{},
		name: name, text: text, env: env, profiles: profiles,
		onSubmit: onSubmit,
	}
	// the repository is the one field with no default, so a form that
	// still needs one opens on it; everything else opens in the text.
	if d.asksRepo() && d.repo.needsChoice() {
		d.focus = cardStopRepo
	} else {
		d.focus = cardStopText
	}
	d.text.Placeholder = cardPlaceholder
	return d
}

// cardPlaceholder is what the web's create offers a session as: say
// what you want done.
const cardPlaceholder = "Say what you want done. The first line is the title, unless you name it."

// Text is the box's current content.
func (d *cardForm) Text() string { return d.text.Value() }

// asksRepo reports whether the dialog has a repository to ask for: only
// in a workspace with more than one.
func (d *cardForm) asksRepo() bool {
	// an agent is in no repository, so a workspace of several asks none
	return d.repo.multi() && !d.home
}

// formRepo is the repository the card is created in: the chosen one.
func (d *cardForm) formRepo() string {
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

// github is alt+g: refetch an import or import the offered reference,
// whichever the form's state calls for; with no reference in the box it
// does nothing. A bare reference with no repository chosen moves focus
// to the repo row and says so.
func (d *cardForm) github() tea.Cmd {
	if d.fetching {
		return nil
	}
	if d.imported != nil {
		if d.onImport == nil {
			return nil
		}
		if !strings.Contains(d.text.Value(), d.imported.text) {
			d.errText = "the imported text was edited — clear it to fetch the issue again"
			return nil
		}
		d.fetching = true
		return d.onImport(d.imported.ref, d.repo.name())
	}
	ref, ok := d.reference()
	if !ok {
		return nil
	}
	if ref.Bare() {
		if d.repo.needsChoice() {
			d.errText = fmt.Sprintf("choose a repository to resolve %s", ref)
			d.focus = cardStopRepo
			return nil
		}
		o := d.origin()
		if !o.github() {
			d.errText = fmt.Sprintf("%s has no GitHub origin to resolve %s against — paste the issue's link instead", d.repo.label(), ref)
			return nil
		}
		owner, repo, _ := strings.Cut(o.ownerRepo, "/")
		ref.Owner, ref.Repo = owner, repo
	}
	if d.onImport == nil {
		return nil
	}
	d.fetching = true
	d.errText = ""
	return d.onImport(ref, d.repo.name())
}

// applyIssue is the import's landing: the reference line in the box is
// replaced by the issue's title and body — and only that line; anything
// else the person typed stays — and the `from` line turns from offer to
// provenance. The issue's labels are reported beside the reference. A
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
}

// failImport is the import's other landing.
func (d *cardForm) failImport(err error) {
	d.fetching = false
	d.errText = "import: " + sanitize(err.Error())
}

// submit validates and fires onSubmit.
func (d *cardForm) submit() (bool, tea.Cmd) {
	if d.asksRepo() && d.repo.needsChoice() {
		d.errText = repoUnchosenErr
		d.focus = cardStopRepo
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
	if _, err := domain.Slugify(d.title()); err != nil {
		d.errText = "the first line needs a letter or digit to make a title"
		if d.named() != "" {
			d.errText = "the name needs a letter or digit to make a title"
			d.focus = cardStopName
		}
		return false, nil
	}
	// A stack is one repository (DESIGN §18.1), and the repo row can move
	// after a card has been chosen here. Refusing is the answer rather
	// than quietly dropping the choice: the store refuses it too
	// (ErrStackRepoMismatch), and that refusal arrives after the card has
	// already been created, as a warning about a stack that did not
	// happen.
	if c, ok := d.stackCandFor(d.stackOnto); ok && c.Repo != d.formRepo() {
		d.errText = c.ID.Ref() + " is in " + stackRepoLabel(c.Repo) + " — a stack is one repository"
		d.focus = cardStopStack
		return false, nil
	}
	var env *int
	trimmed := strings.TrimSpace(d.env.Value())
	if trimmed != "" {
		n, err := domain.ParseDollars(trimmed)
		if err != nil {
			d.errText = "budget must be a dollar amount, like 5 or 12.50 (0 = uncapped)"
			return false, nil
		}
		env = &n
	}
	if d.mainCheckout && d.stackOnto != "" {
		// unreachable through the row itself — cycleWhere cleared it — but
		// a preset (the T gesture) or a web fill could have set both: the
		// refusal names the row rather than minting a card the store would
		// reject at Validate.
		d.errText = "a session in the main checkout has no branch to stack on — clear the stack row"
		d.focus = cardStopStack
		return false, nil
	}
	res := formResult{
		Desc: desc, Name: d.named(), Profile: d.profiles[d.profile], Envelope: env,
		Repo: d.formRepo(),
		Base: d.base, StackOnto: d.stackOnto, StackInto: d.stackInto,
		SessionBackend: d.sessionBackend, SessionModel: d.sessionModel,
		MainCheckout: d.mainCheckout, Home: d.home,
	}
	if d.imported != nil {
		res.ExternalRef = d.imported.prop.ExternalRef
		// the issue's comments are part of what the session is asked
		if disc := strings.TrimSpace(d.imported.prop.Report.Discussion); disc != "" {
			res.Desc += "\n\n## Discussion\n\n" + disc
		}
	}
	if d.onSubmit == nil {
		return true, nil
	}
	return true, d.onSubmit(res)
}

// --- rendering ---

const cardLabelW = 9 // "becomes" plus two spaces, the widest label

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
func packChoices(cells []string, width int, sep string) (lines []string, lineOf []int) {
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
		case curW+ansi.StringWidth(sep)+cw <= width:
			cur += sep + c
			curW += ansi.StringWidth(sep) + cw
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
func foldChoices(s *theme.Styles, cells []string, sel, width, maxLines int, sep string) []string {
	if len(cells) == 0 {
		return nil
	}
	lines, _ := packChoices(cells, width, sep)
	if len(lines) <= maxLines {
		return lines
	}
	// the tail's room has to come out of the cells' budget, so pack again
	// against the narrower width before choosing the window.
	var lineOf []int
	lines, lineOf = packChoices(cells, width-ansi.StringWidth(choiceTail(s, len(cells))), sep)
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

// foldReadout folds an already-labelled row — the draft's `from`, a
// dialog's labelled value — to width, indenting its continuation lines
// under the value (the label column, cardLabelW, plus its margin) so the
// row still reads as one. It wraps rather than truncates: the
// `becomes` line carries the derived title with DeriveTitle's own
// ellipsis where a long first line was cut to maxTitleLen, and that cut
// is the whole reason the title is shown there at all — a second,
// width-driven ellipsis on top of it would hide exactly what the line
// exists to say.
func foldReadout(line string, width int) []string {
	const indent = cardLabelW + 2
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

// named is the name the person typed, "" for none.
func (d *cardForm) named() string { return strings.TrimSpace(d.name.Value()) }

// title is the card's title as Create would mint it: the name the person
// gave it, else the text's first line (cardmint.Mint).
func (d *cardForm) title() string {
	if n := d.named(); n != "" {
		return domain.DeriveTitle(n)
	}
	title, _, _ := domain.SplitFreeform(strings.TrimSpace(d.text.Value()))
	return title
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

// asksStack is whether the stack row is worth a tab stop: a session
// running in the main checkout holds no branch to be stacked onto.
func (d *cardForm) asksStack() bool {
	return !d.mainCheckout
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

// whereIdx is the selected cell of whereChoices.
func (d *cardForm) whereIdx() int {
	switch {
	case d.home:
		return 2
	case d.mainCheckout:
		return 1
	}
	return 0
}

// cycleWhere moves the "runs in" row. The main checkout has no branch,
// so moving onto it clears the fork-from and stack rows — a base or a
// card to stack on names a branch this session will never have, and the
// readouts above stop showing them the same frame. Moving back restores
// nothing: the rows open on their defaults, as a fresh form would.
func (d *cardForm) cycleWhere(dir int) {
	n := len(d.whereChoices())
	i := (d.whereIdx() + dir) % n
	if i < 0 {
		i += n
	}
	d.mainCheckout, d.home = i >= 1, i == 2
	if d.mainCheckout {
		d.base = ""
		d.stackOnto, d.stackLabel, d.stackInto = "", "", ""
	}
}

// setRepoData hands the form the board's branch facts (each repo's checked-out
// branch and its branch list) and offers the selected repo's base candidates.
func (d *cardForm) setRepoData(checkedOut map[string]string, branches map[string][]string) {
	d.checkedOut, d.branchesOf = checkedOut, branches
	d.setBaseCands(branches[d.repo.name()], checkedOut[d.repo.name()])
}

// moveRepo runs move, a change of the repo row made by hand, and when it
// lands on a different repo offers that repo's own branches. Choosing the
// repo already selected leaves the base row as it was.
func (d *cardForm) moveRepo(move func()) {
	before := d.repo.name()
	move()
	if d.repo.name() != before {
		d.setRepoData(d.checkedOut, d.branchesOf)
	}
}

// setRepoChoices re-offers the repositories after a rescan (repoChoices). The
// base cursor stays on the branch it was on when that branch still exists,
// and an explicit base the user chose is kept.
func (d *cardForm) setRepoChoices(names []string, base map[string]string, branches map[string][]string) {
	prev := ""
	if d.baseCursor < len(d.baseCands) {
		prev = d.baseCands[d.baseCursor]
	}
	explicit := d.base
	d.repo.rechoose(names)
	d.setRepoData(base, branches)
	for i, b := range d.baseCands {
		if b == prev {
			d.baseCursor = i
			break
		}
	}
	d.base = explicit
}

// digitKey returns 1–9 for a bare digit key, else 0.
func digitKey(key tea.KeyPressMsg) int {
	if key.Mod != 0 || len(key.Text) != 1 || key.Text[0] < '1' || key.Text[0] > '9' {
		return 0
	}
	return int(key.Text[0] - '0')
}

func cardLabel(s *theme.Styles, label string) string {
	return "  " + s.Faint.Render(fmt.Sprintf("%-*s", cardLabelW, label))
}

// foldedRow prefixes a folded option row with its already-rendered
// label, indenting the continuation lines by the label cell's width
// (indent, which differs between the top-level rows and the expanded
// option rows under "runs as").
func foldedRow(s *theme.Styles, prefix string, indent int, cells []string, sel, width, maxLines int) []string {
	lines := foldChoices(s, cells, sel, max(width-indent, choiceMinWidth), maxLines, choiceSep)
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

// whereChoices is the "runs in" row's cells: the card's own worktree,
// which is what nearly every session wants and what every card minted
// before the choice existed got, then the main checkout, then an agent,
// in no repository at all.
func (d *cardForm) whereChoices() []string {
	return []string{"its own worktree", "the main checkout", "an agent"}
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
