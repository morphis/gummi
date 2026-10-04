// Package branchlog is the pure read model behind a card's log: its own
// commits as rows a surface draws, and the one rule for whether they may
// be rewritten. The TUI's log tab, the web page's, and `gummi log` all
// read it, so no two of them can disagree about what a commit is or
// whether the card's history is still the card's to change.
//
// Nothing here runs git: a caller gathers worktree.LogEntry (Manager.Log)
// and the facts Refusal asks about, and the answer is a value.
package branchlog

import (
	"regexp"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// Row is one commit of the card's branch, oldest first in a Log.
type Row struct {
	SHA     string
	Short   string
	Subject string
	Body    string
	Author  string
	At      time.Time
	Files   int
	Add     int
	Del     int
	// Checkpoint marks a commit gummi made itself between turns and
	// stages ("FD-009: implement checkpoint") rather than one somebody
	// wrote as a commit: the ones a squash is usually for.
	Checkpoint bool
	// Pushed marks a commit the branch's upstream already has.
	Pushed bool
	// Warning names agent-authorship metadata in the message, the same
	// match the landing message is scrubbed for.
	Warning string
}

// Log is a card's branch history and what may be done to it.
type Log struct {
	Rows []Row
	// Why is the reason the history is not rewritable, empty when it is.
	Why string
	// PushCommand is what publishes a rewritten branch over the pushed
	// one. gummi never runs it.
	PushCommand string
}

// the goal path commits "final checkpoint" with no card id in front
var checkpointRe = regexp.MustCompile(`^[A-Z]{2,}-\d+: (.+ )?checkpoint$|^[A-Z]{2,}-\d+: dropped by its goal$|^final checkpoint$`)

// IsCheckpoint reports whether subject is one gummi's own checkpoint
// commits carries.
func IsCheckpoint(subject string) bool { return checkpointRe.MatchString(subject) }

// Rows projects the worktree's entries.
func Rows(entries []worktree.LogEntry) []Row {
	rows := make([]Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, Row{
			SHA: e.SHA, Short: e.Short, Subject: e.Subject, Body: e.Body,
			Author: e.Author, At: e.At, Files: e.Files, Add: e.Add, Del: e.Del,
			Checkpoint: IsCheckpoint(e.Subject), Pushed: e.Pushed,
			Warning: worktree.MatchesAttribution(e.Message()),
		})
	}
	return rows
}

// State is what a caller knows about the card that is not on the card.
type State struct {
	// Busy: an agent session holds the card (a stage run, a freeform
	// turn). A rewrite under it would pull commits out from beneath the
	// checkpoint the session is about to make.
	Busy bool
	// Landed: the branch's work is already on its base.
	Landed bool
}

// Refusal is why card f's history may not be rewritten, or "" when it may.
// It is the single rule: the surfaces show it in their own words and the
// driver returns it as its error, and Manager.Rewrite still checks the
// worktree's own preconditions (clean, no rebase in flight) underneath.
func Refusal(f domain.Feature, s State) string {
	switch {
	case f.Kind == domain.KindResearch:
		// not "yet": a research card never gets one
		return string(f.ID) + " is a research card: it works in a scratch tree and never gets a branch"
	case f.Stage == domain.StageTodo:
		return string(f.ID) + " has no branch yet"
	case f.Kind == domain.KindGoal:
		return string(f.ID) + " is a goal: its branch is built from its cards' landings, not from commits to rewrite"
	case f.Adopted():
		// held, never owned (DESIGN §10 D22): gummi does not record where
		// its own commits on an adopted branch begin, so none are offered
		return string(f.ID) + " is on a branch gummi did not cut — its history is not gummi's to rewrite"
	case s.Landed:
		return string(f.ID) + " has already landed — its commits are on the base"
	case s.Busy:
		return string(f.ID) + " has an agent working — rewrite once it has stopped"
	}
	return ""
}

// PlanGroups turns the reader's choices into a worktree plan: for each
// commit, whether it is squashed into the one before it, and the message
// it carries when the reader changed it. It is what both faces do with
// their controls, in one place.
//
// squashed[i] folds commit i into the group of commit i-1 (the first
// commit cannot be folded). messages maps a group's first commit to its
// new message; a group that folds several commits and has none takes the
// message of its first, which the caller shows so the reader sees what
// they are about to get.
func PlanGroups(rows []Row, squashed map[string]bool, messages map[string]string) []worktree.RewriteGroup {
	var groups []worktree.RewriteGroup
	for i, r := range rows {
		if i > 0 && squashed[r.SHA] {
			g := &groups[len(groups)-1]
			g.Commits = append(g.Commits, r.SHA)
			continue
		}
		groups = append(groups, worktree.RewriteGroup{Commits: []string{r.SHA}, Message: strings.TrimSpace(messages[r.SHA])})
	}
	for i := range groups {
		if len(groups[i].Commits) > 1 && groups[i].Message == "" {
			for _, r := range rows {
				if r.SHA == groups[i].Commits[0] {
					groups[i].Message = joinMessage(r)
				}
			}
		}
	}
	return groups
}

func joinMessage(r Row) string {
	if r.Body == "" {
		return r.Subject
	}
	return r.Subject + "\n\n" + r.Body
}
