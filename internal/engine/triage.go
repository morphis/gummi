package engine

// Triage: a session's first request is read once, before its agent hears
// it, by a cheap tool-less one-shot that says whether it is work for one
// of the workspace's workflow recipes. An agent asked to judge that for
// itself, mid-request, mostly just starts on the work; a reader whose
// only job is the question answers it. When the answer is a recipe, the
// agent is told so ahead of the request and the thread says so to the
// person. It decides nothing: the agent still proposes the run, and the
// person still allows it (decision 28). The same one-shot names the
// session: a session minted from a sentence is titled with its first
// one, and triage's NAME line retitles it, and moves its branch to the
// words, before anyone outside this repository knows the old name
// (sessionname.go). Best effort — a triage that fails or runs out of
// time sends the request as it was, under the name it was minted with.
// A session with workflows off (workflowsoptin.go) is shown no recipes and
// only named.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

const (
	// triageTimeout bounds the one-shot; the first turn waits on it.
	triageTimeout = 90 * time.Second
	// triageMinWords is the shortest request triage reads: a line shorter
	// than this is a greeting or a small ask, never a run.
	triageMinWords = 8
	// triageRequestMax is how much of the request the one-shot is shown.
	triageRequestMax = 6000
	// triageHere is the answer for work the session does itself.
	triageHere = "here"
	// triageSystemHint is the one-shot's whole instruction beside its
	// prompt.
	triageSystemHint = "You triage a request made to a coding agent. Do not modify any file and do not run anything: " +
		"answer from what you are given."
)

// wantsTriage reports whether msg, about to go to the session as a turn,
// is read by triage first: the engine has it on, a person sent it, it is
// the conversation's first turn, it is a request of some length and not
// a command, and the card is a session that may run workflows or still carries its
// minted title (which triage may name). Asked
// before the turn is appended to the transcript.
func (ff *FreeformSession) wantsTriage(ctx context.Context, sess *Session, cmds []ProjectCommand, msg string) bool {
	if !ff.engine.cfg.Triage {
		return false
	}
	if a := actorOf(ctx); a != "" && !state.IsPersonActor(a) {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(msg), "/") {
		return false
	}
	if _, _, ok := FindProjectCommand(cmds, msg); ok {
		return false
	}
	if len(strings.Fields(msg)) < triageMinWords {
		return false
	}
	for _, m := range sess.Snapshot().Transcript {
		if m.Author == AuthorUser {
			return false
		}
	}
	f, err := ff.engine.feature(ctx, ff.id)
	if err != nil || f.IsChild() || f.Home {
		return false
	}
	if !ff.engine.WorkflowsOn(ctx, ff.id) {
		// with workflows off triage only names the session: nothing to
		// do once the title is no longer the one the mint derived
		derived, _, _ := domain.SplitFreeform(msg)
		return derived == f.Title
	}
	return true
}

// triage reads msg against the workspace's recipes and returns the note
// the agent is sent ahead of it: "" when the request is the session's own
// work under the name it has, or when the triage could not answer. When
// it names a recipe, or renames the session, the thread gets a line
// saying so. Its cost goes on the card.
func (ff *FreeformSession) triage(ctx context.Context, sess *Session, msg string) string {
	e := ff.engine
	var recipes []WorkflowRecipe
	if e.WorkflowsOn(ctx, ff.id) {
		all, err := e.WorkflowRecipes()
		if err != nil {
			return ""
		}
		for _, r := range all {
			if r.Err == "" {
				recipes = append(recipes, r)
			}
		}
		if len(recipes) == 0 {
			return ""
		}
	}
	f, err := e.feature(ctx, ff.id)
	if err != nil {
		return ""
	}
	ff.mu.Lock()
	rc, backend, workDir := ff.rc, ff.backend, ff.workDir
	ff.mu.Unlock()
	trc, tbackend := e.resolveOneShotRole(f.Profile, rc, backend,
		agent.RoleTriage, agent.Role(domain.RoleScout), agent.RoleScribe)
	ag, err := e.sessionAgent(tbackend)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, triageTimeout)
	defer cancel()
	ts, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:     workDir,
		Role:        agent.RoleTriage,
		Model:       trc.Model,
		Permission:  e.cfg.Permission,
		ReadOnly:    ConsultConfined(ag.Capabilities()),
		SystemHints: []string{triageSystemHint},
		FeatureID:   string(ff.id),
		// No Tools and no MCP endpoint: its product is the one line.
	})
	if err != nil {
		return ""
	}
	defer func() { _ = ts.Close() }()
	if err := ts.Send(ctx, triagePrompt(msg, recipes)); err != nil {
		return ""
	}
	reply, err := collectOneShot(ctx, e, ff, ts, "triage")
	if err != nil {
		return ""
	}
	var notes []string
	if note := ff.nameFromTriage(ctx, sess, f, msg, reply); note != "" {
		notes = append(notes, note)
	}
	if len(recipes) == 0 {
		return strings.Join(notes, "\n\n")
	}
	name, why, ok := parseTriage(reply, recipes)
	if ok && name != triageHere {
		sess.appendToolDone("gummi triage: work for the "+name+" workflow", true, fmt.Sprintf(
			"This reads as work for the %s workflow (%s). The agent was told so: it may propose the run, and you allow it or not.",
			name, why))
		e.persist(sess)
		e.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
		notes = append(notes, triageNote(name, why))
	}
	return strings.Join(notes, "\n\n")
}

