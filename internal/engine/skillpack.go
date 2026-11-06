package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/skillpack"
)

// The workspace skill pack (DESIGN §23): delivered to every session at its
// published revision, scoped by the session's repository, and counted as
// sessions invoke it. The curator that writes it is curator.go.

// SkillPack returns the workspace's skill pack. It may not have been made
// yet; Published is then "".
func (e *Engine) SkillPack() *skillpack.Pack {
	e.packOnce.Do(func() {
		e.pack = skillpack.New(e.cfg.Workspace.GummiDir(), e.cfg.Workspace.StateDir())
		e.pack.Missing = e.missingOnBase
	})
	return e.pack
}

// workflowSkills are gummi's own skills a session with workflows off is not
// given.
var workflowSkills = map[string]bool{"workflows": true, "workflow-recipes": true}

// skillDirsFor returns the pack's skill directories that reach f's
// session on ag. key names the conversation and continuing says it is one
// going on (a resume, a reopen, a switch of backend): a conversation keeps
// the revision it was first given (§23.2), a new one takes the published
// revision. A backend that cannot take a skill directory gets none (doctor
// says so), and a pack that cannot be read is no reason to refuse a
// card's work: the session runs without it.
func (e *Engine) skillDirsFor(ctx context.Context, ag agent.Agent, f *domain.Feature, key string, continuing bool) []string {
	if ag == nil || !ag.Capabilities().SkillDirs || e.cfg.Workspace.Root == "" {
		return nil
	}
	// The pack sits beside the person's own skills, never over them.
	prefixed := func(dir string) (string, error) {
		if ag.Capabilities().SkillNamespace {
			return dir, nil
		}
		return skillpack.Prefixed(dir)
	}
	// gummi's own skills come first, and whatever the pack holds: a pack
	// that cannot be read still leaves them.
	// a run's step cards are not sessions: they keep both skills whatever
	// the setting
	wfOn := f.IsChild() || e.WorkflowsOn(ctx, f.ID)
	var dirs []string
	if dir, err := skillpack.Builtin(e.SkillPack().BuiltinDir); err == nil {
		if dir, err = prefixed(dir); err == nil {
			for _, d := range skillpack.DirsFor(dir, f.Repo) {
				if !wfOn && workflowSkills[strings.TrimPrefix(filepath.Base(d), skillpack.Prefix)] {
					continue
				}
				dirs = append(dirs, d)
			}
		}
	}
	dir, _, err := e.SkillPack().SessionDir(ctx, key, string(f.ID), continuing)
	if err != nil || dir == "" {
		return dirs // `gummi curator status` and doctor show a pack that cannot be read
	}
	// The card's own staged changes, over the pinned revision (§23.5).
	if dir, err = e.SkillPack().Overlay(dir, string(f.ID)); err != nil {
		return dirs
	}
	if dir, err = prefixed(dir); err != nil {
		return dirs
	}
	for _, d := range skillpack.DirsFor(dir, f.Repo) {
		// A pack skill a person named like one of gummi's own by hand
		// never stands in for it.
		if !skillpack.IsBuiltin(strings.TrimPrefix(filepath.Base(d), skillpack.Prefix)) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// missingOnBase is the grounding lint's read (§23.3): which of paths the
// base branch of the repository scope names lacks. A repository gummi
// cannot reach says nothing is missing; the lint is advice, not a gate.
func (e *Engine) missingOnBase(ctx context.Context, scope string, paths []string) []string {
	if e.cfg.Pool == nil || len(paths) == 0 {
		return nil
	}
	name := scope
	if scope == skillpack.ScopeDefault {
		name = ""
	}
	m, err := e.cfg.Pool.ManagerForName(ctx, name)
	if err != nil || m == nil {
		return nil
	}
	base := m.BaseBranch(ctx)
	var missing []string
	for _, p := range paths {
		cmd := exec.CommandContext(ctx, "git", "-C", m.RepoRoot(), "cat-file", "-e", base+":"+p) //nolint:gosec // git, with a path from the skill and a branch gummi read
		if cmd.Run() != nil {
			missing = append(missing, p)
		}
	}
	return missing
}

// PackRepos names the repositories a skill's scope may name.
func (e *Engine) PackRepos() []string {
	if e.cfg.Pool == nil {
		return nil
	}
	return e.cfg.Pool.Names()
}

// skillUsage is the pack's use: a file of its own, counted as sessions
// call a skill, because no log keeps a session's tool calls whole — a
// freeform card's never reach the card-event log, and a stage's details
// are pruned when it ends.
type skillUsage struct {
	Skills map[string]skillpack.Use `json:"skills"`
}

func (e *Engine) skillUsagePath() string {
	return filepath.Join(e.cfg.Workspace.StateDir(), "skillusage.json")
}

func (e *Engine) readSkillUsage() skillUsage {
	u := skillUsage{}
	if raw, err := os.ReadFile(e.skillUsagePath()); err == nil {
		_ = json.Unmarshal(raw, &u)
	}
	if u.Skills == nil {
		u.Skills = map[string]skillpack.Use{}
	}
	return u
}

// SkillUsage is every skill's use so far, by name.
func (e *Engine) SkillUsage(context.Context) map[string]skillpack.Use {
	e.usageMu.Lock()
	defer e.usageMu.Unlock()
	return e.readSkillUsage().Skills
}

// packNamespace is the name claude gives the pack's generated plugin.
const packNamespace = "gummi-skills"

// packSkillPathRE matches a read of a pack skill's SKILL.md through the
// places delivery puts it: the materialized revision (its prefixed copy is
// what copilot and opencode read) and claude's generated plugin.
var packSkillPathRE = regexp.MustCompile(`(?:^|/)(?:skillpacks/[0-9a-f]{7,64}/(?:\.prefixed/gummi-)?|gummi-skills/skills/)([a-z0-9][a-z0-9-]{0,63})/SKILL\.md$`)

// skillCalled names the pack skill a tool call used, or "", and whether
// the call was a view: a backend's skill invocation, or (a view) a read of
// a delivered SKILL.md. A pack skill is called gummi-skills:<name> on
// claude and gummi-<name> on copilot and opencode (§23.2); any other name
// is a skill of the person's or the repository's, not the pack's.
func skillCalled(tool, detail string) (name string, view bool) {
	detail = strings.TrimSpace(detail)
	if strings.EqualFold(tool, "skill") {
		if ns, name, ok := strings.Cut(detail, ":"); ok {
			if ns != packNamespace {
				return "", false
			}
			return name, false
		}
		if name, ok := strings.CutPrefix(detail, skillpack.Prefix); ok {
			return name, false
		}
		return "", false
	}
	if m := packSkillPathRE.FindStringSubmatch(detail); m != nil {
		return m[1], true
	}
	return "", false
}

// noteSkillUse counts one tool call of card's session toward the skill it
// used, if any. It is called for every session's tool calls, stage and
// freeform alike; a name the pack does not hold is counted harmlessly and
// never read. gummi's own skills are not the pack's: no count, and no
// question about fixing one a session cannot change.
func (e *Engine) noteSkillUse(card domain.FeatureID, tool, detail string, at time.Time) {
	name, view := skillCalled(tool, detail)
	if name == "" || skillpack.IsBuiltin(name) {
		return
	}
	e.countSkillUseAt(string(card), name, view, at)
}

// countSkillUse counts one use of name by card's session, now.
func (e *Engine) countSkillUse(card, name string, view bool) {
	e.countSkillUseAt(card, name, view, time.Now())
}

// countSkillUseAt counts one use. A use of a skill the card has staged a
// write of is a use of its staged copy, counted apart (§23.5).
func (e *Engine) countSkillUseAt(card, name string, view bool, at time.Time) {
	if e.cfg.Workspace.StateDir() == "" {
		return
	}
	staged := false
	if card != "" {
		for _, s := range e.SkillPack().StagedFor(card) {
			if s.Name == name && s.Pending() && s.Op == skillpack.OpWrite {
				staged = true
			}
		}
	}
	e.usageMu.Lock()
	defer e.usageMu.Unlock()
	e.noteSkillAskLocked(card, name)
	u := e.readSkillUsage()
	use := u.Skills[name]
	switch {
	case staged:
		use.Staged++
	case view:
		use.Views++
	default:
		use.Count++
	}
	if at.After(use.Last) {
		use.Last = at
	}
	u.Skills[name] = use
	raw, err := json.Marshal(u)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(e.skillUsagePath()), 0o700)
	_ = atomicfile.Write(e.skillUsagePath(), raw, 0o600)
}

// PackStanding is every published skill with its use and age.
func (e *Engine) PackStanding(ctx context.Context, now time.Time) ([]skillpack.Status, error) {
	pack := e.SkillPack()
	rev := pack.Published(ctx)
	if rev == "" {
		return nil, nil
	}
	skills, err := pack.Skills(ctx, rev)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(skills))
	for n := range skills {
		names = append(names, n)
	}
	born := pack.Born(ctx, names)
	return skillpack.Standing(skills, born, pack.Edits(ctx, born), e.SkillUsage(ctx), now), nil
}

