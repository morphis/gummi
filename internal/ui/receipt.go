package ui

// What autopilot decided on its own, and who counts as "on its own".
//
// This file used to build and render a block summarising every such
// decision a card had ever made, appended to the thread after the live
// stage. That block is gone: its contents are drawn as bounded periods
// among the card's history now (stretch.go), where they happened, rather
// than rolled up at the end of the page where they stayed permanently
// newer than the conversation. What remains is the small vocabulary that
// rollup needed and the stretch still does — who is a person, and the
// shape of the two things autopilot decides.

import (
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// humanGateActors are the ways a person crosses a gate themselves: "user"
// is the TUI's g, "caller" is the headless driver's attended mode.
// Everything else is a crossing made without a person present.
//
// It is written this way round deliberately. The machine actors are
// open-ended — "auto" is the driver's unattended loop, "review" is the
// automatic review→fix→verify chain, "autopilot" is the switch, and any
// future loop names itself — so enumerating those is how a reader
// silently starts miscounting the day one is added. The human set is
// bounded by how a person can actually reach a gate, and that is the
// smaller, more stable thing to name.
var humanGateActors = map[string]bool{"user": true, "caller": true}

// receiptGate is one design gate autopilot crossed on its own.
type receiptGate struct {
	from domain.Stage
	at   time.Time
}

// receiptAnswer is one ask_user question autopilot answered on its own.
type receiptAnswer struct {
	answer string
	at     time.Time
}

// plural is "" for exactly one, "s" otherwise.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
