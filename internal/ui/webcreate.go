package ui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/attachment"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// New cards from the web face. Both halves are the TUI's own card form
// (cardform.go): GET /api/form reads the choices off a form opened the
// way `n` opens it, and POST /api/cards fills one in and submits it — so
// the form's validation, its result and the shell's follow-up (createCard,
// cardCreated: dependencies, stack placement, freeform starting at once,
// "create & autopilot") are the ones the terminal runs, not a second copy.

// webKindValue is a card type as the form's kind choice names it:
// research's diagnosis mode is its own entry, as it is on the kind row.
func webKindValue(ct domain.CardType) string {
	if ct.Kind == domain.KindResearch && ct.Mode == domain.ModeDiagnosis {
		return string(domain.KindResearch) + ":" + string(domain.ModeDiagnosis)
	}
	return string(ct.Kind)
}

// webCardType reads the request's kind back into a card type: a kind
// value WebForm offered, or "research" with the diagnosis flag.
func webCardType(kind string, diagnosis bool) (domain.CardType, bool) {
	if kind == "" {
		kind = string(domain.KindFeature)
	}
	ct := domain.CardType{Kind: domain.Kind(kind)}
	if kind == webKindValue(domain.CardType{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}) ||
		(ct.Kind == domain.KindResearch && diagnosis) {
		ct = domain.CardType{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}
	}
	return ct, ct.Valid()
}

// WebForm is GET /api/form: the new-card form's choices, for repo (the
// workspace default when empty).
func (m *Shell) WebForm(repo string) (webapi.Form, error) {
	d := m.openCardForm(domain.CardType{Kind: domain.KindFeature})
	if problem := m.webFormRepo(d, repo); problem != "" {
		return webapi.Form{}, refuse(WebBadRequest, problem)
	}
	f := webapi.Form{
		Kinds:      make([]webapi.Choice, 0, len(domain.CardTypes)),
		Profiles:   append([]string{}, d.profiles...),
		Repos:      []string{},
		Severities: []string{},
		Branches:   append([]string{}, d.baseCands...),
		Stackable:  []webapi.CardRef{},
		Dependable: []webapi.CardRef{},
		Envelope:   m.envelopePrefill(),
		Sessions:   m.webSessionModels(),
	}
	for _, ct := range domain.CardTypes {
		f.Kinds = append(f.Kinds, webapi.Choice{Value: webKindValue(ct), Label: ct.Name(), Detail: firstLineOf(cardPlaceholderFor(ct))})
	}
	if d.repo.shown() {
		f.Repos = append(f.Repos, d.repo.options()...)
	}
	for _, s := range bugSeverityChoices {
		if s != "" {
			f.Severities = append(f.Severities, string(s))
		}
	}
	for _, c := range d.stackVisible() {
		f.Stackable = append(f.Stackable, webapi.CardRef{ID: string(c.ID), Title: c.Title})
	}
	for _, c := range d.afterCands {
		f.Dependable = append(f.Dependable, webapi.CardRef{ID: string(c.ID), Title: c.Title, Stage: string(c.Stage)})
	}
	return f, nil
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// webFormRepo moves the form's repo row to repo and offers that repo's
// branches, the way the form opens on its preselected one (carddoor.go).
func (m *Shell) webFormRepo(d *cardForm, repo string) string {
	if repo == "" {
		return ""
	}
	if !slices.Contains(d.repo.options(), repo) || !d.repo.shown() {
		return "repository " + strconv.Quote(repo) + " is not configured"
	}
	d.repo.selectName(repo)
	d.setBaseCands(m.repoBranches[d.repo.name()], m.baseBranches[d.repo.name()])
	return ""
}

// Form is GET /api/form: WebForm's choices, with each installed agent's
// model list replaced by the merged view (SessionModelChoices) — the
// agent's own catalog where it can say one, plus the workspace's
// profile ids. The merge asks a backend and possibly runs its CLI, so it
// happens here, off the loop; WebForm built the same rows inline from
// SessionSuggestions alone.
func (b *Bridge) Form(ctx context.Context, repo string) (webapi.Form, error) {
	// the branch lists are a git read the loop must not make (the same
	// call routes_create.go's handler made inline)
	b.RefreshBranches(ctx)
	var (
		f     webapi.Form
		werr  error
		name  string
		pool  *worktree.Pool
		store *state.Store
	)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		f, werr = m.WebForm(repo)
		name, pool, store = repo, m.wt, m.store
		if name == "" {
			name = m.openCardForm(domain.CardType{Kind: domain.KindFeature}).repo.name()
		}
		return nil
	}); err != nil {
		return webapi.Form{}, err
	}
	if werr != nil {
		return webapi.Form{}, werr
	}
	// the adopt list's git reads run beside the catalog's backend asks,
	// which are the slow half of a first form read
	adoptable := make(chan []webapi.AdoptChoice, 1)
	go func() {
		if pool == nil || store == nil {
			adoptable <- nil
			return
		}
		adoptable <- webAdoptable(ctx, pool, store, name, f.Branches)
	}()
	mergeSessionCatalog(ctx, b.shell.engine, &f.Sessions)
	f.Adoptable = <-adoptable
	return f, nil
}

