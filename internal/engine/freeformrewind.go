package engine

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNothingToRewind is Rewind's answer when there is no such message.
var ErrNothingToRewind = errors.New("no earlier message of yours to rewind to")

// Rewind takes a freeform conversation back to just before the n-th most
// recent thing the person said (1 is the last), and returns what they said
// there so a face can put it back in the composer to edit and resend.
//
// It rewinds the CONVERSATION, never the branch. Every commit on a
// freeform card's branch is one the agent made on purpose (DESIGN §19),
// and the work since that message stays where it is, in commits and in
// the worktree. A note says so, both to the reader and, through the
// replay, to the agent, so neither takes the code to match the shorter
// conversation. Taking the branch back as well is the person's call, made
// with git, not something a rewind does by the way.
//
// The backend goes: it remembers the dropped turns, and the backend's own
// conversation id is forgotten so the next turn's respawn replays what is
// left instead of resuming what was cut (SwitchSessionModel does the same).
// Not mid-turn, and not over an open question: stop or answer it first.
func (ff *FreeformSession) Rewind(n int) (string, error) {
	ff.mu.Lock()
	sess := ff.sess
	ff.mu.Unlock()
	if sess == nil || n < 1 {
		return "", ErrNothingToRewind
	}
	if sess.Busy() {
		return "", errors.New("the agent is mid-turn — stop it first, then rewind")
	}
	if sess.Snapshot().PendingAsk != nil {
		return "", errors.New("a question is open — answer it first, then rewind")
	}
	text, ok := sess.truncateBeforeUser(n)
	if !ok {
		return "", ErrNothingToRewind
	}
	sess.setAgentSessionID("")
	sess.appendSystem(fmt.Sprintf("Rewound the conversation to before “%s”. The branch was not rewound: "+
		"anything committed or written since is still there — check git log and git status before "+
		"assuming the code matches this conversation.", rewindExcerpt(text)))
	if sess.Live() {
		ff.settle()
		sess.setState(StateDone)
		sess.stop()
		ff.dropLock()
	} else {
		ff.engine.persist(sess)
	}
	ff.engine.send(Event{Feature: ff.id, Kind: EventUpdated})
	return text, nil
}

func rewindExcerpt(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return t
}

// truncateBeforeUser drops the n-th most recent user message and all that
// follows it, returning its text. The pinned checklist is kept wherever it
// sits: it is the agent's latest statement of its plan, not a turn.
func (s *Session) truncateBeforeUser(n int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := -1
	for i := len(s.transcript) - 1; i >= 0; i-- {
		if s.transcript[i].Author == AuthorUser {
			if n--; n == 0 {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return "", false
	}
	text := s.transcript[at].Content
	var tasks *Message
	for i := at; i < len(s.transcript); i++ {
		if s.transcript[i].Author == AuthorTasks {
			m := s.transcript[i]
			tasks = &m
		}
	}
	s.transcript = s.transcript[:at:at]
	if tasks != nil {
		s.transcript = append(s.transcript, *tasks)
	}
	s.streamOpen = false
	return text, true
}