// SweepPack archives the curator skills the timers have come due on, in
// one commit, and returns their names. A person's or pinned skill is never
// due.
func (e *Engine) SweepPack(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := e.PackStanding(ctx, now)
	if err != nil {
		return nil, err
	}
	var due []string
	for _, s := range rows {
		if s.Due {
			due = append(due, s.Name)
		}
	}
	if len(due) == 0 {
		return nil, nil
	}
	return due, e.SkillPack().Archive(ctx, due, "archive unused for 30 days: "+strings.Join(due, ", "))
}

// releasePackRevisions lets go of the revisions only finished or gone
// cards were reading.
func (e *Engine) releasePackRevisions(ctx context.Context) {
	if e.cfg.Store == nil {
		return
	}
	live := func(card string) bool {
		f, err := e.cfg.Store.GetFeature(ctx, domain.FeatureID(card))
		return err == nil && f.Stage != domain.StageDone
	}
	e.SkillPack().Release(ctx, live)
	e.SkillPack().ReleaseStage(live)
}

// SkillDoc is one skill's SKILL.md as a person reviews it (§23.6): the
// published one, or a card's staged change beside the published one it
// would replace.
type SkillDoc struct {
	Name string
	// Card, Op and Status are a staged change's; empty for a published
	// skill.
	Card, Op, Status string
	// Text is the SKILL.md under review: the published one, or the
	// staged write's. Empty for a staged archive.
	Text string
	// Published is the pack's SKILL.md a staged change replaces or
	// archives; empty for a new skill and for a published read.
	Published string
}

