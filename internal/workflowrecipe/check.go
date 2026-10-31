package workflowrecipe

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/morphis/gummi/internal/domain"
)

// templateRe finds a {{…}} in a prompt.
var templateRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// slotName is what a step's slot may be: a profile role name, the rule
// config.SaveProfile holds a profile's roles to.
var slotName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// AgentName reports whether a step's agent is spelled as a backend name
// may be: the slot rule. Whether gummi has that backend is the engine's.
func AgentName(s string) bool { return slotName.MatchString(s) }

// idRe is what an entry's id may hold.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func allDigits(s string) bool {
	return strings.Trim(s, "0123456789") == ""
}

// Check judges whether a recipe can run (§25.4). Problems refuse it: an id
// missing or used twice, an unknown role or tier, a step without a prompt,
// a {{…}} naming no input, artifact or earlier step, a repeat without a
// max or a single until, a parallel without a valid join, an until naming
// an artifact its body does not declare, and an artifact whose file would
// sit outside the run's directory. Warnings do not: a parallel holding more
// than one writing role, whose steps can overwrite each other's work, and
// a step that still names an envelope or a tier, which are ignored.
// Whether a recipe reviews, tests or designs is its own choice; Check sets
// no floor.
func Check(r domain.Recipe) (problems, warnings []string) {
	return check(r, false)
}

// check is Check; frozen is the recipe a dispatch carries, which is run by
// a session that is not an agent and so may not dispatch in turn.
func check(r domain.Recipe, frozen bool) (problems, warnings []string) {
	c := checker{
		r:         r,
		frozen:    frozen,
		ids:       map[string]string{},
		artifacts: map[string]bool{},
	}
	for i, e := range r.Steps {
		if e.Step != nil && e.Step.Watch != nil && c.watchID == "" {
			c.watchID = e.Step.ID
			c.watchAt = i
		}
	}
	if c.watchID != "" {
		for _, e := range r.Steps[:c.watchAt] {
			c.topIDs = append(c.topIDs, e.ID())
		}
	}
	if len(r.Steps) > 0 && r.Steps[0].Step != nil {
		c.firstID = r.Steps[0].Step.ID
	}
	if r.StaleTier {
		c.warnings = append(c.warnings, "the recipe's tier is ignored — a step's model comes from its role's profile slot, or the slot it names")
	}
	if len(r.Steps) == 0 {
		c.problem("the recipe has no steps")
	}
	Walk(r.Steps, func(s domain.Step, _ []domain.Entry) {
		for name := range s.Artifacts {
			c.artifacts[name] = true
		}
	})
	c.entries(r.Steps)
	for id, where := range c.handlerIDs {
		if _, dup := c.ids[id]; dup {
			c.problem("the id %q is used by the recipe's own step and by a step of %s", id, where)
		}
	}
	return c.problems, c.warnings
}

type checker struct {
	r         domain.Recipe
	frozen    bool   // a dispatch's recipe: no dispatch inside
	firstID   string // the id of the recipe's first step, when it is a plain one
	problems  []string
	warnings  []string
	ids       map[string]string // id → the kind that took it
	artifacts map[string]bool   // every artifact any step declares
	// earlier holds the ids of steps that run before the one being
	// checked; last is the plain step that ran just before it.
	earlier []string
	last    string
	// watchID is the recipe's first top-level watch, watchAt its index;
	// afterWatch is set once the walk has passed it.
	watchID    string
	watchAt    int
	afterWatch bool
	inParallel bool
	inRepeat   bool
	inHandler  bool
	topIDs     []string // the top-level ids before the watch
	// handlerIDs is each step id a handler's recipe holds, by the handler
	// that holds it: an id the recipe's own steps use too would resolve to
	// the wrong step wherever a step is found by id
	handlerIDs map[string]string
	seenWatch  bool
}

func (c *checker) problem(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *checker) id(id, kind string) string {
	if id == "" {
		c.problem("a %s has no id", kind)
		return "a " + kind + " without an id"
	}
	if !idRe.MatchString(id) || allDigits(id) {
		// a path joins ids with "/" and numbers a repeat's iterations, and a
		// template names an id before ".output": an id holds none of them
		c.problem("the id %q may hold only letters, digits, - and _, and not digits alone", id)
	}
	if prev, dup := c.ids[id]; dup {
		c.problem("the id %q is used twice (a %s and a %s)", id, prev, kind)
	} else {
		c.ids[id] = kind
	}
	return id
}