// nameFromTriage renames the session after the NAME line triage gave,
// when its title is still the one its mint took from this request — a
// title anyone chose since is kept. It returns the note the agent hears
// when its branch moved, and "" otherwise.
func (ff *FreeformSession) nameFromTriage(ctx context.Context, sess *Session, f domain.Feature, msg, reply string) string {
	title := parseTriageName(reply)
	if title == "" {
		return ""
	}
	if derived, _, _ := domain.SplitFreeform(msg); derived != f.Title {
		return ""
	}
	before := f.BranchName()
	n, err := ff.engine.RenameSession(ctx, ff.id, title)
	if err != nil || n.Title == f.Title {
		return ""
	}
	sess.appendToolDone("gummi named this session", true, nameReport(n))
	ff.engine.persist(sess)
	ff.engine.send(Event{Feature: ff.id, Stage: domain.StageOpen, Kind: EventUpdated})
	if n.Branch == before {
		return ""
	}
	return renameNote(n)
}

func triagePrompt(msg string, recipes []WorkflowRecipe) string {
	var b strings.Builder
	if len(recipes) == 0 {
		b.WriteString("A person has just sent a coding agent the first message of a session, below. " +
			"Name the session the way a person titles a pull request: two to six words naming the work itself, " +
			"not the sentence that asked for it, with no ticket numbers, quotes or trailing punctuation. " +
			"Answer with exactly one line:\n" +
			"NAME: <the name>\n\n")
		b.WriteString("The request:\n<<<\n" + tailRunes(strings.TrimSpace(msg), triageRequestMax) + "\n>>>")
		return b.String()
	}
	b.WriteString("A person has just sent a coding agent the first message of a session, below. The agent works " +
		"in a checkout of the person's repository. It can do the work itself, in the conversation, or propose a " +
		"workflow run: a recipe of fresh-context agents that do the work in its checkout and are judged against " +
		"checks at the end. A run costs several times what the conversation does, and buys a design, independent " +
		"review and validation.\n\n")
	b.WriteString("The recipes:\n")
	for _, r := range recipes {
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, strings.TrimSpace(r.Description))
	}
	b.WriteString("\nSay which fits the request.\n" +
		"- " + triageHere + ": a question or an explanation, a small or local change, a fix whose cause is known, " +
		"work the person is still shaping, anything a careful agent finishes in one sitting.\n" +
		"- a recipe: a feature or change across several parts of the code, a risky refactor, or work with tests " +
		"and docs to write (deliver); a question to answer with a written report (investigate); checks that " +
		"already fail and need to pass (fix-until-green); any other recipe by its description.\n" +
		"Name a recipe only when the request itself shows the work is that size; do not guess what it may grow into.\n\n")
	b.WriteString("Answer with exactly two lines. The first is in one of these forms:\n" +
		"TRIAGE: " + triageHere + " — <why, in a few words>\n" +
		"TRIAGE: <recipe name> — <why, in a few words>\n\n")
	b.WriteString("The second names the session the way a person titles a pull request: two to six " +
		"words naming the work itself, not the sentence that asked for it, with no ticket numbers, quotes or " +
		"trailing punctuation:\n" +
		"NAME: <the name>\n\n")
	b.WriteString("The request:\n<<<\n" + tailRunes(strings.TrimSpace(msg), triageRequestMax) + "\n>>>")
	return b.String()
}

// parseTriage reads the one-shot's TRIAGE line: "here", or the name of
// one of recipes, and the reason. A name that is no recipe, or no line
// at all, is no answer.
func parseTriage(reply string, recipes []WorkflowRecipe) (name, why string, ok bool) {
	for _, line := range strings.Split(reply, "\n") {
		rest, found := strings.CutPrefix(strings.TrimLeft(strings.TrimSpace(line), "*`"), "TRIAGE:")
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		word, tail := rest, ""
		if i := strings.IndexAny(rest, " \t—–("); i >= 0 {
			word, tail = rest[:i], rest[i:]
		}
		word = strings.ToLower(strings.Trim(word, "`*'\".,:;-"))
		why = strings.TrimSpace(strings.Trim(strings.TrimSpace(tail), "—–-:*`()"))
		if why == "" {
			why = "no reason given"
		}
		if word == triageHere {
			return triageHere, why, true
		}
		for _, r := range recipes {
			if strings.EqualFold(r.Name, word) {
				return r.Name, why, true
			}
		}
		return "", "", false
	}
	return "", "", false
}

// triageNameMax bounds the name triage may give: longer than this is a
// sentence again, and the name the mint gave stands.
const triageNameMax = 60

// parseTriageName reads the one-shot's NAME line: "" when there is none,
// or it is empty or runs long.
func parseTriageName(reply string) string {
	for _, line := range strings.Split(reply, "\n") {
		rest, found := strings.CutPrefix(strings.TrimLeft(strings.TrimSpace(line), "*`"), "NAME:")
		if !found {
			continue
		}
		name := strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(rest), "*`'\"“”.")), " ")
		if name == "" || len(name) > triageNameMax {
			return ""
		}
		return name
	}
	return ""
}

// triageNote is what the agent reads ahead of the request triage sent to
// a recipe.
func triageNote(name, why string) string {
	return fmt.Sprintf("(gummi's triage read this request as work for the %s workflow recipe: %s. "+
		"If you agree, do not start on the work here: write the spec it runs against (spec_start, then its "+
		"sections), run its done-when checks so you know they fail, and propose the run with workflow_run, "+
		"saying in one sentence why. If the request or what you find says otherwise, do the work here and tell "+
		"the person in one line why not a run.)", name, why)
}
