package engine

import "strings"

// assistantText accumulates a transient session's reply text from its
// event stream. Streaming adapters emit incremental deltas AND the
// completed message they were streaming (the message is authoritative),
// so each completed message replaces the deltas accumulated since the
// last one — collecting both would double the text. Board sessions have
// the same rule in Session.finishAssistant; this is the collector for
// passes that only need the final prose.
type assistantText struct {
	done strings.Builder // completed messages, oldest first
	tail strings.Builder // deltas since the last completed message
}

func (t *assistantText) delta(text string) { t.tail.WriteString(text) }

func (t *assistantText) message(text string) {
	t.tail.Reset()
	t.done.WriteString("\n")
	t.done.WriteString(text)
}

func (t *assistantText) String() string { return t.done.String() + t.tail.String() }
