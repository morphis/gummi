package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/webapi"
)

// The dialogs a web intent can meet on the way through a TUI flow, and
// how each is answered from a request instead of a keyboard. Every answer
// goes through the dialog's own submit — the same body its enter, y or
// ctrl+s runs — so validation, the follow-up question and the command it
// starts are the dialog's, not a copy of them.

// webAnswer is what answering a dialog did.
type webAnswer struct {
	// cmd is what the dialog's submit started.
	cmd tea.Cmd
	// wait: the dialog cannot be answered yet (a draft is still out);
	// the intent asks again when its next message lands.
	wait bool
	// keep: the dialog stays open (it moved to a second question the
	// request already answered, and closes itself).
	keep bool
	// dismiss: the dialog is not the request's to answer (webInput.land,
	// webInput.autopilot); it is closed unanswered.
	dismiss bool
	// needs names the input the request did not carry; question is what
	// the dialog asked; draft, on a landing, the message it holds; token,
	// on a confirmation, the yes to that question (webConfirmToken).
	needs    webapi.ActionNeeds
	question string
	draft    *string
	token    string
	// refused is the dialog's own refusal of the value it was given.
	refused string
	// newCard is a line the dialog would start a new card with.
	newCard string
}

// webAnswerable is a dialog a web intent can answer.
type webAnswerable interface {
	webAnswer(m *Shell, in *webInput) webAnswer
}

// webConfirmToken is the yes to one question: the dialog that asks it,
// the card it is about and every word of it. The page gets it with the
// question (a 409 "confirm") and sends it back when the person has read
// that question and said yes — so a yes is never given to a question
// nobody was shown: one that reads differently now (a goal that has
// grown a card, a check list that changed, a hand-off that would now
// leave a dependent behind) has another token, and is asked again.
func webConfirmToken(dialog string, card domain.FeatureID, question string) string {
	sum := sha256.Sum256([]byte(dialog + "\x00" + string(card) + "\x00" + question))
	return "c" + hex.EncodeToString(sum[:12])
}

// takeConfirm reports whether the request said yes to the question token
// was issued for, and spends that yes: it answers the one dialog it was
// issued for, once. A second confirmation the same flow reaches — even one
// asking the very same words — is asked on its own.
func (in *webInput) takeConfirm(token string) bool {
	fields := strings.Fields(in.confirm)
	i := slices.Index(fields, token)
	if i < 0 {
		return false
	}
	in.confirm = strings.Join(slices.Delete(fields, i, i+1), " ")
	return true
}

// confirmAsk is the answer a confirmation gives a request that did not
// say yes to it: its question, whole, and the token that answers it.
func confirmAsk(dialog string, card domain.FeatureID, question string) webAnswer {
	return webAnswer{needs: webapi.ActionNeedsConfirm, question: question, token: webConfirmToken(dialog, card, question)}
}

// A confirm answers yes only when the request carries the yes to its own
// question: the page shows the question the server asked — this one, word
// for word, line breaks and all — and sends back the token it came with.
// A request that did not is told the question. A confirm about another
// card is not the request's at all: the yes the page sent was for its own
// card's question, and taking it as another's would spend or destroy on a
// card nobody looked at.
func (d *confirmDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if d.card != "" && d.card != in.card {
		return webAnswer{dismiss: true}
	}
	q := d.question
	detail := d.detail
	if d.webDetail != "" {
		detail = d.webDetail
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		q += "\n" + detail
	}
	if !in.takeConfirm(webConfirmToken(d.id, in.card, q)) {
		return confirmAsk(d.id, in.card, q)
	}
	return webAnswer{cmd: d.onConfirm()}
}

// The budget dialog takes the number; its resume follow-up (a parked
// autopilot card, raised) is answered by confirm.
func (d *envelopeDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if in.number == nil {
		return webAnswer{needs: webapi.ActionNeedsNumber, question: "set " + string(d.feature.ID) + "'s budget (credits; 0 = uncapped)"}
	}
	if *in.number < 0 {
		return webAnswer{refused: "a budget is 0 (uncapped) or more credits"}
	}
	if why := tooLong(d.input.CharLimit, "that budget", strconv.Itoa(*in.number)); why != "" {
		return webAnswer{refused: why}
	}
	d.input.SetValue(strconv.Itoa(*in.number))
	done, cmd := d.submit()
	switch {
	case done:
		return webAnswer{cmd: cmd}
	case d.askResume:
		d.askResume = false
		q := fmt.Sprintf("raised to %d — resume %s on autopilot?", d.resumeTo, d.feature.ID)
		if in.takeConfirm(webConfirmToken("envelope-resume", d.feature.ID, q)) {
			cmd = tea.Batch(cmd, d.resumeNotice())
			return webAnswer{cmd: cmd}
		}
		// the budget is raised; whether to resume is the dialog's second
		// question, and it is put to the person rather than taken as no
		ask := confirmAsk("envelope-resume", d.feature.ID, q)
		ask.cmd = cmd
		return ask
	case d.problem != "":
		return webAnswer{refused: d.problem}
	}
	return webAnswer{cmd: cmd}
}