// SkillDoc reads name's SKILL.md: published with card empty, else card's
// staged change to it, pending or rejected. A person's read is not a
// session's: it counts no use.
func (e *Engine) SkillDoc(ctx context.Context, card, name string) (SkillDoc, error) {
	pack := e.SkillPack()
	rev := pack.Published(ctx)
	published := func() string {
		if rev == "" {
			return ""
		}
		md, err := pack.Show(ctx, rev, name)
		if err != nil {
			return ""
		}
		return endLine(md)
	}
	if card == "" {
		md := published()
		if md == "" {
			return SkillDoc{}, fmt.Errorf("no skill %q in the pack", name)
		}
		return SkillDoc{Name: name, Text: md}, nil
	}
	for _, st := range pack.StagedFor(card) {
		if st.Name != name {
			continue
		}
		d := SkillDoc{Name: name, Card: card, Op: st.Op, Status: st.Status, Published: published()}
		if st.Op != skillpack.OpArchive {
			md, ok := pack.StagedFile(card, name, "SKILL.md")
			if !ok {
				return SkillDoc{}, fmt.Errorf("no skill %q staged by %s", name, card)
			}
			d.Text = endLine(md)
		}
		return d, nil
	}
	return SkillDoc{}, fmt.Errorf("no skill %q staged by %s", name, card)
}

// endLine ends md in one newline: the pack's read trims it and a staged
// file keeps it, and a review's diff must not show the difference.
func endLine(md string) string { return strings.TrimRight(md, "\n") + "\n" }

// sessionWorkflowHintFor is the workflow hint for a session on ag, naming
// gummi's own skills on the workflows where ag takes skills (§23.2).
func sessionWorkflowHintFor(ag agent.Agent) string {
	if ag == nil || !ag.Capabilities().SkillDirs {
		return sessionWorkflowHint
	}
	name := func(n string) string {
		if ag.Capabilities().SkillNamespace {
			return packNamespace + ":" + n
		}
		return skillpack.Prefix + n
	}
	return sessionWorkflowHint + " Read the " + name("workflows") + " skill before you propose a run or write done-when checks, and " +
		name("workflow-recipes") + " before you write a recipe."
}