func (c *checker) entries(es []domain.Entry) {
	for _, e := range es {
		switch {
		case e.Step != nil:
			c.step(*e.Step, c.last)
			c.earlier = append(c.earlier, e.Step.ID)
			c.last = e.Step.ID
			if e.Step.Watch != nil {
				c.afterWatch = true
			}
		case e.Parallel != nil:
			c.parallel(*e.Parallel)
		case e.Repeat != nil:
			c.repeat(*e.Repeat)
		default:
			c.problem("an entry is none of a step, a parallel or a repeat")
		}
	}
}

func (c *checker) parallel(p domain.Parallel) {
	name := c.id(p.ID, "parallel")
	if !p.Join.Known() {
		c.problem("parallel %s: join %q is not one of all, allSettled or any", name, p.Join)
	}
	if len(p.Steps) == 0 {
		c.problem("parallel %s has no steps", name)
	}
	before := c.last
	writers := 0
	c.inParallel = true
	defer func() { c.inParallel = false }()
	for _, s := range p.Steps {
		c.step(s, before)
		if s.Role.Writes() {
			writers++
		}
	}
	// the branches run side by side: none is earlier than another, and the
	// step after the block reads the last branch as previous.
	for _, s := range p.Steps {
		c.earlier = append(c.earlier, s.ID)
		c.last = s.ID
	}
	if writers > 1 {
		c.warnings = append(c.warnings, fmt.Sprintf("parallel %s holds %d writing steps: they share one tree and can overwrite each other's work", name, writers))
	}
}

func (c *checker) repeat(rp domain.Repeat) {
	name := c.id(rp.ID, "repeat")
	if rp.Max < 1 {
		c.problem("repeat %s has no max", name)
	}
	if !rp.OnMax.Known() {
		c.problem("repeat %s: onMax %q is not one of abort, continue or pause", name, rp.OnMax)
	}
	if len(rp.Steps) == 0 {
		c.problem("repeat %s has no steps", name)
	}
	switch rp.Until.Kind() {
	case "":
		c.problem("repeat %s needs exactly one until: checks, {verdict, in} or {contains}", name)
	case domain.UntilVerdict:
		if rp.Until.Verdict == "" || rp.Until.In == "" {
			c.problem("repeat %s: an until verdict names both the word and the artifact it is in", name)
			break
		}
		declared := false
		Walk(rp.Steps, func(s domain.Step, _ []domain.Entry) {
			if _, ok := s.Artifacts[rp.Until.In]; ok {
				declared = true
			}
		})
		if !declared {
			c.problem("repeat %s: until reads artifact %q, which no step in its body declares", name, rp.Until.In)
		}
	}
	outer := c.inRepeat
	c.inRepeat = true
	c.entries(rp.Steps)
	c.inRepeat = outer
}

func (c *checker) step(s domain.Step, previous string) {
	name := c.id(s.ID, "step")
	if s.Run != "" || s.Dispatch != nil || s.QuietExit != nil || s.PR != nil || s.Watch != nil {
		c.agentless(s, name, previous)
		return
	}
	if !s.Role.Known() {
		c.problem("step %s: role %q is not one of scout, planner, contractor, implementer, tester, scribe or reviewer", name, s.Role)
	}
	if s.Slot != "" && !slotName.MatchString(s.Slot) {
		c.problem("step %s: slot %q is not a profile slot name (lower case letters, digits and -)", name, s.Slot)
	}
	if s.Agent != "" && !AgentName(s.Agent) {
		c.problem("step %s: agent %q is not a backend name (lower case letters, digits and -)", name, s.Agent)
	}
	if strings.ContainsFunc(s.Model, unicode.IsSpace) {
		c.problem("step %s: model %q must be one model id with no spaces", name, s.Model)
	}
	if (s.Agent != "" || s.Model != "") && s.Slot != "" {
		c.warnings = append(c.warnings, fmt.Sprintf("step %s: its slot %q is ignored — the agent or model it names wins", name, s.Slot))
	}
	if s.StaleTier {
		c.warnings = append(c.warnings, fmt.Sprintf("step %s: its tier is ignored — its model comes from its role's profile slot, or the slot it names", name))
	}
	if s.StaleEnvelope {
		c.warnings = append(c.warnings, fmt.Sprintf("step %s: its envelope is ignored — a run spends from the session's budget and estimates nothing", name))
	}
	if strings.TrimSpace(s.Prompt) == "" {
		c.problem("step %s has no prompt", name)
	}
	for art, file := range s.Artifacts {
		if art == "" || file == "" || file != filepath.Base(file) || file == "." || file == ".." {
			c.problem("step %s: artifact %q must be a plain file name in the run's directory, not %q", name, art, file)
		}
	}
	for _, m := range templateRe.FindAllStringSubmatch(s.Prompt, -1) {
		if why := c.template(m[1], s.ID, previous); why != "" {
			c.problem("step %s: {{%s}} %s", name, m[1], why)
		}
	}
}

