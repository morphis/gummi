package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/childproc"
)

// opencode 2.x serves a different HTTP API from 1.x — every route under
// /api, a turn admitted rather than awaited, its own event names — while
// reading the same per-session config (it normalizes the 1.x keys in
// memory). The adapter speaks both: the version is read once from the
// binary, the session's server is spoken to in that version's dialect,
// and its events are mapped into the same grammar the 1.x bus feeds, so
// the events a caller sees do not change with the version.

// opencodeMajor is the major version of the opencode binary at bin: 2 for
// a 2.x install, 1 for a 1.x one or one whose version cannot be read —
// what gummi spoke before it knew there was a 2.x. A variable, so tests
// pick the dialect without a binary.
var opencodeMajor = func(bin string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	childproc.Group(cmd)
	if err := childproc.Start(cmd); err != nil {
		return 1
	}
	if err := cmd.Wait(); err != nil {
		return 1
	}
	return parseOpencodeMajor(out.String())
}

var ocVersionRe = regexp.MustCompile(`\bv?(\d+)\.\d+`)

// parseOpencodeMajor reads the major version out of `opencode --version`:
// "1.19.5" from a 1.x binary, "opencode v2.2.5" from a 2.x one.
func parseOpencodeMajor(s string) int {
	m := ocVersionRe.FindStringSubmatch(s)
	if m == nil {
		return 1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// withInstructions adds an instruction file to a session config: how the
// stage hints reach a 2.x server, whose prompt has no system field. An
// instruction file is part of every system prompt the server builds, so
// the hints hold after a compaction, as the 1.x message field's do.
func withInstructions(cfg []byte, path string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		return nil, err
	}
	m["instructions"] = []string{path}
	return json.Marshal(m)
}

// ocExec is one execution of a 2.x session's agent loop that the adapter
// waits on — a turn's, or a compaction's. A prompt is admitted, not
// awaited, so its end is read off the bus: the loop's started event, then
// its terminal one. A terminal event before this execution started is a
// previous one's (an interrupted turn's late notice) and does not end it.
type ocExec struct {
	done    chan struct{}
	started bool   // under the session's mutex
	ended   bool   // under the session's mutex
	err     string // the failure the loop reported, if any
	// admitted: the server took the prompt or compaction, so its loop
	// is running or has run (under the session's mutex)
	admitted bool
}

// admit records that the server took ex's prompt or compaction.
func (s *opencodeSession) admit(ex *ocExec) {
	s.mu.Lock()
	ex.admitted = true
	s.mu.Unlock()
}

// ocLostEnd is the failure of an execution whose end fell in the gap a
// dropped event stream left: what the turn did last was not relayed.
const ocLostEnd = "the opencode server's event stream dropped as the turn ended, so its last events were lost (the server is still running)"

// settleExecV2 runs once a 2.x stream dropped under an execution is open
// again: the execution's end may have fallen in the gap, and would then
// be waited on for good. The server lists the sessions whose loop is
// running; when this one's is not — asked twice, a moment apart, so a loop
// that had only just been admitted has started — the execution ends as
// failed (ocLostEnd). One whose end the new stream carries ends as it
// says; whichever comes first wins.
func (s *opencodeSession) settleExecV2() {
	s.mu.Lock()
	ex, id := s.exec, s.sessionID
	s.mu.Unlock()
	for range 2 {
		s.mu.Lock()
		open := ex != nil && s.exec == ex && ex.admitted && !ex.ended
		s.mu.Unlock()
		if !open || id == "" {
			return
		}
		ctx, cancel := context.WithTimeout(s.sctx, 10*time.Second)
		running, err := s.srv.activeV2(ctx, id)
		cancel()
		if err != nil || running {
			return
		}
		select {
		case <-ex.done:
			return
		case <-s.stop:
			return
		case <-time.After(ocSettleRecheck):
		}
	}
	s.mu.Lock()
	open := s.exec == ex
	ex.started = true // its started event may have been lost too
	s.mu.Unlock()
	if open {
		s.endExecution(ocLostEnd)
	}
}

// ocSettleRecheck is how long settleExecV2 waits before asking again
// whether a session's loop is running. A variable, so tests run quick.
var ocSettleRecheck = time.Second

// beginExec opens the execution the next prompt or compaction starts.
func (s *opencodeSession) beginExec() *ocExec {
	ex := &ocExec{done: make(chan struct{})}
	s.mu.Lock()
	s.exec = ex
	s.mu.Unlock()
	return ex
}

// endExec drops ex once its caller has stopped waiting on it.
func (s *opencodeSession) endExec(ex *ocExec) {
	s.mu.Lock()
	if s.exec == ex {
		s.exec = nil
	}
	s.mu.Unlock()
}

// awaitExec waits for ex to end, or ctx to.
func awaitExec(ctx context.Context, ex *ocExec) error {
	select {
	case <-ex.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// postTurnV2 is postTurn on a 2.x server: the prompt admitted, then the
// turn's execution waited out on the bus. A resumed session is handed the
// model first — 2.x keeps the model on the session, not the message —
// which is also where a session the server no longer holds shows.
func (s *opencodeSession) postTurnV2(t *ocTurn, msg string, imgs []Image) error {
	id, err := s.sessionForTurn(t.ctx)
	if err != nil {
		return err
	}
	srv := s.srv
	s.mu.Lock()
	bind := !s.modelBound
	s.mu.Unlock()
	if bind {
		provider, model, _ := strings.Cut(s.model, "/")
		if err := srv.setModelV2(t.ctx, id, provider, model); err != nil {
			return err
		}
		s.mu.Lock()
		s.modelBound = true
		s.mu.Unlock()
	}
	ex := s.beginExec()
	defer s.endExec(ex)
	if err := srv.promptV2(t.ctx, id, promptBodyV2(msg, imgs)); err != nil {
		return err
	}
	s.admit(ex)
	return awaitExec(t.ctx, ex)
}

// promptBodyV2 is a 2.x prompt: the text, and each image as an attached
// file, inline as a data URI.
func promptBodyV2(msg string, imgs []Image) map[string]any {
	body := map[string]any{"text": msg}
	var files []map[string]any
	for _, img := range imgs {
		raw, err := os.ReadFile(img.Path)
		if err != nil {
			continue
		}
		mime := img.MediaType
		if mime == "" {
			mime = "application/octet-stream"
		}
		name := img.Path
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		files = append(files, map[string]any{
			"uri":  "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw),
			"name": name,
		})
	}
	if len(files) > 0 {
		body["files"] = files
	}
	return body
}

// runCompactV2 compacts on a 2.x server: the compaction admitted like a
// prompt, then its execution waited out on the bus.
func (s *opencodeSession) runCompactV2(ctx context.Context, srv opencodeServer, id string) error {
	ex := s.beginExec()
	defer s.endExec(ex)
	if err := srv.compactV2(ctx, id); err != nil {
		return err
	}
	s.admit(ex)
	if err := awaitExec(ctx, ex); err != nil {
		return err
	}
	s.mu.Lock()
	detail := ex.err
	s.mu.Unlock()
	if detail != "" {
		return errors.New(detail)
	}
	return nil
}

// ocV2Event is one event on a 2.x server's bus: a type and the data that
// type carries. Only the slice the adapter maps is decoded.
type ocV2Event struct {
	Type string `json:"type"`
	Data struct {
		SessionID string `json:"sessionID"`
		ParentID  string `json:"parentID"` // session.created
		// text and reasoning: the message and the part's ordinal in it
		AssistantMessageID string `json:"assistantMessageID"`
		Ordinal            int    `json:"ordinal"`
		Delta              string `json:"delta"`
		Text               string `json:"text"`
		// tools: the call's id, its name (input.started) and arguments
		// (called), and its outcome (success, failed)
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Input   map[string]any `json:"input"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// step.ended, compaction.ended: what the step cost
		Finish string  `json:"finish"`
		Cost   float64 `json:"cost"`
		Tokens *struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Reason string `json:"reason"` // execution.interrupted: "user" | "shutdown" | …
		// permission.asked
		Action    string   `json:"action"`
		Resources []string `json:"resources"`
		// form.created
		Form struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		} `json:"form"`
	} `json:"data"`
}

// partKey names one streamed text or reasoning part of a 2.x message.
// An ordinal counts a message's parts of one kind, so a step's thought and
// its prose are both ordinal 0: keyed without the kind, they shared one
// streamed length, and the thought's end dropped the prose's — whose own
// end, arriving after the tool call that closed it, then wrote it all
// into the next bubble again.
func (ev *ocV2Event) partKey(kind string) string {
	return kind + ":" + ev.Data.AssistantMessageID + ":" + strconv.Itoa(ev.Data.Ordinal)
}

// stepPart is a 2.x step's end as the part the 1.x grammar reads.
func (ev *ocV2Event) stepPart() ocPart {
	p := ocPart{SessionID: ev.Data.SessionID, Type: "step-finish", Cost: ev.Data.Cost, Reason: ev.Data.Finish}
	if tok := ev.Data.Tokens; tok != nil {
		p.Tokens.Input, p.Tokens.Output, p.Tokens.Reasoning = tok.Input, tok.Output, tok.Reasoning
		p.Tokens.Cache.Read, p.Tokens.Cache.Write = tok.Cache.Read, tok.Cache.Write
	}
	return p
}

// dispatchV2 maps one 2.x bus payload into gummi Events, through the same
// relays the 1.x bus feeds.
func (s *opencodeSession) dispatchV2(data []byte) {
	var ev ocV2Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return
	}
	d := &ev.Data
	ours := d.SessionID != "" && d.SessionID == s.sessionIDValue()
	switch ev.Type {
	case "session.created":
		s.adoptChild(d.SessionID, d.ParentID)
	case "session.execution.started":
		if !ours {
			return
		}
		s.mu.Lock()
		if s.exec != nil {
			s.exec.started = true
		}
		s.mu.Unlock()
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		if !ours {
			return
		}
		var detail string
		switch {
		case ev.Type == "session.execution.failed" && d.Error != nil:
			detail = d.Error.Message
			if detail == "" {
				detail = d.Error.Type
			}
			if detail == "" {
				detail = "opencode reported an error"
			}
		case ev.Type == "session.execution.interrupted" && d.Reason != "user":
			detail = "opencode stopped the turn (" + d.Reason + ")"
		}
		s.endExecution(detail)
	case "session.step.failed":
		if ours && d.Error != nil && d.Error.Type != "aborted" {
			s.foldTurnErr(d.Error.Message)
		}
		if d.Tokens != nil {
			s.relayStep(ev.stepPart())
		}
	case "session.step.ended":
		s.relayStep(ev.stepPart())
	case "session.compaction.ended", "session.compaction.failed":
		if ours || s.isChild(d.SessionID) {
			s.relaySpend(ev.stepPart())
		}
	case "session.text.delta", "session.reasoning.delta":
		if !ours {
			return
		}
		kind := strings.TrimSuffix(strings.TrimPrefix(ev.Type, "session."), ".delta")
		key := ev.partKey(kind)
		s.mu.Lock()
		t := s.turn
		open := t != nil && !t.finishing
		if open {
			s.partLen[key] += len(d.Delta)
		}
		s.mu.Unlock()
		if open {
			s.relayEvents(t, textEvents(kind, d.Delta, &t.msg))
		}
	case "session.text.ended", "session.reasoning.ended":
		if !ours {
			return
		}
		// the part's whole text: whatever no delta carried (deltas are
		// not durable on the bus, the ended event is)
		kind := strings.TrimSuffix(strings.TrimPrefix(ev.Type, "session."), ".ended")
		key := ev.partKey(kind)
		s.mu.Lock()
		t := s.turn
		open := t != nil && !t.finishing
		var rest string
		if n := s.partLen[key]; open && n < len(d.Text) {
			rest = d.Text[n:]
		}
		delete(s.partLen, key)
		s.mu.Unlock()
		if open {
			s.relayEvents(t, textEvents(kind, rest, &t.msg))
		}
	case "session.tool.input.started":
		if !ours || d.ID == "" {
			return
		}
		s.mu.Lock()
		if s.toolNames == nil {
			s.toolNames = map[string]string{}
		}
		s.toolNames[d.ID] = d.Name
		s.mu.Unlock()
	case "session.tool.called":
		if !ours {
			return
		}
		s.relayTool("tool_running", ocPart{
			SessionID: d.SessionID, Tool: s.toolName(d.ID, false), CallID: d.ID,
			State: ocToolState{Input: d.Input, Status: "running"},
		})
	case "session.tool.success", "session.tool.failed":
		if !ours {
			return
		}
		p := ocPart{SessionID: d.SessionID, Tool: s.toolName(d.ID, true), CallID: d.ID}
		var out []string
		for _, c := range d.Content {
			if c.Type == "text" && c.Text != "" {
				out = append(out, c.Text)
			}
		}
		if ev.Type == "session.tool.success" {
			p.State.Status, p.State.Output = "completed", strings.Join(out, "\n")
		} else {
			p.State.Status = "error"
			if d.Error != nil {
				p.State.Error = d.Error.Message
			}
			if p.State.Error == "" {
				p.State.Error = strings.Join(out, "\n")
			}
		}
		s.relayTool("tool_use", p)
	case "permission.asked":
		// as on 1.x, a task child's held call holds this session's turn
		// too, so it surfaces whichever session asked; the reply is
		// addressed under the session that asked, so that is kept
		s.mu.Lock()
		if s.permSession == nil {
			s.permSession = map[string]string{}
		}
		s.permSession[d.ID] = d.SessionID
		if t := s.turn; t != nil && !t.finishing {
			t.msg.mark(true)
		}
		s.mu.Unlock()
		s.emit(Event{
			Kind:   EventPermission,
			Tool:   d.Action,
			Detail: strings.Join(d.Resources, ", "),
			CallID: d.ID,
		})
	case "form.created":
		// 2.x asks its questions as forms; one asked anyway would hold the
		// turn on an answer gummi never gives, so it is cancelled, as a
		// 1.x question is rejected
		f := d.Form
		if f.ID == "" || f.SessionID == "" {
			return
		}
		go func() {
			rctx, cancel := context.WithTimeout(s.sctx, 5*time.Second)
			defer cancel()
			_ = s.srv.cancelFormV2(rctx, f.SessionID, f.ID)
		}()
	}
}

// endExecution ends the execution the adapter waits on with the failure
// the loop reported ("" for a clean end). A failure during a turn is that
// turn's one failure, as a 1.x session.error is.
func (s *opencodeSession) endExecution(detail string) {
	if s.interruptedValue() {
		detail = "" // the interrupt's own end: the turn ends idle
	}
	s.mu.Lock()
	ex := s.exec
	if ex == nil || !ex.started || ex.ended {
		s.mu.Unlock()
		return
	}
	ex.ended = true
	ex.err = detail
	s.mu.Unlock()
	if detail != "" {
		s.foldTurnErr(detail)
	}
	close(ex.done)
}

// foldTurnErr records detail as the open turn's failure, the first one
// reported winning.
func (s *opencodeSession) foldTurnErr(detail string) {
	if detail == "" || s.interruptedValue() {
		return
	}
	s.mu.Lock()
	if t := s.turn; t != nil && !t.finishing && t.errDetail == "" {
		t.errDetail = detail
	}
	s.mu.Unlock()
}

// relayStep relays a 2.x step's end as the 1.x step-finish part.
func (s *opencodeSession) relayStep(p ocPart) {
	if p.SessionID == "" {
		return
	}
	if p.SessionID != s.sessionIDValue() {
		if s.isChild(p.SessionID) {
			s.relaySpend(p)
		}
		return
	}
	s.mu.Lock()
	t := s.turn
	finishing := t == nil || t.finishing
	s.mu.Unlock()
	if finishing {
		s.relaySpend(p)
		return
	}
	s.relayEvents(t, s.mapOcEvent(&ocEvent{Type: "step_finish", SessionID: p.SessionID, Part: p}, &t.msg))
}

// relayTool relays a 2.x tool call's start or end as the 1.x tool part.
func (s *opencodeSession) relayTool(kind string, p ocPart) {
	s.mu.Lock()
	t := s.turn
	finishing := t == nil || t.finishing
	s.mu.Unlock()
	if finishing {
		return
	}
	s.relayEvents(t, s.mapOcEvent(&ocEvent{Type: kind, SessionID: p.SessionID, Part: p}, &t.msg))
}

// toolName is the name a 2.x call announced when its input began; done
// forgets it, at the call's end.
func (s *opencodeSession) toolName(id string, done bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.toolNames[id]
	if done {
		delete(s.toolNames, id)
	}
	if name == "" {
		name = "tool"
	}
	return name
}

// permissionSession is the session a held 2.x call was asked under.
func (s *opencodeSession) permissionSession(requestID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.permSession[requestID]; id != "" {
		delete(s.permSession, requestID)
		return id
	}
	return s.sessionID
}

// readV2 reads a 2.x response's body, failing on a status other than ok —
// a 404 on a session route as the session the server no longer holds.
func readV2(resp *http.Response, what, sessionID string, ok ...int) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if slices.Contains(ok, resp.StatusCode) {
		return body, nil
	}
	if resp.StatusCode == http.StatusNotFound && sessionID != "" {
		return nil, fmt.Errorf("%w: %s", errOcLostSession, sessionID)
	}
	return nil, fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(body)))
}

func v2Session(id string) string { return "/api/session/" + url.PathEscape(id) }

// createV2 opens a 2.x session on the model and returns its id.
func (o opencodeServer) createV2(ctx context.Context, provider, model string) (string, error) {
	resp, err := o.do(ctx, http.MethodPost, "/api/session", map[string]any{
		"model": map[string]string{"providerID": provider, "id": model},
	})
	if err != nil {
		return "", err
	}
	body, err := readV2(resp, "creating the session", "", http.StatusOK)
	if err != nil {
		return "", err
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Data.ID == "" {
		return "", errors.New("creating the session: no session id in the response")
	}
	return out.Data.ID, nil
}

// setModelV2 puts a 2.x session on the model.
func (o opencodeServer) setModelV2(ctx context.Context, id, provider, model string) error {
	resp, err := o.do(ctx, http.MethodPost, v2Session(id)+"/model", map[string]any{
		"model": map[string]string{"providerID": provider, "id": model},
	})
	if err != nil {
		return err
	}
	_, err = readV2(resp, "choosing the model", id, http.StatusOK, http.StatusNoContent)
	return err
}

// promptV2 admits one prompt to a 2.x session; the turn runs after it
// returns.
func (o opencodeServer) promptV2(ctx context.Context, id string, body any) error {
	resp, err := o.do(ctx, http.MethodPost, v2Session(id)+"/prompt", body)
	if err != nil {
		return err
	}
	_, err = readV2(resp, "sending the message", id, http.StatusOK)
	return err
}

// activeV2 reports whether session id's loop is running: the server
// lists every session whose loop is, and none other.
func (o opencodeServer) activeV2(ctx context.Context, id string) (bool, error) {
	resp, err := o.do(ctx, http.MethodGet, "/api/session/active", nil)
	if err != nil {
		return false, err
	}
	body, err := readV2(resp, "listing the running sessions", "", http.StatusOK)
	if err != nil {
		return false, err
	}
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("listing the running sessions: %w", err)
	}
	_, running := out.Data[id]
	return running, nil
}

// compactV2 admits a compaction to a 2.x session.
func (o opencodeServer) compactV2(ctx context.Context, id string) error {
	resp, err := o.do(ctx, http.MethodPost, v2Session(id)+"/compact", map[string]any{})
	if err != nil {
		return err
	}
	_, err = readV2(resp, "compact", id, http.StatusOK)
	return err
}

// cancelFormV2 cancels a form a 2.x session raised.
func (o opencodeServer) cancelFormV2(ctx context.Context, sessionID, formID string) error {
	resp, err := o.do(ctx, http.MethodDelete, v2Session(sessionID)+"/form/"+url.PathEscape(formID), nil)
	if err != nil {
		return err
	}
	_, err = readV2(resp, "cancelling the form", "", http.StatusOK, http.StatusNoContent)
	return err
}

// catalogV2 is the 2.x model list: every model a connected provider
// offers, with its context window.
func (o opencodeServer) catalogV2(ctx context.Context) ([]ocV2Model, error) {
	resp, err := o.do(ctx, http.MethodGet, "/api/model", nil)
	if err != nil {
		return nil, err
	}
	body, err := readV2(resp, "opencode models", "", http.StatusOK)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []ocV2Model `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}
	return out.Data, nil
}

type ocV2Model struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Enabled    *bool  `json:"enabled"`
	Limit      struct {
		Context int64 `json:"context"`
	} `json:"limit"`
}
