package ui

import "strings"

// verbKind classifies one line of the thread's input, per parseInput's
// doc comment.
type verbKind int

const (
	verbNone    verbKind = iota // ordinary prose — a message to the agent
	verbCommand                 // "/" + a word in the closed vocabulary
	verbMenu                    // "/" or "/<filter>" — open the command menu
)

// verbs is the closed, case-insensitive vocabulary parseInput matches
// against the first word AFTER a leading "/". This is the whole set —
// nothing outside it is ever recognised, and nothing inside it is ever
// fuzzy-matched.
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
	"handoff":   true,
	"rebase":    true,
	"squash":    true,
	"clean":     true,
	"ask":       true,
}

// parsedInput is parseInput's result.
type parsedInput struct {
	Kind verbKind
	// Verb is the canonical lower-case verb; empty unless Kind ==
	// verbCommand.
	Verb string
	// Remainder is everything after the verb (verbCommand), or after the
	// leading "/" (verbMenu), trimmed; empty when there is nothing left
	// over.
	Remainder string
	// Text is the original line, trimmed, for the message path
	// (verbNone) — empty only when the line was itself empty/whitespace.
	Text string
}

// parseInput classifies one line typed into the thread's input. It is
// pure — no side effects, no knowledge of the engine, the board, or a
// card — which is what makes the whole vocabulary exhaustively
// table-tested (verbs_test.go) without a Shell, an engine, or a fake
// agent anywhere in sight.
//
// The rule is a sigil, and it is the whole rule: **a bare word is always
// prose; "/verb" is always a verb.** Nothing about a line's content
// decides its kind — only its first character does. This is what retired
// the confirm chip. The chip existed because ten state-changing verbs are
// also ordinary English first words ("verify the CSV path is right" would
// have run the checks), so every one of them had to stop and ask. With
// the sigil there is nothing to disambiguate: prose can never fire an
// action, so an action never has to confirm it was meant.
//
// Rules:
//   - leading/trailing whitespace is trimmed before anything is read.
//   - a line that does not begin with "/" is verbNone — prose — whatever
//     words it contains and wherever they sit. "approve" is a message.
//   - a bare "/" is verbMenu with no Remainder.
//   - "/" followed by an exact, whole verb is verbCommand: "/verify" and
//     "/verify the csv path" both carry Verb "verify", the latter with
//     Remainder "the csv path". Matching is case-insensitive but exact on
//     the token, split on whitespace only — "/approve." and "/appro" are
//     not the verb.
//   - "/" followed by anything else is verbMenu, with whatever followed
//     the "/" (trimmed) as Remainder, so the command menu opens
//     pre-filtered by it.
func parseInput(line string) parsedInput {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return parsedInput{Kind: verbNone}
	}
	if trimmed[0] != '/' {
		return parsedInput{Kind: verbNone, Text: trimmed}
	}
	rest := strings.TrimSpace(trimmed[1:])
	if fields := strings.Fields(rest); len(fields) > 0 {
		if verb := strings.ToLower(fields[0]); verbs[verb] {
			return parsedInput{
				Kind:      verbCommand,
				Verb:      verb,
				Remainder: strings.TrimSpace(strings.TrimPrefix(rest, fields[0])),
				Text:      trimmed,
			}
		}
	}
	return parsedInput{Kind: verbMenu, Remainder: rest, Text: trimmed}
}
