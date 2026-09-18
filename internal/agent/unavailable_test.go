package agent

import (
	"errors"
	"testing"
)

func TestUnavailableTellsAnOutageFromASetupProblem(t *testing.T) {
	limit := &RunFailure{Backend: "claude", Diagnostic: "You've hit your session limit · resets 3:10pm (UTC)", FirstTurn: true}
	words, ok := Unavailable(limit)
	if !ok {
		t.Fatal("a session limit is the backend being unavailable, not the card being broken")
	}
	if words != "You've hit your session limit · resets 3:10pm (UTC)" {
		t.Errorf("words = %q — the backend's own sentence is what says when it comes back", words)
	}

	for _, setup := range []error{
		&RunFailure{Backend: "claude", Diagnostic: "Invalid API key · please run /login", FirstTurn: true},
		&RunFailure{Backend: "codex", Diagnostic: "codex: command not found in $PATH"},
		&RunFailure{Backend: "claude", Diagnostic: "exit status 1"},
		errors.New("something else entirely"),
		nil,
	} {
		if words, ok := Unavailable(setup); ok {
			t.Errorf("Unavailable(%v) = %q, true — waiting fixes none of these", setup, words)
		}
	}

	// an error that only carries the text, no RunFailure to read
	if _, ok := Unavailable(errors.New("claude run failed: 429 Too Many Requests")); !ok {
		t.Error("a rate limit reported without a RunFailure is still a rate limit")
	}
}
