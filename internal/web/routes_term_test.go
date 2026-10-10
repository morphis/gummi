package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/webapi"
)

func withTerminal(o *Options) { o.Terminal = true }

// dialTerm opens card id's terminal as client c, from the page's origin
// unless origin says otherwise.
func (h *harness) dialTerm(c *http.Client, id, origin string) (*websocket.Conn, *http.Response, error) {
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.http.URL, "http")+"/api/cards/"+id+"/term?cols=90&rows=20",
		&websocket.DialOptions{HTTPClient: c, HTTPHeader: hdr})
}

// termUntil reads the socket until its binary frames hold want.
func termUntil(t *testing.T, conn *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no %q on the terminal: %v; got %q", want, err, out.String())
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
	return out.String()
}

func TestTheTerminalIsAShellInTheCardsWorktree(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	b := newDocsBoard(t, agent.NewFake("ok"), withTerminal)
	ctx := context.Background()

	var c webapi.Card
	if st := b.get("/api/cards/FD-001", &c); st != http.StatusOK || !c.Terminal {
		t.Fatalf("card = %d, terminal %v; want it offered", st, c.Terminal)
	}

	conn, _, err := b.dialTerm(b.c, "FD-001", b.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo in:$(pwd -P):end; stty size\n")); err != nil {
		t.Fatal(err)
	}
	if out := termUntil(t, conn, "20 90"); !strings.Contains(out, "in:"+b.wt+":end") {
		t.Errorf("the shell is not in the worktree %s: %q", b.wt, out)
	}
	if !b.logged("opened a terminal in FD-001") {
		t.Error("opening a terminal was not logged")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"cols":100,"rows":30}`)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Write(ctx, websocket.MessageBinary, []byte("stty size\n"))
	termUntil(t, conn, "30 100")
	_ = conn.CloseNow()

	// the shell outlived the socket: the next one is replayed its output
	again, _, err := b.dialTerm(b.c, "FD-001", b.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.CloseNow() }()
	termUntil(t, again, "in:"+b.wt+":end")

	// exit says so, then closes
	_ = again.Write(ctx, websocket.MessageBinary, []byte("exit 7\n"))
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		typ, data, err := again.Read(rctx)
		if err != nil {
			t.Fatalf("the socket ended without an exit message: %v", err)
		}
		if typ == websocket.MessageText {
			if string(data) != `{"exit":7}` {
				t.Errorf("exit message = %s", data)
			}
			break
		}
	}
}

func TestTheTerminalIsRefused(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	b := newDocsBoard(t, agent.NewFake("ok"), withTerminal)

	status := func(c *http.Client, id, origin string) int {
		t.Helper()
		conn, res, err := b.dialTerm(c, id, origin)
		if err == nil {
			_ = conn.CloseNow()
			return http.StatusSwitchingProtocols
		}
		if res == nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode
	}
	if got := status(b.client(), "FD-001", b.http.URL); got != http.StatusUnauthorized {
		t.Errorf("an unpaired browser = %d, want 401", got)
	}
	if got := status(b.c, "FD-001", "https://evil.example"); got != http.StatusForbidden {
		t.Errorf("another origin = %d, want 403", got)
	}
	if got := status(b.c, "FD-001", ""); got != http.StatusForbidden {
		t.Errorf("no origin = %d, want 403", got)
	}
	if got := status(b.c, "FD-002", b.http.URL); got != http.StatusNotFound {
		t.Errorf("a card with no worktree = %d, want 404", got)
	}
	if b.logged("opened a terminal") {
		t.Error("a refused terminal started a shell")
	}
}

func TestTheTerminalIsOffUnlessAskedFor(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	var c webapi.Card
	if st := b.get("/api/cards/FD-001", &c); st != http.StatusOK || c.Terminal {
		t.Fatalf("card = %d, terminal %v; want it not offered", st, c.Terminal)
	}
	conn, res, err := b.dialTerm(b.c, "FD-001", b.http.URL)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("a server without --terminal opened one")
	}
	if res != nil {
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	}
}

func TestATerminalNeedsPairing(t *testing.T) {
	h := newHarness(t)
	_, err := New(Options{Board: h.bridge, Devices: h.devices, Pairing: h.pairing, OpenAccess: true, Terminal: true})
	if err == nil {
		t.Fatal("a server without pairing was given a terminal")
	}
}

func TestAnUnpairedDeviceLosesItsTerminal(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	old := termRecheck
	termRecheck = 20 * time.Millisecond
	t.Cleanup(func() { termRecheck = old })
	b := newDocsBoard(t, agent.NewFake("ok"), withTerminal)

	conn, _, err := b.dialTerm(b.c, "FD-001", b.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if res, _ := b.do(b.c, http.MethodPost, "/api/unpair", "{}"); res.StatusCode != http.StatusOK {
		t.Fatalf("unpair = %d", res.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Errorf("the socket ended with %v, want a policy close", err)
			}
			return
		}
	}
}
