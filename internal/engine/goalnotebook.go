package engine

// The goal notebook, seen from the engine: where it lives, who is shown
// its index, and which cards stand on an entry that just changed.
// internal/notebook is the thing itself.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/notebook"
)

// goalNotebook opens goal's notebook.
func (e *Engine) goalNotebook(goal domain.FeatureID) *notebook.Notebook {
	return notebook.Open(e.cfg.Workspace.GoalNotebookDir(goal))
}

// GoalNotebook opens goal's notebook for a caller outside the engine — the
// command that puts an owner's reference documents into it.
func (e *Engine) GoalNotebook(goal domain.FeatureID) *notebook.Notebook { return e.goalNotebook(goal) }

// notebookHint is what a session working inside a goal is told about what
// the goal knows: the index, a line per entry, and the rule that makes it
// worth having. Empty for a card on the open board and for a goal that
// knows nothing yet.
func (e *Engine) notebookHint(f domain.Feature) string {
	goal := f.GoalID
	if f.IsGoal() {
		goal = f.ID
	}
	if goal == "" {
		return ""
	}
	idx := e.goalNotebook(goal).Index()
	if idx == "" {
		return ""
	}
	who := "This card is part of goal " + string(goal) + ", which"
	if f.IsGoal() {
		who = "This goal"
	}
	return strings.TrimSpace(fmt.Sprintf(`
%s keeps a notebook of what every card needs and no card owns. Its index:

%s

Read the bodies when your work touches them; they are files. Three rules:
the reference is the owner's and is what the goal was agreed against; a
constant in the registry is decided — use it as written, and if you need
one that is not there, ask (the goal's lead decides it) rather than picking
a value another card may pick differently; and when your work depends on an
entry, cite it in your spec by its key or F-number, so that if it is ever
superseded the cards standing on it can be found. If what you find
contradicts an entry, say so plainly in your spec under a line starting
"FINDING:" with the evidence — do not quietly work around it.`, who, idx))
}

func (lt *leadTurn) notebookRead(goal domain.Feature, a leadArgs) (string, error) {
	nb := lt.e.goalNotebook(goal.ID)
	what := strings.TrimSpace(a.What)
	switch strings.ToLower(what) {
	case "", "index":
		if idx := nb.Index(); idx != "" {
			return idx, nil
		}
		return "the notebook is empty: nothing decided, nothing found, no reference documents", nil
	case "registry":
		return readOr(filepath.Join(nb.Dir(), "REGISTRY.md"), "nothing has been decided yet")
	case "findings":
		return readOr(filepath.Join(nb.Dir(), "FINDINGS.md"), "nothing has been recorded yet")
	}
	for _, r := range nb.Reference() {
		if r.Name == what && !r.Missing {
			raw, err := os.ReadFile(filepath.Join(nb.ReferenceDir(), filepath.FromSlash(r.Name))) //nolint:gosec // a name the notebook itself listed
			if err != nil {
				return "", err
			}
			return clip(string(raw), 24000), nil
		}
	}
	return "", errors.New("read index, registry, findings, or a reference document by the name the index gives it")
}

func readOr(path, empty string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // inside the notebook
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	return string(raw), err
}

var discoveryLineRe = regexp.MustCompile(`(?m)^[ \t>*-]*FINDING:\s*\S`)

// reportedDiscoveries counts the FINDING: lines in a card's spec: what it
// says it found out about the system the goal is building on.
func (e *Engine) reportedDiscoveries(f *domain.Feature) int {
	path := e.artifactFile(f)
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(discoveryLineRe.FindAll(raw, -1))
}

// citingCards names the goal's unfinished cards whose spec cites ref, as
// the tail of a tool result: the moment an entry changes is the moment the
// lead can do something about the cards standing on it.
func citingCards(e *Engine, view GoalView, ref string) string {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(ref) + `($|[^A-Za-z0-9-])`)
	var ids []string
	for _, c := range view.Cards {
		if c.State == goalpolicy.Landed || c.State == goalpolicy.Dropped {
			continue
		}
		f := c.Feature
		path := e.artifactFile(&f)
		if path == "" {
			continue
		}
		if raw, err := os.ReadFile(path); err == nil && re.Match(raw) {
			ids = append(ids, string(f.ID))
		}
	}
	if len(ids) == 0 {
		return ". No unfinished card cites " + ref + "."
	}
	return ". Unfinished cards citing " + ref + ": " + strings.Join(ids, ", ") +
		" — send each a note or send it back (card_send_back) so it is not built on what no longer holds."
}
