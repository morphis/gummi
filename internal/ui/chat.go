package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
)

// A conversation in progress is not re-read.
//
// The reader is a one-word scribe pass with the artifact and the diff in
// reach; it does not have the thread. A line typed after a consult
// reply — "no, the other one", "what about the quota path" — continues
// that exchange, and a reader that cannot see what it continues would
// call it a question at best and a wrong plan at worst, and charge a
// turn to do it. So once the thread's newest exchange is a consult
// answer, prose stays in the conversation, with no read, until the
// reader plainly asks for a move.
//
// The way out is not a second classifier. It is the vocabulary's own
// words, typed as words at the start of the line — the same words the
// screen already teaches — matched literally. A reader who has been
// talking and now wants a move says so in those words, and the read
// runs on the rest of the line.

// chatExit reads a vocabulary word off the start of a line. ok is false
// for a line that is conversation. intent is "" for "send it back" and
// "bounce", whose rest still has to be read; a word that IS its intent
// (approve, go on, land, new card) needs no read at all.
func chatExit(line string) (intent reentry.Intent, rest string, ok bool) {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, w := range []struct {
		word   string
		intent reentry.Intent
	}{
		{"send it back", ""},
		{"bounce", ""},
		{"approve", reentry.Proceed},
		{"go on", reentry.Proceed},
		{"land", reentry.Proceed},
		{"new card", reentry.SeparateCard},
		{"separate card", reentry.SeparateCard},
	} {
		if !strings.HasPrefix(lower, w.word) {
			continue
		}
		tail := strings.TrimSpace(line[len(w.word):])
		// the word alone, or the word followed by a separator and the
		// rest — never a longer word that happens to start with it
		// ("landing", "approved")
		if tail != "" && !strings.ContainsAny(tail[:1], ":,;—- ") && (len(lower) > len(w.word) && lower[len(w.word)] != ' ') {
			continue
		}
		return w.intent, strings.TrimLeft(tail, ":,;—- "), true
	}
	return "", "", false
}

// startChat marks the card's thread as a conversation in progress.
func (m *Shell) startChat(id domain.FeatureID) {
	if m.chatting == nil {
		m.chatting = map[domain.FeatureID]bool{}
	}
	m.chatting[id] = true
}

// endChat is every way out of a conversation that is not a word: a row
// picked, a verb typed, the card moving, the page closed.
func (m *Shell) endChat(id domain.FeatureID) {
	if m.chatting != nil {
		delete(m.chatting, id)
	}
}

// inChat reports whether the card's next line continues a conversation.
func (m *Shell) inChat(id domain.FeatureID) bool { return m.chatting[id] }
