package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A request that reaches `opencode serve` while it is still starting can
// be held unanswered; the readiness poll must give up on it and ask again
// rather than wait on it for good.
func TestOpencodeServerWaitReadyOutlivesAHeldRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			<-r.Context().Done() // the first poll is never answered
			return
		}
		if r.URL.Path != "/session/ses_1" {
			t.Errorf("polled %s", r.URL.Path)
		}
		if u, p, _ := r.BasicAuth(); u != "opencode" || p != "pw" {
			t.Errorf("auth = %q/%q", u, p)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	o := opencodeServer{base: srv.URL, password: "pw"}
	if err := o.waitReady(context.Background(), "ses_1", 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestOpencodeServerWaitReadyNamesALostSession(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := opencodeServer{base: srv.URL}.waitReady(context.Background(), "ses_gone", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "ses_gone") {
		t.Fatalf("err = %v, want it to name the lost session", err)
	}
}

func TestOpencodeServerSummarize(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses_1/summarize" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["modelID"] == "bad" {
			http.Error(w, "no such model", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()
	o := opencodeServer{base: srv.URL}
	if err := o.summarize(context.Background(), "ses_1", "opencode", "mimo"); err != nil {
		t.Fatal(err)
	}
	if got["providerID"] != "opencode" || got["modelID"] != "mimo" {
		t.Errorf("body = %v", got)
	}
	err := o.summarize(context.Background(), "ses_1", "opencode", "bad")
	if err == nil || !strings.Contains(err.Error(), "no such model") {
		t.Errorf("err = %v, want the server's reason", err)
	}
}

// With no turn behind it there is no opencode session to compact: Compact
// says so and ends idle, without starting a server.
func TestOpencodeCompactBeforeAnyTurn(t *testing.T) {
	s := &opencodeSession{o: &Opencode{bin: "/nonexistent"}, model: "p/m", raw: make(chan Event, 4), events: make(chan Event), stop: make(chan struct{})}
	go s.forward()
	defer s.Close()
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	for e := range s.Events() {
		kinds = append(kinds, e.Kind)
		if e.Kind == EventIdle {
			break
		}
	}
	if len(kinds) != 2 || kinds[0] != EventMessage {
		t.Errorf("events = %v, want a message then idle", kinds)
	}
}