// agentless checks a command step (§26.3) or a dispatch step (§26.4):
// gummi's own acts, with no role, slot or prompt of their own.
func (c *checker) agentless(s domain.Step, name, previous string) {
	switch {
	case s.Run != "" && s.Dispatch != nil:
		c.problem("step %s: a step is a command (run) or a dispatch, not both", name)
		return
	case s.QuietExit != nil && s.Run == "":
		c.problem("step %s: quietExit belongs to a command step (run)", name)
		return
	case s.Dispatch != nil && c.frozen:
		c.problem("step %s: a dispatched recipe cannot dispatch in turn", name)
		return
	case s.PR != nil && s.Watch != nil, (s.PR != nil || s.Watch != nil) && (s.Run != "" || s.Dispatch != nil):
		c.problem("step %s: a step is exactly one of a command (run), a dispatch, a pr act or a watch", name)
		return
	}
	if s.Role != "" || s.Slot != "" || s.Agent != "" || s.Model != "" || strings.TrimSpace(s.Prompt) != "" || len(s.Artifacts) > 0 {
		c.problem("step %s: a %s step has no role, slot, agent, model, prompt or artifacts of its own", name, s.Kind())
	}
	if s.StaleEnvelope {
		c.problem("step %s: a %s step has no envelope of its own; a dispatch names its session's under dispatch", name, s.Kind())
	}
	templates := func(field, text string) {
		for _, m := range templateRe.FindAllStringSubmatch(text, -1) {
			if why := c.template(m[1], s.ID, previous); why != "" {
				c.problem("step %s: %s {{%s}} %s", name, field, m[1], why)
			}
		}
	}
	if s.PR != nil || s.Watch != nil {
		c.prOrWatch(s, name)
		return
	}
	if s.Run != "" {
		if q := s.QuietExit; q != nil {
			if *q < 1 || *q > 255 {
				c.problem("step %s: quietExit %d is not an exit code from 1 to 255", name, *q)
			}
			if s.ID == "" || s.ID != c.firstID {
				c.problem("step %s: quietExit belongs to the recipe's first step: a run that ended quietly leaves no trace, so no model may have worked before it", name)
			}
		}
		if strings.Contains(s.Run, "run.diff") {
			c.problem("step %s: a command cannot read {{run.diff}}", name)
		}
		if templateQuoted(s.Run) {
			c.problem("step %s: a {{template}} in a command is a word of its own, filled quoted: never write it inside quotes", name)
		}
		templates("run", s.Run)
		return
	}
	d := s.Dispatch
	switch {
	case d.Repo == "":
		c.problem("step %s: a dispatch names the repository its session is minted in", name)
	case strings.Contains(d.Repo, "{{"):
		c.problem("step %s: a dispatch's repo is a literal the person allows, never a template", name)
	case strings.ContainsAny(d.Repo, "/\\"):
		c.problem("step %s: repo %q must be a configured repository name, not a path", name, d.Repo)
	}
	seen := map[string]bool{d.Repo: true}
	for _, r := range d.Repos {
		switch {
		case strings.Contains(r, "{{"):
			c.problem("step %s: a dispatch's repos are literals the person allows, never templates", name)
		case !domain.RepoNameOK(r):
			c.problem("step %s: repos entry %q must be a configured repository name, not a path", name, r)
		case seen[r]:
			c.problem("step %s: repos entry %q is named twice, or is the dispatch's own repo", name, r)
		}
		seen[r] = true
	}
	if d.Envelope <= 0 {
		c.problem("step %s: a dispatch gives its session an envelope above zero, carved from the agent's", name)
	}
	if strings.TrimSpace(d.Title) == "" {
		c.problem("step %s: a dispatch names its session's title", name)
	}
	hasRecipe := d.Recipe != "" || strings.TrimSpace(d.RecipeYAML) != ""
	switch {
	case hasRecipe && strings.TrimSpace(d.Spec) == "":
		c.problem("step %s: a dispatch with a recipe gives its session the spec the run is judged against", name)
	case hasRecipe && strings.TrimSpace(d.Prompt) != "":
		c.problem("step %s: a dispatch with a recipe starts a run, not a conversation: give it a spec, not a prompt", name)
	case !hasRecipe && strings.TrimSpace(d.Prompt) == "":
		c.problem("step %s: a dispatch without a recipe gives its session an opening prompt", name)
	case !hasRecipe && strings.TrimSpace(d.Spec) != "":
		c.problem("step %s: a dispatch's spec is for its recipe's run: name a recipe, or give a prompt instead", name)
	}
	if y := strings.TrimSpace(d.RecipeYAML); y != "" {
		if d.Recipe == "" {
			c.problem("step %s: a frozen recipe comes with the name of the recipe it froze", name)
		}
		r, err := Parse([]byte(y))
		if err != nil {
			c.problem("step %s: its frozen recipe: %v", name, err)
		} else if p, _ := check(r, true); len(p) > 0 {
			c.problem("step %s: its frozen recipe cannot run: %s", name, strings.Join(p, "; "))
		}
	}
	templates("title", d.Title)
	templates("spec", d.Spec)
	templates("prompt", d.Prompt)
}

