package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/spec"
)

// This file is the engine's half of the re-entry: the one model turn
// that reads a human sentence and names what kind of complaint it is,
// and the artifact write a route performs before it moves the card.
// Where the card then goes is not decided here — that is
// internal/reentry, compiled in and pure, so the same routing serves the
// TUI, the web surface and the headless driver (DESIGN §6.3: the
// options are deterministic even when the narration and the
// classification are not).

// ErrNoClassifier reports that there is no backend to run the
// classification turn on at all — no agent configured, or none the
// profile's scribe role resolves to.
//
// It is deliberately distinct from a reply that classified nothing. A
// caller must tell the two apart: an unreadable SENTENCE becomes a turn
// (DESIGN §6.3's safety property — prose is always accepted and always
// safe), but an unreachable CLASSIFIER is not a statement about the
// sentence at all, and turning "send it back" into a chat message
// because a model was offline would silently drop an answer the screen
// had already offered. On this error a caller falls back to the fixed
// route its own row declares, which is what the surface did before this
// existed.
var ErrNoClassifier = errors.New("no agent backend available to classify the re-entry")

// classifyIntro opens the classification turn. The sentence itself is
// appended verbatim, fenced, so a line that happens to contain the word
// "INTENT" cannot pose as the answer.
const classifyIntro = `Someone reviewing this card typed one sentence saying what they want
different about it. Your only job is to say which ONE kind of complaint
it is. You are not fixing anything and you must not modify any file.`

// classifyEvidence names what to read before answering. It is the
// read-only three the design asks for, in the form a one-shot session
// has them: the card's own status is handed over inline (below), the
// artifact is on disk at a path the session is explicitly allowed to
// read, and the diff is the branch the session's working directory
// already is. Wiring the workspace MCP scope in to serve the same three
// facts as tool calls would cost a socket, a server and several
// round trips to deliver strictly less than this.
const classifyEvidence = `Before answering, read the card's artifact and the branch's own diff
(git diff against the base branch, and git log). Judge the sentence
against what the artifact actually asks for and what the code actually
does — the distinction between "the plan never asked for this" and "the
code does not do what the plan asked" is the whole question.`

// classifyClose is the reply contract. One line, one word, nothing else
// — the same shape estimate and discover ask for, and for the same
// reason: a machine-readable answer that a prose paragraph cannot
// accidentally satisfy.
const classifyClose = `Reply with exactly one line and nothing else:

INTENT: <one of the words above>

If the sentence fits none of them, or you cannot tell which, reply
INTENT: none — a wrong guess routes the card to the wrong stage, and
saying nothing is always the safer answer.`

// intentRe pulls the answer word out of the reply. Every match is
// collected and the last wins, matching parseScribeEstimate: a model
// that corrects itself mid-reply means the correction.
var intentRe = regexp.MustCompile(`(?im)^\s*INTENT:\s*` + "`?" + `([A-Za-z_ -]+)` + "`?" + `\s*$`)

// classifyPrompt renders the full turn for one sentence at one stage.
// The vocabulary is read from internal/reentry rather than written out
// here, so the words the model is offered and the words the router
// switches on cannot drift apart.
func classifyPrompt(f domain.Feature, sentence string) string {
	var b strings.Builder
	b.WriteString(classifyIntro + "\n\n")
	fmt.Fprintf(&b, "The card is a %s, currently at the %s stage.\n\n", kindNoun(f.Kind), f.Stage)
	b.WriteString("The kinds, exactly one of which is the answer:\n\n")
	for _, i := range reentry.Vocabulary() {
		fmt.Fprintf(&b, "- %s — %s\n", i, reentry.Describe(i))
	}
	b.WriteString("\n" + classifyEvidence + "\n\nThe sentence:\n\n```\n")
	b.WriteString(strings.TrimSpace(sentence) + "\n```\n\n")
	b.WriteString(classifyClose)
	return b.String()
}

// noun names a kind for the classifier's prompt.
func kindNoun(k domain.Kind) string {
	switch k {
	case domain.KindBug:
		return "bug"
	case domain.KindResearch:
		return "research card"
	default:
		return "feature"
	}
}