// webAdoptable says which of the repo's branches a new card may adopt,
// and why not, with the refusals cardmint and worktree.InspectBranch
// would give: a branch another card holds (any card, landed ones too —
// one branch, one card), the branch the card would land on, and a branch
// with no commits of its own past it or no history in common with it.
// Measured against the repo's default base; the git reads make it the
// off-loop half of the form.
func webAdoptable(ctx context.Context, pool *worktree.Pool, store *state.Store, repo string, branches []string) []webapi.AdoptChoice {
	if len(branches) == 0 {
		return nil
	}
	owners := map[string]domain.FeatureID{}
	if fs, err := store.ListFeatures(ctx); err == nil {
		for _, f := range fs {
			// BranchTaken's rule: research cards hold no branch
			if f.Repo != repo || f.Kind == domain.KindResearch {
				continue
			}
			if b := f.BranchName(); b != "" {
				if _, dup := owners[b]; !dup {
					owners[b] = f.ID
				}
			}
		}
	}
	base := pool.BaseBranch(ctx, repo)
	out := make([]webapi.AdoptChoice, 0, len(branches))
	for _, b := range branches {
		c := webapi.AdoptChoice{Branch: b}
		switch owner, held := owners[b]; {
		case held:
			c.Why, c.Held = string(owner)+" has it", string(owner)
		case b == base:
			c.Why = "the branch it lands on"
		default:
			if n, err := pool.Ahead(ctx, repo, b, base); err != nil {
				c.Why = "no history in common with " + base
			} else if n == 0 {
				c.Why = "no commits past " + base
			}
		}
		out = append(out, c)
	}
	return out
}

// webFill sets the form's rows from a request, refusing a value no row
// offers — the same set the form's own cycling can reach.
func (m *Shell) webFill(d *cardForm, req webapi.CreateCardRequest) string {
	if problem := m.webFormRepo(d, req.Repo); problem != "" {
		return problem
	}
	if req.Backend != "" || strings.TrimSpace(req.Model) != "" {
		// only a session picks its own model: a stage takes its agent from
		// the card's profile (DESIGN §19.8)
		if d.ct.Kind != domain.KindFreeform {
			return "only a session picks its own model — a " + d.ct.Name() + "'s stages take theirs from its profile"
		}
		if problem := m.checkSessionPick(req.Backend, req.Model); problem != "" {
			return problem
		}
		d.sessionBackend, d.sessionModel = req.Backend, strings.TrimSpace(req.Model)
	}
	if req.MainCheckout {
		// the freeform kind's "runs in" choice, refused everywhere else
		// for the same reason the model pick is: every other kind exists
		// to end as a branch.
		if d.ct.Kind != domain.KindFreeform {
			return "only a session can run in the main checkout — a " + d.ct.Name() + " works in a branch worktree of its own"
		}
		if req.Base != "" {
			return "a session in the main checkout forks from no branch — drop base"
		}
		if req.StackOn != "" {
			return "a session in the main checkout has no branch to stack on — drop stackOn"
		}
		d.mainCheckout = true
	}
	text := strings.TrimSpace(req.Title)
	var body []string
	if s := strings.TrimSpace(req.Description); s != "" {
		body = append(body, s)
	}
	// a bug's report, under the headings the form's placeholder teaches
	for _, sec := range [][2]string{
		{"Steps to reproduce", req.Repro}, {"Expected", req.Expected},
		{"Actual", req.Actual}, {"Environment", req.Env},
	} {
		if s := strings.TrimSpace(sec[1]); s != "" {
			body = append(body, "## "+sec[0]+"\n\n"+s)
		}
	}
	if len(req.Attachments) > 0 {
		if m.engine == nil {
			return m.noAgent(" (attachments need the workspace's store)")
		}
		refs, err := m.engine.Attachments().Resolve(req.Attachments)
		if err != nil {
			return err.Error()
		}
		for _, ref := range refs {
			body = append(body, attachment.Link(ref))
		}
	}
	if len(body) > 0 {
		text += "\n\n" + strings.Join(body, "\n\n")
	}
	if why := tooLong(d.text.CharLimit, "the card's text", text); why != "" {
		return why
	}
	d.text.SetValue(text)
	if req.Envelope != nil {
		if why := tooLong(d.env.CharLimit, "that budget", strconv.Itoa(*req.Envelope)); why != "" {
			return why
		}
		d.env.SetValue(strconv.Itoa(*req.Envelope))
	}
	if req.Profile != "" {
		i := slices.Index(d.profiles, req.Profile)
		if i < 0 {
			return "profile " + strconv.Quote(req.Profile) + " is not configured"
		}
		d.profile = i
	}
	if req.Severity != "" {
		i := slices.Index(bugSeverityChoices, domain.Severity(req.Severity))
		if i < 0 {
			return "severity " + strconv.Quote(req.Severity) + " is not one of " + strings.Join(func() []string {
				var out []string
				for _, s := range bugSeverityChoices[1:] {
					out = append(out, string(s))
				}
				return out
			}(), ", ")
		}
		d.sev = i
	}
	for _, dep := range req.DependsOn {
		id := domain.FeatureID(strings.TrimSpace(dep))
		if !slices.ContainsFunc(d.afterCands, func(c afterCand) bool { return c.ID == id }) {
			return string(id) + " cannot be waited on — it is done, or not on this board"
		}
		if !slices.Contains(d.after, id) {
			d.after = append(d.after, id)
		}
	}
	if req.Base != "" {
		if !slices.Contains(d.baseCands, req.Base) {
			return "branch " + strconv.Quote(req.Base) + " is not in the repository"
		}
		d.base = req.Base
	}
	if req.Adopt != "" {
		if !d.asksAdopt() || !slices.Contains(d.baseCands, req.Adopt) {
			return "branch " + strconv.Quote(req.Adopt) + " cannot be adopted by this card"
		}
		d.adopt = req.Adopt
	}
	if req.StackOn != "" {
		id := domain.FeatureID(req.StackOn)
		i := slices.IndexFunc(d.stackVisible(), func(c stackCand) bool { return c.ID == id })
		if !d.asksStack() || i < 0 {
			return string(id) + " is not a card this one can stack on"
		}
		c := d.stackVisible()[i]
		d.stackOnto, d.stackLabel = c.ID, c.Title
	}
	return ""
}