// template says why ref names nothing a step may read, or "" when it does.
func (c *checker) template(ref, self, previous string) string {
	switch ref {
	case "spec", "run.diff":
		return ""
	case "event":
		if !c.inHandler {
			return "belongs to a watch handler's steps"
		}
		return ""
	case "watch.event":
		if c.watchID == "" || c.afterWatch || c.inHandler {
			return "belongs to a step before the recipe's watch"
		}
		return ""
	case "previous.output":
		if previous == "" {
			return "has no step before it"
		}
		return ""
	}
	head, tail, ok := strings.Cut(ref, ".")
	switch {
	case !ok:
	case head == "inputs":
		if _, declared := c.r.Inputs[tail]; !declared {
			return "names no input of the recipe"
		}
		return ""
	case head == "artifacts":
		if !c.artifacts[tail] {
			return "names no artifact any step declares"
		}
		return ""
	case tail == "output":
		if head == self {
			return "is the step's own output"
		}
		for _, id := range c.earlier {
			if id == head {
				return ""
			}
		}
		return "names no earlier step"
	}
	return "is not a template: spec, inputs.<name>, artifacts.<name>, previous.output, <step>.output, run.diff, or in a watch's handler event"
}

// templateQuoted reports whether a {{template}} in a shell command sits
// between quote characters, where its filled, quoted value would end the
// quote and let an earlier step's output run as commands.
func templateQuoted(command string) bool {
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && strings.HasPrefix(command[i:], "{{"):
			if loc := templateRe.FindStringIndex(command[i:]); loc != nil && loc[0] == 0 {
				return true
			}
		}
	}
	return false
}

// prOrWatch checks a pr step (§28.1) or a watch step (§28.2).
func (c *checker) prOrWatch(s domain.Step, name string) {
	if s.Role != "" || s.Slot != "" || s.Agent != "" || s.Model != "" || strings.TrimSpace(s.Prompt) != "" || len(s.Artifacts) > 0 {
		c.problem("step %s: a %s step has no role, slot, agent, model, prompt or artifacts of its own", name, s.Kind())
	}
	if s.StaleEnvelope {
		c.problem("step %s: a %s step has no envelope of its own", name, s.Kind())
	}
	if s.PR != nil {
		switch {
		case c.inHandler:
			c.problem("step %s: a handler never publishes; gummi pushes after each pass", name)
		case c.inParallel:
			c.problem("step %s: a pr step acts on the PR alone, never inside a parallel", name)
		}
		known := false
		for _, a := range domain.PRActs {
			known = known || a == s.PR.Act
		}
		if !known {
			c.problem("step %s: pr act %q is not push, create, update or ready", name, s.PR.Act)
		}
		if s.PR.Draft && s.PR.Act != "create" {
			c.problem("step %s: draft belongs to pr act create", name)
		}
		return
	}
	w := s.Watch
	switch {
	case c.inHandler:
		c.problem("step %s: a handler cannot hold a watch", name)
		return
	case c.inParallel:
		c.problem("step %s: a watch waits alone, never inside a parallel", name)
		return
	case c.inRepeat:
		c.problem("step %s: a watch sits at the top level, never inside a repeat", name)
		return
	case c.seenWatch:
		c.problem("step %s: a recipe waits on its PR in one watch step; %s is already one", name, c.watchID)
		return
	}
	c.seenWatch = true
	switch w.Until {
	case "", domain.UntilMerged, domain.UntilGreenApproved:
	default:
		c.problem("step %s: watch until %q is not merged or green-approved", name, w.Until)
	}
	switch w.OnClosed {
	case "", domain.OnClosedAbort, domain.OnClosedContinue:
	default:
		c.problem("step %s: watch onClosed %q is not abort or continue", name, w.OnClosed)
	}
	if w.Max < 0 {
		c.problem("step %s: watch max must be zero or more", name)
	}
	for _, ch := range w.Checks {
		if ch == "" {
			c.problem("step %s: watch checks names a check", name)
		}
	}
	c.trust(w.Trust, name)
	if len(w.On) == 0 {
		c.problem("step %s: a watch names at least one event under on", name)
	}
	var evs []string
	for ev := range w.On {
		evs = append(evs, string(ev))
	}
	sort.Strings(evs)
	for _, e := range evs {
		ev := domain.WatchEvent(e)
		h := w.On[ev]
		if !ev.Known() {
			c.problem("step %s: event %q is not base-moved, checks-failed, changes-requested or comment", name, ev)
			continue
		}
		c.handler(name, ev, h)
	}
}

