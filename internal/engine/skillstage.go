package engine

// skillstage.go is the session half of the pack's staging (DESIGN §23.5):
// the two tools every card session on a backend the pack reaches is
// given, skill_read and skill_manage, and the curator's half — reviewing
// what cards staged, and reading the repositories a staged claim is about.
// The files are skillpack/stage.go's.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/skillpack"
)

const (
	skillManageTool  = "skill_manage"
	stagedReadTool   = "staged_read"
	stagedDecideTool = "staged_decide"
	repoReadTool     = "repo_read"
	repoGrepTool     = "repo_grep"

	repoGrepMaxLines = 200
)

// cardSkillTools are a card session's skill tools.
func cardSkillTools() []agent.ToolDef {
	return []agent.ToolDef{
		{
			Name: skillReadTool,
			Description: "Read one skill of the workspace skill pack: its SKILL.md, or one of its files. " +
				"A skill this card staged is read as staged. Use the bare name: gummi-skills:make-test " +
				"and gummi-make-test are both make-test.",
			Parameters: obj(map[string]any{
				"name": str("The skill's name, without the gummi-skills: or gummi- prefix."),
				"file": str("Optional: a file beside SKILL.md, by relative path (references/x.md)."),
			}, "name"),
		},
		{
			Name: skillManageTool,
			Description: "Improve the workspace skill pack, for this card first. A write (a new skill, or " +
				"the whole new text of an existing one) or an archive is staged: only this card's own " +
				"sessions see it until gummi's curator reviews it and takes it into the pack for every " +
				"session, or rejects it with a reason. Actions: write, archive, withdraw (drop a staged " +
				"change), list (this card's staged changes and the curator's verdicts). A person's or " +
				"pinned skill cannot be changed.",
			Parameters: obj(map[string]any{
				"action":      str("One of \"write\", \"archive\", \"withdraw\", \"list\"."),
				"name":        str("The skill's name, lowercase-dashed, at most 58 characters, without a prefix. Not needed for list."),
				"description": str("write: one line saying what the skill does and when to use it. Backends read this to decide."),
				"scope":       str("write: \"general\"; \"default\" for the workspace's own repository; or the name of the one repository the skill is only for."),
				"body":        str("write: the SKILL.md body, markdown: the rule and how to apply it, not the story of how it was learned."),
				"files":       map[string]any{"type": "object", "description": "write: optional files beside SKILL.md, by relative path.", "additionalProperties": map[string]any{"type": "string"}},
				"why":         str("archive: why the skill should go, in one line."),
			}, "action"),
		},
	}
}

// cardSkillToolsFor is cardSkillTools for a session on ag, or none where
// the pack does not reach ag or there is no workspace to stage in.
func (e *Engine) cardSkillToolsFor(ag agent.Agent) []agent.ToolDef {
	if ag == nil || !ag.Capabilities().SkillDirs || e.cfg.Workspace.Root == "" {
		return nil
	}
	return cardSkillTools()
}

// skillStageHint tells a session it may improve the pack, and how.
func skillStageHintFor(workflows bool) string {
	own := "gummi's own workflows, publishing-prs and\nworkflow-recipes, which ship"
	if !workflows {
		own = "gummi's own publishing-prs, which ships"
	}
	return fmt.Sprintf(skillStageHintFmt, own)
}

const skillStageHintFmt = `The workspace skill pack is the skills named gummi-skills:<name> or
gummi-<name> in your session, beside %s with gummi and cannot be changed here. You
may improve the pack with skill_manage: write
a new skill, rewrite one that is wrong or stale, or archive one that
misleads. Read a skill whole with skill_read before you rewrite it.
What you change is staged for this card: its next session sees it, and
skill_read sees it at once, while no other card does until gummi's
curator reviews it. If a pack skill you followed turned out wrong — a
command that fails, a path or name that moved, a step it misses — fix it
with skill_manage before you finish what you were asked: every later
session would follow it into the same failure. Stage a new lesson only
when a later session in this workspace would pay for it again — a
command that must be run a certain way, a trap in this codebase or
environment — and write the rule, not the story: no card ids, no dates,
no "I tried". A new skill is never required.`

