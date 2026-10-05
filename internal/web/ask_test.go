package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/web/push"
	"github.com/morphis/gummi/internal/webapi"
)

// askingAgent asks one ask_user question on its first turn and
// acknowledges whatever comes after.
func askingAgent() *agent.Fake {
	return askingAgentWith(`{"changes_section":"Problem","question":"Persist where?","options":[{"label":"per-device","detail":"localStorage"},{"label":"synced","detail":"account"}]}`)
}

// askingAgentWith asks the ask_user call args on its first turn.
func askingAgentWith(call string) *agent.Fake {
	f := agent.NewFake("")
	f.Caps = agent.Capabilities{ClientTools: true, Interrupt: true, UsageEvents: true}
	args := []byte(call)
	var mu sync.Mutex
	asked := false
	f.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		defer mu.Unlock()
		if !asked {
			asked = true
			return []agent.Event{{Kind: agent.EventClientToolCall, ToolCall: &agent.ToolCall{ID: "call-1", Name: "ask_user", Args: args}}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ack: " + msg}, {Kind: agent.EventIdle}}
	}
	return f
}

// startArchitect answers the design stage's "start the architect" and
// waits for the question the agent asks.
func (h *cardBoard) startArchitect(id string) webapi.Card {
	h.t.Helper()
	c := h.card(id)
	option(h.t, c.Decision, "run")
	st, raw := h.answer(id, webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "run", Against: c.Decision.Against.Token})
	if st != http.StatusOK {
		h.t.Fatalf("start the architect: %d %s", st, raw)
	}
	return h.waitCard(id, "the question", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionAsk
	})
}

// The agent's question is the card's decision, with the picker's options
// and the chat row; picking one answers it as the person who picked, and
// the card is no longer waiting.
func TestAnswerAnAsk(t *testing.T) {
	h := newCardBoard(t, askingAgent())
	c := h.startArchitect(h.planCard("Dark mode").ID)
	d := c.Decision
	if d.Question != "Persist where?" || len(d.Options) != 3 || d.Options[0].Label != "per-device" ||
		!option(t, d, "chat").Chat || !option(t, d, "chat").Words {
		t.Fatalf("ask decision = %+v", d)
	}
	if c.Needs == nil || c.Needs.Kind != webapi.NeedsQuestion {
		t.Errorf("the card is not waiting on a question: %+v", c.Row)
	}
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: d.Ref, Option: "0", Against: d.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("answer: %d %s", st, raw)
	}
	h.waitCard(c.ID, "the question to close", func(c webapi.Card) bool {
		return c.Decision == nil || c.Decision.Kind != webapi.DecisionAsk
	})
	p := lastAsk(t, h, c.ID)
	if p.Answer != "per-device" || p.By != state.PersonActor("Simon") || threadfold.AskAnswerer(p) != "Simon" {
		t.Errorf("answer recorded as %+v", p)
	}
}

// "Chat about this" answers with the person's own words.
func TestAnswerAnAskWithWords(t *testing.T) {
	h := newCardBoard(t, askingAgent())
	c := h.startArchitect(h.planCard("Dark mode").ID)
	d := c.Decision
	// the chat row with no words has nothing to say
	if st, _ := h.answer(c.ID, webapi.AnswerRequest{Ref: d.Ref, Option: "chat", Against: d.Against.Token}); st != http.StatusBadRequest {
		t.Errorf("chat without words = %d, want 400", st)
	}
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: d.Ref, Option: "chat", Words: "in the URL, so links share it", Against: d.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("answer: %d %s", st, raw)
	}
	h.waitCard(c.ID, "the question to close", func(c webapi.Card) bool {
		return c.Decision == nil || c.Decision.Kind != webapi.DecisionAsk
	})
	if p := lastAsk(t, h, c.ID); p.Answer != "in the URL, so links share it" || !state.IsPersonActor(p.By) {
		t.Errorf("answer recorded as %+v", p)
	}
}

// A question that takes several answers says so, and is answered with
// the picked options' ids, comma-separated.
func TestAnswerAMultiPickAsk(t *testing.T) {
	h := newCardBoard(t, askingAgentWith(`{"changes_section":"Problem","question":"Which surfaces?","multi_select":true,`+
		`"options":[{"label":"board"},{"label":"card"},{"label":"stats"}]}`))
	c := h.startArchitect(h.planCard("Dark mode").ID)
	d := c.Decision
	if !d.Multi || len(d.Options) != 4 {
		t.Fatalf("multi-pick decision = %+v", d)
	}
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: d.Ref, Option: "0,2", Against: d.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("answer: %d %s", st, raw)
	}
	if p := lastAsk(t, h, c.ID); p.Answer != "board, stats" {
		t.Errorf("answer recorded as %+v", p)
	}
}