// trust checks a watch's trust: a literal, or one {{inputs.x}}.
func (c *checker) trust(t, name string) {
	t = strings.TrimSpace(t)
	if !strings.Contains(t, "{{") {
		if _, err := ParseTrust(t); err != nil {
			c.problem("step %s: trust: %v", name, err)
		}
		return
	}
	m := templateRe.FindStringSubmatch(t)
	if m == nil {
		c.problem("step %s: trust has an unterminated or malformed {{ }}; it is a literal or one {{inputs.<name>}}", name)
		return
	}
	head, input, ok := strings.Cut(m[1], ".")
	if m[0] != t || !ok || head != "inputs" {
		c.problem("step %s: trust is a literal or one {{inputs.<name>}}", name)
		return
	}
	if _, declared := c.r.Inputs[input]; !declared {
		c.problem("step %s: trust {{inputs.%s}} names no input of the recipe", name, input)
		return
	}
	if d, has := c.r.InputDefaults[input]; has {
		if _, err := ParseTrust(d); err != nil {
			c.problem("step %s: trust input %s's default: %v", name, input, err)
		}
	}
}

func (c *checker) handler(name string, ev domain.WatchEvent, h domain.Handler) {
	if h.Max < 0 {
		c.problem("step %s: watch max must be zero or more", name)
	}
	var forms []string
	if len(h.Steps) > 0 {
		forms = append(forms, "steps")
	}
	if h.Recipe != "" || strings.TrimSpace(h.RecipeYAML) != "" {
		forms = append(forms, "a recipe")
	}
	if h.Goto != "" {
		forms = append(forms, "a goto")
	}
	switch {
	case len(forms) == 0:
		c.problem("step %s: the %s handler names steps, a recipe or a goto", name, ev)
		return
	case len(forms) > 1:
		c.problem("step %s: the %s handler is one of steps, a recipe or a goto, not %s", name, ev, strings.Join(forms, " and "))
		return
	}
	switch h.Form() {
	case "goto":
		found := false
		for _, id := range c.topIDs {
			found = found || id == h.Goto
		}
		if !found {
			c.problem("step %s: the %s handler's goto %q names no earlier top-level step", name, ev, h.Goto)
		}
	case "recipe":
		if h.Recipe == "" {
			c.problem("step %s: the %s handler's frozen recipe comes with the name of the recipe it froze", name, ev)
		} else if err := CheckName(h.Recipe); err != nil {
			c.problem("step %s: the %s handler's recipe: %v", name, ev, err)
		}
		if y := strings.TrimSpace(h.RecipeYAML); y != "" {
			r, err := Parse([]byte(y))
			if err != nil {
				c.problem("step %s: the %s handler's recipe %s: %v", name, ev, h.Recipe, err)
				break
			}
			held := ""
			Walk(r.Steps, func(st domain.Step, _ []domain.Entry) {
				if c.handlerIDs == nil {
					c.handlerIDs = map[string]string{}
				}
				c.handlerIDs[st.ID] = fmt.Sprintf("the %s handler's recipe %s of step %s", ev, h.Recipe, name)
				switch {
				case st.Watch != nil:
					held = "watch"
				case st.PR != nil && held == "":
					held = "pr"
				case st.Dispatch != nil && held == "":
					held = "dispatch"
				}
			})
			if held != "" {
				c.problem("step %s: the %s handler's recipe %s holds a %s step of its own, and a handler cannot", name, ev, h.Recipe, held)
			} else if p, _ := check(r, true); len(p) > 0 {
				c.problem("step %s: the %s handler's recipe cannot run: %s", name, ev, strings.Join(p, "; "))
			}
		}
	case "steps":
		saved, savedLast := c.inHandler, c.last
		c.inHandler = true
		for _, hs := range h.Steps {
			c.step(hs, c.last)
			c.last = hs.ID
		}
		c.inHandler, c.last = saved, savedLast
	}
}