// noteSkillAskLocked records that card's session used the pack skill
// name, for its next turn to ask about once. Called under usageMu.
func (e *Engine) noteSkillAskLocked(card, name string) {
	if card == "" || name == "" {
		return
	}
	if e.skillAsks == nil {
		e.skillAsks = map[string]map[string]bool{}
	}
	if e.skillAsks[card] == nil {
		e.skillAsks[card] = map[string]bool{}
	}
	if _, seen := e.skillAsks[card][name]; !seen {
		e.skillAsks[card][name] = false
	}
}

// takeSkillAsk is the note a card's next turn carries about the pack
// skills its sessions used since it was last asked, or "" when there are
// none or the backend agentName has no skill_manage to act on it. Each
// skill is asked about once per card: a session is not nagged every turn
// about a skill it keeps using. The names it asked about are returned for
// requeueSkillAsk, should the turn not land.
func (e *Engine) takeSkillAsk(card domain.FeatureID, agentName string) (string, []string) {
	if caps, ok := agent.CapabilitiesFor(agentName); !ok || !caps.SkillDirs || e.cfg.Workspace.Root == "" {
		return "", nil
	}
	e.usageMu.Lock()
	defer e.usageMu.Unlock()
	var names []string
	for name, asked := range e.skillAsks[string(card)] {
		if !asked {
			names = append(names, name)
			e.skillAsks[string(card)][name] = true
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return skillAskNote(names), names
}

// requeueSkillAsk puts back the names a turn that did not land asked about.
func (e *Engine) requeueSkillAsk(card domain.FeatureID, names []string) {
	e.usageMu.Lock()
	defer e.usageMu.Unlock()
	for _, n := range names {
		if e.skillAsks[string(card)] != nil {
			e.skillAsks[string(card)][n] = false
		}
	}
}

// skillAskNote asks a session whether the pack skills it used held up.
func skillAskNote(names []string) string {
	shown := make([]string, len(names))
	for i, n := range names {
		shown[i] = packNamespace + ":" + n
	}
	return "(gummi: earlier in this card you used the pack skill " + strings.Join(shown, ", ") +
		". If what you did next showed one of them wrong, stale or missing a step, fix it with " +
		"skill_manage — read it whole with skill_read first — as part of this turn. If it held up, " +
		"carry on and do not mention this note.)"
}

// withSkillAsk appends note to a turn's text, if there is one.
func withSkillAsk(msg, note string) string {
	if note == "" {
		return msg
	}
	return msg + "\n\n" + note
}

// handleSkillTool answers skill_read and skill_manage for a card session.
func (e *Engine) handleSkillTool(s *Session, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	out, err := e.cardSkillCall(context.Background(), string(s.Feature.ID), tc.Name, tc.Args)
	if err != nil {
		out = err.Error()
	}
	e.resolveNow(s, tc.ID, out)
}

func (e *Engine) cardSkillCall(ctx context.Context, card, name string, raw json.RawMessage) (string, error) {
	if e.cfg.Workspace.Root == "" {
		return "", fmt.Errorf("no workspace: this session's engine has no .gummi")
	}
	pack := e.SkillPack()
	var a struct {
		Action, Name, Description, Scope, Body, File, Why string
		Files                                             map[string]string
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	a.Name = bareSkillName(a.Name)
	switch name {
	case skillReadTool:
		return e.readCardSkill(ctx, card, a.Name, a.File)
	case skillManageTool:
		switch a.Action {
		case "write":
			l, err := pack.StageWrite(ctx, card, skillpack.Draft{
				Name: a.Name, Description: a.Description, Scope: a.Scope, Body: a.Body, Files: a.Files,
			}, e.PackRepos())
			if err != nil {
				return "", err
			}
			if !l.OK() {
				return "not staged\n" + l.String(), nil
			}
			return strings.TrimSpace("staged " + a.Name + " for this card: skill_read sees it now, this card's " +
				"next session loads it, and the curator reviews it for every session\n" + l.String()), nil
		case "archive":
			if err := pack.StageArchive(ctx, card, a.Name, a.Why); err != nil {
				return "", err
			}
			return "staged archiving " + a.Name + ": this card's next session runs without it, and the curator reviews it", nil
		case "withdraw":
			if err := pack.Unstage(card, a.Name); err != nil {
				return "", err
			}
			return "withdrew this card's change to " + a.Name, nil
		case "list":
			return stagedList(pack.StagedFor(card)), nil
		}
		return "", fmt.Errorf("action is one of \"write\", \"archive\", \"withdraw\", \"list\" — %q is none of them", a.Action)
	}
	return "", fmt.Errorf("unknown skill tool %q", name)
}

// bareSkillName strips the names a backend shows a pack skill under.
func bareSkillName(name string) string {
	name = strings.TrimSpace(name)
	if n, ok := strings.CutPrefix(name, packNamespace+":"); ok {
		return n
	}
	if n, ok := strings.CutPrefix(name, skillpack.Prefix); ok {
		return n
	}
	return name
}

// readCardSkill reads a skill as card's sessions see it: staged over
// published. It counts as a use of the skill.
func (e *Engine) readCardSkill(ctx context.Context, card, name, file string) (string, error) {
	pack := e.SkillPack()
	for _, st := range pack.StagedFor(card) {
		if st.Name != name || !st.Pending() {
			continue
		}
		if st.Op == skillpack.OpArchive {
			return "", fmt.Errorf("this card staged archiving %q (%s); withdraw that to read it", name, st.Why)
		}
		path := "SKILL.md"
		if file != "" {
			path = file
		}
		out, ok := pack.StagedFile(card, name, path)
		if !ok {
			return "", fmt.Errorf("this card's staged %q has no %s", name, path)
		}
		e.countSkillUse(card, name, false)
		return "(staged by this card, not yet reviewed)\n\n" + out, nil
	}
	rev := pack.Published(ctx)
	if rev == "" {
		return "", fmt.Errorf("the skill pack has no skills yet")
	}
	var out string
	var err error
	if file == "" {
		out, err = pack.Show(ctx, rev, name)
	} else {
		out, err = pack.ShowFile(ctx, rev, name, file)
	}
	if err != nil {
		return "", fmt.Errorf("no skill %q in the pack", name)
	}
	e.countSkillUse(card, name, false)
	return out, nil
}

// stagedList says a card's staged changes and their verdicts.
func stagedList(list []skillpack.Staged) string {
	if len(list) == 0 {
		return "this card has staged nothing"
	}
	var b strings.Builder
	for _, s := range list {
		fmt.Fprintf(&b, "- %s %s: %s", s.Op, s.Name, s.Status)
		if s.Why != "" {
			fmt.Fprintf(&b, " (why: %s)", s.Why)
		}
		if s.Verdict != "" {
			fmt.Fprintf(&b, " — the curator: %s", s.Verdict)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// curatorReviewTools are the curator's tools for staged changes and for
// the repositories they are about.
func curatorReviewTools() []agent.ToolDef {
	return []agent.ToolDef{
		{
			Name: stagedReadTool,
			Description: "Read one change a card staged: the staged SKILL.md and files (for a write) or the " +
				"reason (for an archive), the published skill it changes, and whether the published skill " +
				"moved since it was staged.",
			Parameters: obj(map[string]any{
				"card": str("The card id curator_inputs lists the change under."),
				"name": str("The skill's name."),
			}, "card", "name"),
		},
		{
			Name: stagedDecideTool,
			Description: "Decide one staged change. accept: gummi lints it and commits it to the pack for " +
				"every session (a refused accept changes nothing and says why). merged: you have already " +
				"written what it taught into another skill with skill_write; the change is cleared. " +
				"reject: the change stays with the card, with your reason, and reaches no session.",
			Parameters: obj(map[string]any{
				"card":     str("The card id."),
				"name":     str("The skill's name."),
				"decision": str("One of \"accept\", \"merged\", \"reject\"."),
				"reason":   str("One line: why. The card reads it on a rejection; it is the commit message on an accept."),
			}, "card", "name", "decision", "reason"),
		},
		{
			Name: repoReadTool,
			Description: "Read one file of a repository at its base branch, to check what a skill claims: " +
				"its AGENTS.md, a Makefile, a source file a skill names.",
			Parameters: obj(map[string]any{
				"repo": str("A repository curator_inputs names, or \"default\" for the workspace's own."),
				"path": str("The file's path in the repository."),
			}, "repo", "path"),
		},
		{
			Name:        repoGrepTool,
			Description: "Search a repository's base branch with git grep (extended regex), to check that what a skill names still exists.",
			Parameters: obj(map[string]any{
				"repo":    str("A repository curator_inputs names, or \"default\" for the workspace's own."),
				"pattern": str("An extended regular expression."),
			}, "repo", "pattern"),
		},
	}
}

// reviewDispatch answers the review tools; ok is false for any other.
func (ct *curatorTurn) reviewDispatch(ctx context.Context, name string, raw json.RawMessage) (out string, ok bool, err error) {
	var a struct{ Card, Name, Decision, Reason, Repo, Path, Pattern string }
	switch name {
	case stagedReadTool, stagedDecideTool, repoReadTool, repoGrepTool:
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", true, err
		}
	default:
		return "", false, nil
	}
	switch name {
	case stagedReadTool:
		out, err = ct.readStaged(ctx, a.Card, a.Name)
	case stagedDecideTool:
		out, err = ct.decideStaged(ctx, a.Card, a.Name, a.Decision, a.Reason)
	case repoReadTool:
		out, err = ct.e.repoRead(ctx, a.Repo, a.Path)
	case repoGrepTool:
		out, err = ct.e.repoGrep(ctx, a.Repo, a.Pattern)
	}
	return out, true, err
}

func stagedEntry(pack *skillpack.Pack, card, name string) (skillpack.Staged, error) {
	for _, s := range pack.StagedFor(card) {
		if s.Name == name && s.Pending() {
			return s, nil
		}
	}
	return skillpack.Staged{}, fmt.Errorf("card %s has no change to %q waiting for review", card, name)
}

func (ct *curatorTurn) readStaged(ctx context.Context, card, name string) (string, error) {
	pack := ct.e.SkillPack()
	st, err := stagedEntry(pack, card, name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	rev := pack.Published(ctx)
	if st.Op == skillpack.OpArchive {
		fmt.Fprintf(&b, "Card %s asks to archive %s, because: %s\n", card, name, st.Why)
	} else {
		d, err := pack.StagedDraft(card, name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Card %s staged this %s:\n\n", card, name)
		md, _ := pack.StagedFile(card, name, "SKILL.md")
		b.WriteString(md)
		files := make([]string, 0, len(d.Files))
		for rel := range d.Files {
			files = append(files, rel)
		}
		sort.Strings(files)
		for _, rel := range files {
			fmt.Fprintf(&b, "\n--- %s ---\n%s\n", rel, d.Files[rel])
		}
	}
	published := ""
	if rev != "" {
		published, _ = pack.Show(ctx, rev, name)
	}
	if published == "" {
		b.WriteString("\nThe pack has no published skill of this name: it is new.\n")
	} else {
		fmt.Fprintf(&b, "\nThe published %s:\n\n%s\n", name, published)
		if st.Base != "" && st.Base != rev {
			if then, _ := pack.Show(ctx, st.Base, name); then != published {
				b.WriteString("\nThe published skill has changed since the card staged this: weigh both.\n")
			}
		}
	}
	return clip(b.String(), curatorMaxInputLen), nil
}

func (ct *curatorTurn) decideStaged(ctx context.Context, card, name, decision, reason string) (string, error) {
	e := ct.e
	pack := e.SkillPack()
	st, err := stagedEntry(pack, card, name)
	if err != nil {
		return "", err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("say why, in one line")
	}
	switch decision {
	case "accept":
		msg := curatorCommitPrefix(ct.run.ID) + "accept " + st.Op + " " + name + " staged by " + card + ": " + reason
		if st.Op == skillpack.OpArchive {
			if err := pack.Archive(ctx, []string{name}, msg); err != nil {
				return "", err
			}
		} else {
			d, err := pack.StagedDraft(card, name)
			if err != nil {
				return "", err
			}
			l, err := pack.Write(ctx, d, e.PackRepos(), msg)
			if err != nil {
				return "", err
			}
			if !l.OK() {
				return "not accepted\n" + l.String(), nil
			}
		}
		if err := pack.Unstage(card, name); err != nil {
			return "", err
		}
		ct.mu.Lock()
		if st.Op == skillpack.OpArchive {
			ct.run.Archived = append(ct.run.Archived, name)
		} else {
			ct.run.Written = append(ct.run.Written, name)
		}
		ct.run.Accepted = append(ct.run.Accepted, card+"/"+name)
		ct.mu.Unlock()
		ct.checkpoint(ctx)
		return "accepted and published", nil
	case "merged":
		if err := pack.Unstage(card, name); err != nil {
			return "", err
		}
		ct.mu.Lock()
		ct.run.Accepted = append(ct.run.Accepted, card+"/"+name)
		ct.mu.Unlock()
		ct.checkpoint(ctx)
		return "cleared as merged", nil
	case "reject":
		if err := pack.Reject(card, name, reason); err != nil {
			return "", err
		}
		ct.mu.Lock()
		ct.run.Rejected = append(ct.run.Rejected, card+"/"+name)
		ct.mu.Unlock()
		ct.checkpoint(ctx)
		return "rejected; the card can read why", nil
	}
	return "", fmt.Errorf("decision is one of \"accept\", \"merged\", \"reject\" — %q is none of them", decision)
}

// writeStagedCards is curator_inputs' list of staged changes.
func (ct *curatorTurn) writeStagedCards(ctx context.Context, b *strings.Builder) {
	all := ct.e.SkillPack().AllStaged()
	cards := make([]string, 0, len(all))
	n := 0
	for c, list := range all {
		cards = append(cards, c)
		n += len(list)
	}
	slices.Sort(cards)
	fmt.Fprintf(b, "Changes card sessions staged, waiting for your review (%d; read each with staged_read, decide with staged_decide):\n", n)
	if n == 0 {
		b.WriteString("(none)\n")
	}
	for _, c := range cards {
		title := ""
		if ct.e.cfg.Store != nil {
			if f, err := ct.e.cfg.Store.GetFeature(ctx, domain.FeatureID(c)); err == nil {
				title = " " + f.Name()
				if f.Repo != "" {
					title += " (repository " + f.Repo + ")"
				}
			}
		}
		for _, s := range all[c] {
			fmt.Fprintf(b, "- %s%s: %s %s\n", c, title, s.Op, s.Name)
		}
	}
	b.WriteString("\n")
}

// repoRoot resolves a scope-style repository name to its checkout and
// base branch.
func (e *Engine) repoRoot(ctx context.Context, repo string) (root, base string, err error) {
	if e.cfg.Pool == nil {
		return "", "", fmt.Errorf("no repositories to read")
	}
	name := strings.TrimSpace(repo)
	if name == skillpack.ScopeDefault {
		name = ""
	} else if !slices.Contains(e.PackRepos(), name) {
		return "", "", fmt.Errorf("no repository %q: name one curator_inputs lists, or \"default\"", repo)
	}
	m, err := e.cfg.Pool.ManagerForName(ctx, name)
	if err != nil || m == nil {
		return "", "", fmt.Errorf("repository %q cannot be read: %v", repo, err)
	}
	return m.RepoRoot(), m.BaseBranch(ctx), nil
}

func (e *Engine) repoRead(ctx context.Context, repo, path string) (string, error) {
	root, base, err := e.repoRoot(ctx, repo)
	if err != nil {
		return "", err
	}
	path = strings.TrimPrefix(strings.TrimSpace(path), "/")
	if path == "" || strings.Contains(path, "..") {
		return "", fmt.Errorf("no file %q", path)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "show", base+":"+path).Output()
	if err != nil {
		return "", fmt.Errorf("%s has no %s on %s", repo, path, base)
	}
	return clip(string(out), curatorMaxInputLen), nil
}

func (e *Engine) repoGrep(ctx context.Context, repo, pattern string) (string, error) {
	root, base, err := e.repoRoot(ctx, repo)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("an empty pattern finds everything")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "grep", "-n", "-I", "-E", "-e", pattern, base, "--")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "no match on " + base, nil
		}
		return "", fmt.Errorf("git grep: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	note := ""
	if len(lines) > repoGrepMaxLines {
		note = fmt.Sprintf("\n(%d more matches; narrow the pattern)", len(lines)-repoGrepMaxLines)
		lines = lines[:repoGrepMaxLines]
	}
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, base+":")
	}
	return clip(strings.Join(lines, "\n")+note, curatorMaxInputLen), nil
}