func lastAsk(t *testing.T, h *cardBoard, id string) state.AskPayload {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		evs, err := h.store.Events(context.Background(), domain.FeatureID(id))
		if err != nil {
			t.Fatal(err)
		}
		for i := len(evs) - 1; i >= 0; i-- {
			var p state.AskPayload
			if evs[i].Kind == state.EventAsk && json.Unmarshal([]byte(evs[i].Payload), &p) == nil && p.Answer != "" {
				return p
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no answered ask in the log")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A device that subscribed hears the card that starts needing someone:
// the push service gets an encrypted message the browser opens to the
// card's title, its question and a link to its page.
func TestANeedsYouTransitionIsPushed(t *testing.T) {
	type delivery struct {
		hdr  http.Header
		body []byte
	}
	got := make(chan delivery, 4)
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- delivery{r.Header.Clone(), b}
		w.WriteHeader(http.StatusCreated)
	}))
	defer svc.Close()

	var pusher *Push
	h := newCardBoard(t, askingAgent(), func(s *ui.Shell) func(*Options) {
		p, err := OpenPush(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p.Sender.Client = svc.Client()
		p.Notifier.Debounce = -1
		p.Sender.AllowPrivate = true // the stand-in push service is on loopback
		s.AddAttentionNotifier(p)
		pusher = p
		return func(o *Options) { o.Push = p }
	})

	var key webapi.PushKey
	if st := h.call(http.MethodGet, "/api/push/key", nil, &key); st != http.StatusOK || key.Key != pusher.Key() {
		t.Fatalf("push key = %d %+v", st, key)
	}
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	b64 := base64.RawURLEncoding
	sub := webapi.PushSubscription{Endpoint: svc.URL + "/push/abc", Keys: webapi.PushKeys{P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(auth)}}
	// a subscription that is not https is refused before anything is sent to it
	bad := sub
	bad.Endpoint = "http://push.example/x"
	if st := h.call(http.MethodPost, "/api/push/subscribe", bad, nil); st != http.StatusBadRequest {
		t.Errorf("an http endpoint = %d, want 400", st)
	}
	if st := h.call(http.MethodPost, "/api/push/subscribe", sub, nil); st != http.StatusOK {
		t.Fatalf("subscribe = %d", st)
	}
	if subs := pusher.Store.List(); len(subs) != 1 || subs[0].Person != "Simon" {
		t.Fatalf("stored subscriptions = %+v", subs)
	}

	c := h.startArchitect(h.planCard("Dark mode").ID)
	var d delivery
	select {
	case d = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the needs-you transition reached no push service")
	}
	if d.hdr.Get("Content-Encoding") != "aes128gcm" || d.hdr.Get("Topic") != push.TopicFor(c.ID) {
		t.Errorf("push headers = %v", d.hdr)
	}
	plain, err := openPush(ua, auth, d.body)
	if err != nil {
		t.Fatal(err)
	}
	var msg push.Message
	if err := json.Unmarshal(plain, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Title != c.ID+" needs you" || !strings.Contains(msg.Body, "Persist where?") || msg.URL != "/#"+c.ID || msg.Tag != c.ID {
		t.Errorf("pushed %+v", msg)
	}

	// unsubscribing drops this device
	if st := h.call(http.MethodDelete, "/api/push/subscribe", nil, nil); st != http.StatusOK || len(pusher.Store.List()) != 0 {
		t.Errorf("unsubscribe = %d, left %+v", st, pusher.Store.List())
	}
}

// openPush is the browser's half of RFC 8291, enough to read what was sent.
func openPush(ua *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short body")
	}
	salt, idlen := body[:16], int(body[20])
	_ = binary.BigEndian.Uint32(body[16:20])
	asPublic, record := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := ua.ECDH(as)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPublic), 32)
	if err != nil {
		return nil, err
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		return nil, err
	}
	i := bytes.LastIndexByte(pt, 0x02)
	if i < 0 {
		return nil, errors.New("no delimiter")
	}
	return pt[:i], nil
}

// A freeform card's agent that asks is blocked inside its ask_user call, so
// a line sent as a turn is refused. The composer routes prose at the open
// question to its answer, and "Chat about this" with the person's words
// closes it as the answer.
func TestAFreeformQuestionIsAnsweredInYourWords(t *testing.T) {
	h := newCardBoard(t, askingAgent())
	c := h.create(webapi.CreateCardRequest{Kind: "freeform", Title: "Poke at the persistence", Description: "ask me where"})
	c = h.waitCard(c.ID, "the question", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionAsk
	})
	if got := h.composer(c.ID, "Go up the middle"); got.Route != webapi.RouteAnswer {
		t.Fatalf("prose at the open question routes %q, want an answer", got.Route)
	}
	st, raw := h.answer(c.ID, webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "chat", Words: "Go up the middle", Against: c.Decision.Against.Token})
	if st != http.StatusOK {
		t.Fatalf("answer: %d %s", st, raw)
	}
	h.waitCard(c.ID, "the question to close", func(c webapi.Card) bool {
		return c.Decision == nil || c.Decision.Kind != webapi.DecisionAsk
	})
	if p := lastAsk(t, h, c.ID); p.Answer != "Go up the middle" || !state.IsPersonActor(p.By) {
		t.Errorf("answer recorded as %+v", p)
	}
}

// Enter on a prose line at a freeform card's open question is the answer
// without "Chat about this" clicked first: the send path is the one the TUI's
// enter takes, so the words resolve the ask rather than being refused as a
// second turn.
func TestAFreeformQuestionTakesASendAsItsAnswer(t *testing.T) {
	h := newCardBoard(t, askingAgent())
	c := h.create(webapi.CreateCardRequest{Kind: "freeform", Title: "Poke at the persistence", Description: "ask me where"})
	c = h.waitCard(c.ID, "the question", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionAsk
	})
	if st, raw := h.send(c.ID, "Go up the middle"); st != http.StatusOK {
		t.Fatalf("send at the open question: %d %s", st, raw)
	}
	h.waitCard(c.ID, "the question to close", func(c webapi.Card) bool {
		return c.Decision == nil || c.Decision.Kind != webapi.DecisionAsk
	})
	if p := lastAsk(t, h, c.ID); p.Answer != "Go up the middle" || !state.IsPersonActor(p.By) {
		t.Errorf("answer recorded as %+v", p)
	}
}
