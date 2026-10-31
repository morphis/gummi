// Package workflowrecipe reads and judges workflow recipes (DESIGN §25.4):
// the YAML a recipe is kept as, the rules a recipe must pass before it may
// run (Check), the templates a
// step's prompt is filled from (Fill), the recipes gummi bundles and the
// workspace's own (Load, List), the lines a step's output may carry —
// a planner's gummi-workflow block, FAILED:, MET:/UNMET:, DECISION: and
// OPEN: — and an artifact captured from a step's output when the step did
// not write it (CaptureArtifact). It is pure apart from Load and List reading recipe files: no
// store, no git, no agent.
package workflowrecipe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// rawRecipe is a recipe's YAML before it becomes the AST.
type rawRecipe struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description,omitempty"`
	Tier        any                 `yaml:"tier,omitempty"`
	Inputs      map[string]rawInput `yaml:"inputs,omitempty"`
	Steps       []rawEntry          `yaml:"steps"`
}

// rawInput is one of a recipe's inputs: its hint alone, which makes it
// required, or a mapping that may let a run leave it out —
// `{hint, required: false}` fills as nothing, `{hint, default}` as the
// default.
type rawInput struct {
	Hint     string
	Required bool
	Default  *string
}

func (in *rawInput) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		in.Required = true
		return n.Decode(&in.Hint)
	}
	if n.Kind == yaml.MappingNode {
		// Node.Decode does not carry Parse's KnownFields: a misspelt key
		// would quietly make the input required
		for i := 0; i < len(n.Content); i += 2 {
			switch k := n.Content[i].Value; k {
			case "hint", "required", "default":
			default:
				return fmt.Errorf("line %d: an input takes hint, required and default, not %q", n.Content[i].Line, k)
			}
		}
	}
	var m struct {
		Hint     string  `yaml:"hint"`
		Required *bool   `yaml:"required"`
		Default  *string `yaml:"default"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	if m.Required != nil && *m.Required && m.Default != nil {
		return fmt.Errorf("line %d: an input with a default is not required", n.Line)
	}
	in.Hint, in.Default = m.Hint, m.Default
	// an input with a default may be left out; one without is required
	// unless it says it is not
	in.Required = m.Default == nil && (m.Required == nil || *m.Required)
	return nil
}

func (in rawInput) MarshalYAML() (any, error) {
	if in.Required {
		return in.Hint, nil
	}
	m := map[string]any{"hint": in.Hint}
	if in.Default != nil && *in.Default != "" {
		m["default"] = *in.Default
	} else {
		m["required"] = false
	}
	return m, nil
}

// rawEntry is one entry of steps: a step's fields, or a parallel or a
// repeat beside its id. Envelope and Tier are read only to be ignored: a
// recipe from before runs estimated nothing, or from before a step's model
// came from its role, still parses, and Check warns about it
// (domain.Step.StaleEnvelope, StaleTier).
type rawEntry struct {
	ID            string            `yaml:"id"`
	Role          string            `yaml:"role,omitempty"`
	Slot          string            `yaml:"slot,omitempty"`
	Agent         string            `yaml:"agent,omitempty"`
	Model         string            `yaml:"model,omitempty"`
	Tier          any               `yaml:"tier,omitempty"`
	Envelope      any               `yaml:"envelope,omitempty"`
	Artifacts     map[string]string `yaml:"artifacts,omitempty"`
	CaptureOutput *bool             `yaml:"captureOutput,omitempty"`
	Prompt        string            `yaml:"prompt,omitempty"`
	Run           string            `yaml:"run,omitempty"`
	QuietExit     *int              `yaml:"quietExit,omitempty"`
	Dispatch      *rawDispatch      `yaml:"dispatch,omitempty"`
	PR            *rawPR            `yaml:"pr,omitempty"`
	Watch         *rawWatch         `yaml:"watch,omitempty"`
	Parallel      *rawParallel      `yaml:"parallel,omitempty"`
	Repeat        *rawRepeat        `yaml:"repeat,omitempty"`
}

// rawDispatch is a dispatch step's session (DESIGN §26.4).
type rawDispatch struct {
	Repo       string   `yaml:"repo"`
	Repos      []string `yaml:"repos,omitempty"`
	Title      string   `yaml:"title,omitempty"`
	Spec       string   `yaml:"spec,omitempty"`
	Prompt     string   `yaml:"prompt,omitempty"`
	Recipe     string   `yaml:"recipe,omitempty"`
	RecipeYAML string   `yaml:"recipeYAML,omitempty"`
	Envelope   dollars  `yaml:"envelope"`
}

// rawPR is a pr step's act (DESIGN §28.1).
type rawPR struct {
	Act   string `yaml:"act"`
	Draft bool   `yaml:"draft,omitempty"`
}

// rawWatch is a watch step (DESIGN §28.2). Quiet is a Go duration, with d
// for days: 7d, 12h, 30m.
type rawWatch struct {
	Until    string                `yaml:"until,omitempty"`
	Trust    string                `yaml:"trust,omitempty"`
	Max      int                   `yaml:"max,omitempty"`
	Quiet    string                `yaml:"quiet,omitempty"`
	OnClosed string                `yaml:"onClosed,omitempty"`
	Checks   []string              `yaml:"checks,omitempty"`
	On       map[string]rawHandler `yaml:"on"`
}

// rawHandler is a watch event's handler: a YAML sequence is inline steps; a
// mapping holds recipe (and recipeYAML), goto or steps, and a max.
type rawHandler struct {
	Steps      []rawEntry `yaml:"steps,omitempty"`
	Recipe     string     `yaml:"recipe,omitempty"`
	RecipeYAML string     `yaml:"recipeYAML,omitempty"`
	Goto       string     `yaml:"goto,omitempty"`
	Max        int        `yaml:"max,omitempty"`
}

// strictKeys refuses a mapping key t does not declare, at any depth.
// Node.Decode does not carry Parse's KnownFields, so an inline handler
// step would otherwise take a misspelt flag as its default. A type that
// reads its own node (rawHandler, rawUntil, dollars) checks itself.
func strictKeys(n *yaml.Node, t reflect.Type) error {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice && n.Kind == yaml.SequenceNode {
			for _, c := range n.Content {
				if err := strictKeys(c, t.Elem()); err != nil {
					return err
				}
			}
			return nil
		}
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || n.Kind != yaml.MappingNode ||
		reflect.PointerTo(t).Implements(reflect.TypeFor[yaml.Unmarshaler]()) {
		return nil
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" {
			name = strings.ToLower(t.Field(i).Name)
		}
		fields[name] = t.Field(i).Type
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		ft, ok := fields[n.Content[i].Value]
		if !ok {
			return fmt.Errorf("line %d: unknown field %q", n.Content[i].Line, n.Content[i].Value)
		}
		if err := strictKeys(n.Content[i+1], ft); err != nil {
			return err
		}
	}
	return nil
}

func (h *rawHandler) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		if err := strictKeys(n, reflect.TypeOf(h.Steps)); err != nil {
			return err
		}
		return n.Decode(&h.Steps)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a handler is a list of steps, or a mapping of steps, recipe or goto", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "steps", "recipe", "recipeYAML", "goto", "max":
		default:
			return fmt.Errorf("line %d: a handler takes steps, recipe, goto and max, not %q", n.Content[i].Line, k)
		}
	}
	type plain rawHandler
	var p plain
	if err := strictKeys(n, reflect.TypeOf(p)); err != nil {
		return err
	}
	if err := n.Decode(&p); err != nil {
		return err
	}
	*h = rawHandler(p)
	return nil
}

func (h rawHandler) MarshalYAML() (any, error) {
	if len(h.Steps) > 0 && h.Recipe == "" && h.RecipeYAML == "" && h.Goto == "" && h.Max == 0 {
		return h.Steps, nil
	}
	type plain rawHandler
	return plain(h), nil
}

// ParseQuiet reads a watch's quiet: a Go duration, or whole days as 7d.
func ParseQuiet(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("quiet %q is not a duration like 7d, 12h or 30m", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("quiet %q is not a duration like 7d, 12h or 30m", s)
	}
	return d, nil
}

func formatQuiet(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return d.String()
}

// dollars is a budget a recipe writes the way a person types one —
// envelope: 5 or 2.50, in US dollars, as every face and flag takes it —
// held in credits, the unit every envelope is stored in.
type dollars int

func (d *dollars) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: an envelope is a dollar amount, like 5 or 2.50", n.Line)
	}
	c, err := domain.ParseDollars(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: envelope: %w", n.Line, err)
	}
	*d = dollars(c)
	return nil
}

func (d dollars) MarshalYAML() (any, error) {
	v, err := strconv.ParseFloat(domain.DollarsInput(int(d)), 64)
	if err != nil {
		return nil, err
	}
	return v, nil
}

type rawParallel struct {
	Join  string     `yaml:"join"`
	Steps []rawEntry `yaml:"steps"`
}

type rawRepeat struct {
	Max   int        `yaml:"max"`
	Until rawUntil   `yaml:"until"`
	OnMax string     `yaml:"onMax,omitempty"`
	Steps []rawEntry `yaml:"steps"`
}

// rawUntil is a stop condition: the scalar `checks`, or a mapping of
// checks, verdict and in, or contains.
type rawUntil struct {
	Checks   bool   `yaml:"checks,omitempty"`
	Verdict  string `yaml:"verdict,omitempty"`
	In       string `yaml:"in,omitempty"`
	Contains string `yaml:"contains,omitempty"`
}

func (u *rawUntil) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if strings.TrimSpace(n.Value) != domain.UntilChecks {
			return fmt.Errorf("line %d: until %q: a bare until may only say checks", n.Line, n.Value)
		}
		u.Checks = true
		return nil
	}
	type plain rawUntil
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*u = rawUntil(p)
	return nil
}

func (u rawUntil) MarshalYAML() (any, error) {
	if u.Checks && u.Verdict == "" && u.In == "" && u.Contains == "" {
		return domain.UntilChecks, nil
	}
	type plain rawUntil
	return plain(u), nil
}

// Parse reads a recipe's YAML. It refuses what the AST cannot hold — an
// unknown field, an entry that is more than one kind, a parallel holding
// anything but plain steps — and leaves every other judgement to Check.
func Parse(data []byte) (domain.Recipe, error) {
	var raw rawRecipe
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return domain.Recipe{}, errors.New("the recipe is empty")
		}
		return domain.Recipe{}, fmt.Errorf("the recipe's YAML: %w", err)
	}
	r := domain.Recipe{
		Name:        strings.TrimSpace(raw.Name),
		Description: strings.TrimSpace(raw.Description),
		StaleTier:   raw.Tier != nil,
	}
	for name, in := range raw.Inputs {
		if r.Inputs == nil {
			r.Inputs = map[string]string{}
		}
		r.Inputs[name] = in.Hint
		if !in.Required {
			if r.InputDefaults == nil {
				r.InputDefaults = map[string]string{}
			}
			if in.Default != nil {
				r.InputDefaults[name] = *in.Default
			} else {
				r.InputDefaults[name] = ""
			}
		}
	}
	steps, err := entries(raw.Steps)
	if err != nil {
		return domain.Recipe{}, err
	}
	r.Steps = steps
	return r, nil
}

// ParseEntries reads a list of entries — the steps of a recipe without
// the recipe around them, as a planner's gummi-workflow block writes them.
func ParseEntries(data []byte) ([]domain.Entry, error) {
	var raw []rawEntry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no steps")
		}
		return nil, fmt.Errorf("the steps' YAML: %w", err)
	}
	return entries(raw)
}

func entries(raw []rawEntry) ([]domain.Entry, error) {
	var out []domain.Entry
	for _, re := range raw {
		e, err := entry(re)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (re rawEntry) hasStepFields() bool {
	return re.Role != "" || re.Prompt != "" || re.Slot != "" || re.Agent != "" || re.Model != "" || re.Tier != nil || re.Envelope != nil ||
		len(re.Artifacts) > 0 || re.CaptureOutput != nil ||
		re.Run != "" || re.QuietExit != nil || re.Dispatch != nil || re.PR != nil || re.Watch != nil
}

func entry(re rawEntry) (domain.Entry, error) {
	id := strings.TrimSpace(re.ID)
	name := id
	if name == "" {
		name = "a step without an id"
	}
	kinds := 0
	for _, has := range []bool{re.hasStepFields(), re.Parallel != nil, re.Repeat != nil} {
		if has {
			kinds++
		}
	}
	if kinds > 1 {
		return domain.Entry{}, fmt.Errorf("%s: an entry is exactly one of a step, a parallel or a repeat", name)
	}
	switch {
	case re.Parallel != nil:
		p := &domain.Parallel{ID: id, Join: domain.Join(strings.TrimSpace(re.Parallel.Join))}
		for _, b := range re.Parallel.Steps {
			if b.Parallel != nil || b.Repeat != nil {
				return domain.Entry{}, fmt.Errorf("%s: a parallel holds plain steps only", name)
			}
			e, err := entry(b)
			if err != nil {
				return domain.Entry{}, err
			}
			p.Steps = append(p.Steps, *e.Step)
		}
		return domain.Entry{Parallel: p}, nil
	case re.Repeat != nil:
		u := re.Repeat.Until
		rp := &domain.Repeat{
			ID:  id,
			Max: re.Repeat.Max,
			Until: domain.Until{
				Checks:   u.Checks,
				Verdict:  strings.TrimSpace(u.Verdict),
				In:       strings.TrimSpace(u.In),
				Contains: u.Contains,
			},
			OnMax: domain.OnMax(strings.TrimSpace(re.Repeat.OnMax)),
		}
		body, err := entries(re.Repeat.Steps)
		if err != nil {
			return domain.Entry{}, err
		}
		rp.Steps = body
		return domain.Entry{Repeat: rp}, nil
	}
	arts := map[string]string{}
	for k, v := range re.Artifacts {
		arts[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(arts) == 0 {
		arts = nil
	}
	var d *domain.Dispatch
	if rd := re.Dispatch; rd != nil {
		d = &domain.Dispatch{
			Repo: strings.TrimSpace(rd.Repo), Repos: trimAll(rd.Repos), Title: strings.TrimSpace(rd.Title),
			Spec: rd.Spec, Prompt: rd.Prompt,
			Recipe: strings.TrimSpace(rd.Recipe), RecipeYAML: rd.RecipeYAML,
			Envelope: int(rd.Envelope),
		}
	}
	var pr *domain.PRAct
	if re.PR != nil {
		pr = &domain.PRAct{Act: strings.ToLower(strings.TrimSpace(re.PR.Act)), Draft: re.PR.Draft}
	}
	var watch *domain.Watch
	if re.Watch != nil {
		w, err := watchOf(name, *re.Watch)
		if err != nil {
			return domain.Entry{}, err
		}
		watch = &w
	}
	return domain.Entry{Step: &domain.Step{
		PR:            pr,
		Watch:         watch,
		ID:            id,
		Role:          domain.WorkflowRole(strings.ToLower(strings.TrimSpace(re.Role))),
		Prompt:        re.Prompt,
		Slot:          strings.TrimSpace(re.Slot),
		Agent:         strings.ToLower(strings.TrimSpace(re.Agent)),
		Model:         strings.TrimSpace(re.Model),
		StaleTier:     re.Tier != nil,
		StaleEnvelope: re.Envelope != nil,
		Artifacts:     arts,
		CaptureOutput: re.CaptureOutput,
		Run:           strings.TrimSpace(re.Run),
		QuietExit:     re.QuietExit,
		Dispatch:      d,
	}}, nil
}

func watchOf(name string, rw rawWatch) (domain.Watch, error) {
	quiet, err := ParseQuiet(rw.Quiet)
	if err != nil {
		return domain.Watch{}, fmt.Errorf("%s: %w", name, err)
	}
	w := domain.Watch{
		Until: strings.TrimSpace(rw.Until), Trust: strings.TrimSpace(rw.Trust), Max: rw.Max,
		Quiet: quiet, OnClosed: strings.TrimSpace(rw.OnClosed), Checks: trimAll(rw.Checks),
	}
	for ev, rh := range rw.On {
		h := domain.Handler{
			Recipe: strings.TrimSpace(rh.Recipe), RecipeYAML: rh.RecipeYAML,
			Goto: strings.TrimSpace(rh.Goto), Max: rh.Max,
		}
		for _, b := range rh.Steps {
			if b.Parallel != nil || b.Repeat != nil || b.Watch != nil {
				return domain.Watch{}, fmt.Errorf("%s: a handler's inline steps are plain steps", name)
			}
			e, err := entry(b)
			if err != nil {
				return domain.Watch{}, err
			}
			h.Steps = append(h.Steps, *e.Step)
		}
		if strings.TrimSpace(h.RecipeYAML) != "" {
			// a handler recipe that does not parse is Check's to report
			if r, err := Parse([]byte(h.RecipeYAML)); err == nil {
				h.Entries = r.Steps
			}
		}
		if w.On == nil {
			w.On = map[domain.WatchEvent]domain.Handler{}
		}
		w.On[domain.WatchEvent(strings.TrimSpace(ev))] = h
	}
	return w, nil
}

// Marshal writes a recipe back as YAML that Parse reads to the same
// recipe: the copy a run keeps once a planner's revision is spliced in.
func Marshal(r domain.Recipe) ([]byte, error) {
	raw := rawRecipe{
		Name:        r.Name,
		Description: r.Description,
		Inputs:      rawInputs(r),
		Steps:       rawEntries(r.Steps),
	}
	return yaml.Marshal(raw)
}

func rawInputs(r domain.Recipe) map[string]rawInput {
	if len(r.Inputs) == 0 {
		return nil
	}
	out := map[string]rawInput{}
	for name, hint := range r.Inputs {
		in := rawInput{Hint: hint, Required: true}
		if d, ok := r.InputDefaults[name]; ok {
			in.Required, in.Default = false, &d
		}
		out[name] = in
	}
	return out
}

// ResolveInputs is the inputs a run of r is given: every one of given
// that r declares, and each optional one left out as its default. An
// input r does not declare is refused, naming the ones it does, and so is
// a required one left out.
func ResolveInputs(r domain.Recipe, given map[string]string) (map[string]string, error) {
	var unknown, missing []string
	for name := range given {
		if _, ok := r.Inputs[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		declared := "none"
		if len(r.Inputs) > 0 {
			names := make([]string, 0, len(r.Inputs))
			for name := range r.Inputs {
				names = append(names, name)
			}
			sort.Strings(names)
			declared = strings.Join(names, ", ")
		}
		return nil, fmt.Errorf("the recipe declares no input %s (its inputs: %s)", strings.Join(unknown, ", "), declared)
	}
	out := map[string]string{}
	for name := range r.Inputs {
		if v, ok := given[name]; ok {
			out[name] = v
		} else if d, optional := r.InputDefaults[name]; optional {
			out[name] = d
		} else {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		s := ""
		if len(missing) > 1 {
			s = "s"
		}
		return nil, fmt.Errorf("the recipe needs the input%s %s", s, strings.Join(missing, ", "))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// RequiredInputs is the inputs r cannot run without, sorted.
func RequiredInputs(r domain.Recipe) []string {
	var out []string
	for name := range r.Inputs {
		if _, optional := r.InputDefaults[name]; !optional {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// MarshalEntries writes a list of entries as YAML that ParseEntries reads.
func MarshalEntries(es []domain.Entry) ([]byte, error) { return yaml.Marshal(rawEntries(es)) }

func rawEntries(es []domain.Entry) []rawEntry {
	out := []rawEntry{}
	for _, e := range es {
		out = append(out, toRaw(e))
	}
	return out
}

func rawStep(s domain.Step) rawEntry {
	re := rawEntry{
		ID: s.ID, Role: string(s.Role), Prompt: s.Prompt, Slot: s.Slot, Agent: s.Agent, Model: s.Model,
		Artifacts: s.Artifacts, CaptureOutput: s.CaptureOutput,
		Run: s.Run, QuietExit: s.QuietExit,
	}
	if s.PR != nil {
		re.PR = &rawPR{Act: s.PR.Act, Draft: s.PR.Draft}
	}
	if w := s.Watch; w != nil {
		rw := &rawWatch{
			Until: w.Until, Trust: w.Trust, Max: w.Max, Quiet: formatQuiet(w.Quiet),
			OnClosed: w.OnClosed, Checks: w.Checks, On: map[string]rawHandler{},
		}
		for ev, h := range w.On {
			rh := rawHandler{Recipe: h.Recipe, RecipeYAML: h.RecipeYAML, Goto: h.Goto, Max: h.Max}
			for _, st := range h.Steps {
				rh.Steps = append(rh.Steps, rawStep(st))
			}
			rw.On[string(ev)] = rh
		}
		re.Watch = rw
	}
	if d := s.Dispatch; d != nil {
		re.Dispatch = &rawDispatch{
			Repo: d.Repo, Repos: d.Repos, Title: d.Title, Spec: d.Spec, Prompt: d.Prompt,
			Recipe: d.Recipe, RecipeYAML: d.RecipeYAML, Envelope: dollars(d.Envelope),
		}
	}
	return re
}

func toRaw(e domain.Entry) rawEntry {
	switch {
	case e.Parallel != nil:
		p := &rawParallel{Join: string(e.Parallel.Join), Steps: []rawEntry{}}
		for _, s := range e.Parallel.Steps {
			p.Steps = append(p.Steps, rawStep(s))
		}
		return rawEntry{ID: e.Parallel.ID, Parallel: p}
	case e.Repeat != nil:
		u := e.Repeat.Until
		return rawEntry{ID: e.Repeat.ID, Repeat: &rawRepeat{
			Max:   e.Repeat.Max,
			Until: rawUntil{Checks: u.Checks, Verdict: u.Verdict, In: u.In, Contains: u.Contains},
			OnMax: string(e.Repeat.OnMax),
			Steps: rawEntries(e.Repeat.Steps),
		}}
	case e.Step != nil:
		return rawStep(*e.Step)
	}
	return rawEntry{}
}

// Walk calls fn for every plain step of es, in document order, with the
// entries that hold it (outermost first).
func Walk(es []domain.Entry, fn func(s domain.Step, within []domain.Entry)) {
	walk(es, nil, fn)
}

func walk(es []domain.Entry, within []domain.Entry, fn func(domain.Step, []domain.Entry)) {
	for _, e := range es {
		switch {
		case e.Step != nil:
			fn(*e.Step, within)
			if w := e.Step.Watch; w != nil {
				for _, ev := range domain.WatchEvents {
					h := w.On[ev]
					in := append(append([]domain.Entry{}, within...), e)
					for _, hs := range h.Steps {
						fn(hs, in)
					}
					walk(handlerEntries(h), in, fn)
				}
			}
		case e.Parallel != nil:
			in := append(append([]domain.Entry{}, within...), e)
			for _, s := range e.Parallel.Steps {
				fn(s, in)
			}
		case e.Repeat != nil:
			walk(e.Repeat.Steps, append(append([]domain.Entry{}, within...), e), fn)
		}
	}
}

// handlerEntries is the steps of a handler's recipe: the parsed copy when
// the handler has one, else its frozen or inline YAML read now, so a command
// there is seen wherever steps are listed.
func handlerEntries(h domain.Handler) []domain.Entry {
	if len(h.Entries) > 0 || strings.TrimSpace(h.RecipeYAML) == "" {
		return h.Entries
	}
	if r, err := Parse([]byte(h.RecipeYAML)); err == nil {
		return r.Steps
	}
	return nil
}

// Steps lists every plain step of a recipe, in document order.
func Steps(r domain.Recipe) []domain.Step {
	var out []domain.Step
	Walk(r.Steps, func(s domain.Step, _ []domain.Entry) { out = append(out, s) })
	return out
}

// Find returns the plain step with id, if the recipe has one.
func Find(r domain.Recipe, id string) (domain.Step, bool) {
	for _, s := range Steps(r) {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Step{}, false
}

// Summary is what a person reads of a recipe before allowing it to run on
// a schedule: its steps, each command verbatim, and each repository a
// dispatch targets.
type Summary struct {
	Steps    []string // "id · kind", in order
	Commands []string // "id: command"
	Repos    []string // "repo · envelope · recipe"
}

// Summarize reads a recipe's steps, commands and dispatch targets.
func Summarize(r domain.Recipe) Summary {
	var s Summary
	for _, st := range Steps(r) {
		s.Steps = append(s.Steps, st.ID+" · "+st.Kind())
		if st.Run != "" {
			s.Commands = append(s.Commands, st.ID+": "+st.Run)
		}
		if d := st.Dispatch; d != nil {
			with := "a prompt"
			if d.Recipe != "" {
				with = d.Recipe
			}
			target := d.Repo
			if len(d.Repos) > 0 {
				target += " + " + strings.Join(d.Repos, ", ")
			}
			s.Repos = append(s.Repos, fmt.Sprintf("%s · envelope %s · %s", target, domain.FormatDollars(float64(d.Envelope)), with))
			// the frozen recipe runs its commands in that repository
			if child, err := Parse([]byte(d.RecipeYAML)); err == nil && d.RecipeYAML != "" {
				for _, c := range Steps(child) {
					if c.Run != "" {
						s.Commands = append(s.Commands, fmt.Sprintf("%s (in %s): %s: %s", d.Recipe, d.Repo, c.ID, c.Run))
					}
				}
			}
		}
	}
	return s
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}