// parseIntentReply extracts the classified intent from a reply, or
// ("", false) when the reply names nothing in the vocabulary — the
// explicit "none", a paragraph of prose, or a word that is not one of
// the six.
func parseIntentReply(text string) (reentry.Intent, bool) {
	m := intentRe.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return "", false
	}
	return reentry.ParseIntent(m[len(m)-1][1])
}

// ClassifyReentry runs one cheap scribe-role pass over the card and
// returns which kind of complaint a typed sentence is.
//
// It is the only model call the re-entry makes, and it is deliberately
// the smallest one available: the scribe tier is the cheap one-shot
// tier a profile can point at a small model (hints.go), the session is
// read-only, and the reply is one word. The transient session is not
// tracked on the board, exactly like Estimate and DiscoverChecks.
//
// The two failure modes are different answers, not one:
//
//   - ("", nil) — the turn ran and classified nothing. The sentence is
//     unreadable, and the caller sends it as a turn.
//   - ("", err) — the turn could not run. The caller falls back to the
//     route its own row declares; see ErrNoClassifier.
func (e *Engine) ClassifyReentry(ctx context.Context, f domain.Feature, sentence string) (reentry.Intent, error) {
	if strings.TrimSpace(sentence) == "" {
		return "", nil
	}
	rc, backend := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return "", ErrNoClassifier
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return "", err
	}
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:      workDir,
		ArtifactPath: specPath,
		Role:         agent.RoleScribe,
		Model:        rc.Model,
		Provider:     rc.Provider,
		Think:        rc.Think,
		Permission:   e.cfg.Permission,
		SystemHints: []string{
			"You are classifying one sentence read-only; do not modify any file.",
			fmt.Sprintf("The card's artifact is at %s.", specPath),
		},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, classifyPrompt(f, sentence)); err != nil {
		return "", err
	}
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				intent, _ := parseIntentReply(text.String())
				return intent, nil
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle, agent.EventBudgetExhausted:
				// Budget exhaustion is a soft stop here for the same
				// reason it is in DiscoverChecks: the in-flight reply is
				// done and no further turn will run, so waiting for an
				// idle that is not coming would hang the caller.
				intent, _ := parseIntentReply(text.String())
				return intent, nil
			case agent.EventError:
				return "", ev.Err
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// ApplyReentryEdit writes a re-entry's artifact edit into the card's
// artifact, as an open `%%` user marker under the section the router
// named.
//
// This is the step that makes a rewind worth taking. A note carried
// into a kickoff is gone the moment the stage ends: the artifact still
// asks for the wrong thing and verify still has no check for the miss,
// which is the failure the whole re-entry exists to fix. Writing it
// into the document instead means the next stage reads it as part of
// its own contract — and, because an unresolved user marker holds every
// gate shut (spec.UserOpenThreads, gatepolicy.Decide's own check above
// its stage switch), it also cannot be crossed past again without
// somebody answering it.
//
// It fails rather than falling back. A section heading the artifact
// does not carry means the edit would land somewhere nobody asked for,
// and a silent write to the top of the document is exactly the
// "recorded it somewhere" outcome this replaces — the caller's own rule
// is that a rewind without its edit does not happen at all.
//
// Same lock + atomic-write path as the ask protocol's own writes
// (asktool.go's handleAnnotate), so a concurrent stage write cannot
// tear the artifact and cannot lose either edit.
func (e *Engine) ApplyReentryEdit(f domain.Feature, ed reentry.Edit) error {
	if ed.Empty() {
		return nil
	}
	path := e.artifactFile(&f)
	if path == "" {
		return fmt.Errorf("%s has no artifact to record the re-entry in", f.ID)
	}
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read the artifact: %w", err)
	}
	line, ok := spec.HeadingLine(string(raw), ed.Section)
	if !ok {
		return fmt.Errorf("the artifact has no %q section to record this under", ed.Section)
	}
	out, err := spec.AddComment(string(raw), line, "user", e.now().Format("2006-01-02"), ed.Text)
	if err != nil {
		return fmt.Errorf("could not record the re-entry: %w", err)
	}
	if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("could not write the artifact: %w", err)
	}
	return nil
}