// RefreshBranches reads the repositories' branches again — off the
// board's loop, since it runs git — and hands them to the board, so the
// new-card form offers what the repository has now.
func (b *Bridge) RefreshBranches(ctx context.Context) {
	var wt *worktree.Pool
	if b.Do(ctx, func(m *Shell) tea.Cmd { wt = m.wt; return nil }) != nil || wt == nil {
		return
	}
	base, repo := readBranches(ctx, wt)
	_ = b.Do(ctx, func(m *Shell) tea.Cmd { m.baseBranches, m.repoBranches = base, repo; return nil })
}

// CreateCard is POST /api/cards: the new-card form, filled in and
// submitted as its Create (or, with Autopilot, its "Create & autopilot")
// button submits it. It answers the new card.
func (b *Bridge) CreateCard(ctx context.Context, req webapi.CreateCardRequest, person string) (webapi.Card, error) {
	ct, ok := webCardType(req.Kind, req.Diagnosis)
	if !ok {
		return webapi.Card{}, refuse(WebBadRequest, "no card kind "+strconv.Quote(req.Kind))
	}
	// A session is started from its first message, the way a person
	// starts a conversation, and takes its title from that message's first
	// line (domain.SplitFreeform) — the draft has no title field to fill.
	session := ct.Kind == domain.KindFreeform && strings.TrimSpace(req.Description) != ""
	if strings.TrimSpace(req.Title) == "" && !session {
		return webapi.Card{}, refuse(WebBadRequest, "a card needs a title")
	}
	if req.Adopt != "" || req.Base != "" {
		b.RefreshBranches(ctx)
	}
	// only "create & autopilot" answers the autopilot switch its flow
	// opens; a plain create leaves any such dialog for a person
	in := webInput{actor: state.PersonActor(person)}
	if req.Autopilot {
		in.mode, in.autopilot, in.handoverAsked = domain.GateAutopilot, true, true
	}
	out, werr := b.intent(ctx, "", in, webWait, func(m *Shell, _ featureRow) (tea.Cmd, error) {
		d := m.openCardForm(ct)
		if problem := m.webFill(d, req); problem != "" {
			return nil, refuse(WebBadRequest, problem)
		}
		done, cmd := d.submit(req.Autopilot)
		if !done {
			return nil, refuse(WebBadRequest, d.errText)
		}
		return cmd, nil
	})
	if werr != nil {
		return webapi.Card{}, werr
	}
	if out.created == "" {
		if e := out.err(); e != nil {
			return webapi.Card{}, e
		}
		return webapi.Card{}, refuse(WebConflict, "the card was not created")
	}
	return b.Card(ctx, string(out.created))
}