// The autopilot switch takes the mode the request names, or the one the
// dialog's own confirm hands over (the other side of the switch).
//
// Handing a card to autopilot loosens control, so the menu's hand-over
// asks first, as the TUI's overlay does: the question is the overlay's
// own account of what autopilot will do with this card, and only a yes
// to those words hands it over. Stopping autopilot tightens control and
// goes at once. A card created on autopilot was already asked, on the
// new-card form's own button (webInput.handoverAsked).
func (d *autopilotDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if in.mode == "" && !in.autopilot {
		return webAnswer{dismiss: true}
	}
	mode := in.mode
	if mode == "" {
		mode = d.submitMode()
	}
	if !in.handoverAsked && mode == domain.GateAutopilot {
		q := "Hand " + string(d.feature.ID) + " to autopilot?\n" +
			strings.Join(autopilotBody(d.feature, d.plan, mode, d.base()), "\n")
		if !in.takeConfirm(webConfirmToken(d.ID(), in.card, q)) {
			return confirmAsk(d.ID(), in.card, q)
		}
	}
	return webAnswer{cmd: d.onSubmit(mode)}
}

// The landing message: the request's own words, and nothing else. A
// landing never goes on a draft nobody has read — the TUI shows the draft
// and waits for a second ctrl+s before an unreviewed one lands — so a
// request with no message is told what the draft says (waiting on it
// while it is still being written) and sends the landing again with the
// words the person approved.
func (d *commitMsgDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if !in.land {
		return webAnswer{dismiss: true}
	}
	if msg := strings.TrimSpace(in.message); msg != "" {
		if why := tooLong(d.input.CharLimit, "the landing message", msg); why != "" {
			return webAnswer{refused: why}
		}
		d.input.SetValue(msg)
		d.modified = true
		_, cmd := d.merge()
		return webAnswer{cmd: cmd}
	}
	if d.drafting {
		return webAnswer{wait: true}
	}
	// the question says what sending the message back will do: a landing
	// lands the branch on its base; a squash in place collapses it to one
	// commit and lands nothing
	draft := strings.TrimSpace(d.input.Value())
	noun := "landing message"
	if d.inPlace {
		noun = "commit message"
	}
	why := "read the " + noun + ", then " + d.action()
	if draft == "" {
		why = "no " + noun + " was drafted"
		if d.reason != "" {
			why += " (" + strings.TrimPrefix(d.reason, "no draft: ") + ")"
		}
		why += " — write one to " + d.action()
	}
	return webAnswer{needs: webapi.ActionNeedsMessage, question: why, draft: &draft}
}

// The repository picker takes the repo the request names, if it is one
// the picker offers.
func (d *repoPickerDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if in.repo == "" {
		return webAnswer{needs: webapi.ActionNeedsRepo, question: "choose a repository for " + string(d.feature.ID) + ": " + strings.Join(d.candidates, ", ")}
	}
	for i, c := range d.candidates {
		if c == in.repo {
			return webAnswer{cmd: d.submit(i)}
		}
	}
	return webAnswer{refused: "repository " + strconv.Quote(in.repo) + " is not configured"}
}

// The new-card form a flow opens seeded with a line (the re-entry's "not
// this card's work") goes back to the page, which has its own form.
func (d *cardForm) webAnswer(_ *Shell, _ *webInput) webAnswer {
	text := strings.TrimSpace(d.Text())
	if text == "" {
		text = " "
	}
	return webAnswer{newCard: text}
}

// Linking a pull request takes the URL or number the request carried. With
// none it submits what the dialog opened on — the one open pull request its
// probe found for the branch, or nothing, which resolves the same way.
func (d *prLinkDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if spec := strings.TrimSpace(in.message); spec != "" {
		if why := tooLong(d.input.CharLimit, "that pull request", spec); why != "" {
			return webAnswer{refused: why}
		}
		d.input.SetValue(spec)
	}
	_, cmd := d.submit()
	return webAnswer{cmd: cmd}
}

// Reversing one of a goal's decisions is picked from the list the goal's
// page shows, with the lead's reasons beside each; the request is told to
// go there.
func (d *reverseDialog) webAnswer(_ *Shell, _ *webInput) webAnswer {
	return webAnswer{needs: webapi.ActionNeedsDecision, question: "pick the decision to reverse on " + string(d.f.ID) + "'s page"}
}

// tooLong is the refusal for a value longer than the terminal's input
// takes, or "" when it fits. A web request is never cut down to fit: a
// landing message trimmed at its limit, or a budget losing a digit, is a
// different answer from the one the person gave.
func tooLong(limit int, what, value string) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return ""
	}
	return fmt.Sprintf("%s is too long — at most %d characters", what, limit)
}

// Running verify's checks is a confirm, and the question names every
// command in full, one to a line, as the terminal's dialog shows them:
// what runs in the worktree is read before it runs, never behind a bare
// yes — and the yes is bound to those commands, so a list that changed
// since the page showed it is asked again rather than run.
func (d *verifyDialog) webAnswer(_ *Shell, in *webInput) webAnswer {
	if d.feature != in.card && in.card != "" {
		return webAnswer{dismiss: true}
	}
	lines := make([]string, 0, len(d.checks)+1)
	lines = append(lines, "run "+string(d.feature)+"'s verify checks in its worktree?")
	for _, ch := range d.checks {
		lines = append(lines, sanitize(ch.Name)+": "+sanitize(ch.Cmd))
	}
	q := strings.Join(lines, "\n")
	if !in.takeConfirm(webConfirmToken(d.ID(), d.feature, q)) {
		return confirmAsk(d.ID(), d.feature, q)
	}
	return webAnswer{cmd: d.onRun()}
}
