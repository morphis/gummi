package ui

import "strings"

// verbKind classifies one line of the thread's input, per parseInput's
// doc comment.
type verbKind int

const (
	verbNone    verbKind = iota // ordinary prose — a message to the agent
	verbCommand                 // the first word matched the closed vocabulary
	verbMenu                    // "/" or "/<filter>" — open the command menu
)

// verbs is the closed, case-insensitive vocabulary parseInput matches
// against a line's first word. This is the whole set — nothing outside it
// is ever recognised, and nothing inside it is ever fuzzy-matched.
var verbs = map[string]bool{
	"approve":   true,
	"changes":   true,
	"diff":      true,
	"spec":      true,
	"bounce":    true,
	"verify":    true,
	"autopilot": true,
	"park":      true,
	"land":      true,
	"rebase":    true,
	"squash":    true,
	"clean":     true,
}

// parsedInput is parseInput's result.
type parsedInput struct {
	Kind verbKind
	// Verb is the canonical lower-case verb; empty unless Kind ==
	// verbCommand.
	Verb string
	// Remainder is everything after the first word (verbCommand), or
	// after the leading "/" (verbMenu), trimmed; empty when there is
	// nothing left over.
	Remainder string
	// Text is the original line, trimmed, for the message path
	// (verbNone) — empty only when the line was itself empty/whitespace.
	Text string
}

// parseInput classifies one line typed into the thread's input. It is
// pure — no side effects, no knowledge of the engine, the board, or a
// card — which is what makes the whole vocabulary exhaustively
// table-tested (verbs_test.go) without a Shell, an engine, or a fake
// agent anywhere in sight. Routing a verbCommand to an action, deciding
// whether it needs a confirm chip, and opening the command menu for a
// verbMenu are all callers' concerns (threadinput.go, shell.go's
// boardVerb) — parseInput only says what kind of line this was.
//
// Rules:
//   - leading/trailing whitespace is trimmed before the first word is
//     taken; internal whitespace elsewhere is left alone.
//   - matching is case-insensitive: "Approve", "APPROVE" and "approve"
//     all match "approve".
//   - only the line's FIRST word is ever tested against the vocabulary,
//     and it has to match it exactly: "looks good, but verify the
//     padding" is prose (verbNone) even though it contains "verify",
//     because its first word is "looks". parseInput never looks past the
//     first word and never fuzzy-matches within it.
//   - a first word carrying trailing punctuation does not match:
//     "approve." is prose, not the verb "approve" — the vocabulary is
//     matched against the literal first token (split on whitespace only),
//     so a verb has to stand alone to be recognised.
//   - an empty or whitespace-only line is verbNone with an empty Text.
//   - a bare "/" is verbMenu with no Remainder. A line that merely
//     starts with "/" but carries more ("/foo", "/ foo bar") is ALSO
//     verbMenu, with whatever follows the "/" (trimmed) as Remainder —
//     the command menu opens pre-filtered by it rather than gummi trying
//     to guess where a verb grammar would end and a menu query begins.
//   - a recognised verb with more after it carries the rest, trimmed, as
//     Remainder: "changes the pill needs the dim token" parses to Verb
//     "changes", Remainder "the pill needs the dim token".
func parseInput(line string) parsedInput {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return parsedInput{Kind: verbNone}
	}
	if trimmed[0] == '/' {
		return parsedInput{Kind: verbMenu, Remainder: strings.TrimSpace(trimmed[1:]), Text: trimmed}
	}
	fields := strings.Fields(trimmed)
	verb := strings.ToLower(fields[0])
	if !verbs[verb] {
		return parsedInput{Kind: verbNone, Text: trimmed}
	}
	remainder := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	return parsedInput{Kind: verbCommand, Verb: verb, Remainder: remainder, Text: trimmed}
}
