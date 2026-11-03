//go:build opencode_integration

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOpencodeLiveStreamDrops drives real opencode turns through a proxy
// that cuts the server's event stream at random, every second or few, the
// way a server that drops it mid-turn does (an unexpected EOF on the
// read), while the caller takes its events slowly. Every turn must still
// end idle, with the work done and its reply relayed. Several sessions run
// side by side, each on its own server. Like the rest of this file it
// spawns the binary GUMMI_OPENCODE_BIN names and runs on a free model;
// GUMMI_OPENCODE_STRESS_SESSIONS and _TURNS size it, _READ_MS is how long
// the caller takes over each event (20), _SLEEP_S has each turn run a
// silent `sleep` that long first, and _CUT=0 leaves the streams whole, so
// only a slow caller or a quiet turn is left to lose them.
func TestOpencodeLiveStreamDrops(t *testing.T) {
	bin := ocIntegrationBin()
	sessions, turns := envInt("GUMMI_OPENCODE_STRESS_SESSIONS", 3), envInt("GUMMI_OPENCODE_STRESS_TURNS", 3)
	readDelay := time.Duration(envInt("GUMMI_OPENCODE_STRESS_READ_MS", 20)) * time.Millisecond
	cut := os.Getenv("GUMMI_OPENCODE_STRESS_CUT") != "0"
	pause := 0
	if os.Getenv("GUMMI_OPENCODE_STRESS_SLEEP_S") != "" {
		pause = envInt("GUMMI_OPENCODE_STRESS_SLEEP_S", 0)
	}

	var cuts atomic.Int64
	old := serveOpencode
	serveOpencode = func(ctx context.Context, bin string, port int, dir string, env []string) (*opencodeProc, error) {
		p, err := old(ctx, bin, port, dir, env)
		if err != nil {
			return nil, err
		}
		p.url = cuttingProxy(t, p.url, cut, &cuts)
		return p, nil
	}
	t.Cleanup(func() { serveOpencode = old })

	ag, err := NewOpencode(bin)
	if err != nil {
		t.Skip(err)
	}
	defer ag.Close()

	var wg sync.WaitGroup
	for n := range sessions {
		wg.Go(func() { stressSession(t, ag, n, turns, pause, readDelay) })
	}
	wg.Wait()
	t.Logf("event streams cut: %d", cuts.Load())
	if cut && cuts.Load() == 0 {
		t.Error("no event stream was cut: the test proved nothing")
	}
}

func stressSession(t *testing.T, ag Agent, n, turns, pause int, readDelay time.Duration) {
	dir := t.TempDir()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: dir, Model: ocIntegrationModel()})
	if err != nil {
		t.Errorf("session %d: %v", n, err)
		return
	}
	defer sess.Close()
	for turn := range turns {
		name := fmt.Sprintf("f%d.txt", turn)
		word := fmt.Sprintf("W%d%d", n, turn)
		prompt := ""
		if pause > 0 {
			prompt = fmt.Sprintf("First run the shell command `sleep %d` and wait for it to finish. Then ", pause)
		}
		prompt += fmt.Sprintf("Use your tools: write a file named %s holding the single line %s, "+
			"then run the shell command `cat %s` and `ls`, then reply with exactly the word DONE-%s and nothing else.",
			name, word, name, word)
		start := time.Now()
		if err := sess.Send(context.Background(), prompt); err != nil {
			t.Errorf("session %d turn %d: send: %v", n, turn, err)
			return
		}
		text, tools, err := slowTurn(sess, readDelay, 5*time.Minute)
		if err != nil {
			t.Errorf("session %d turn %d: %v (after %s, %d tool calls, text %q)", n, turn, err, time.Since(start).Round(time.Second), tools, text)
			return
		}
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !strings.Contains(string(b), word) {
			t.Errorf("session %d turn %d: %s = %q (%v)", n, turn, name, b, err)
		}
		if c := strings.Count(text, "DONE-"+word); c != 1 {
			t.Errorf("session %d turn %d: reply %q, want DONE-%s once", n, turn, text, word)
		}
		t.Logf("session %d turn %d: ok in %s, %d tool calls", n, turn, time.Since(start).Round(time.Second), tools)
	}
}

// slowTurn reads one turn's events to its end, taking delay over each, as
// a caller that falls behind does. It returns the turn's text, how many tool
// calls it made, and its failure.
func slowTurn(sess Session, delay, within time.Duration) (string, int, error) {
	var text strings.Builder
	tools := 0
	deadline := time.After(within)
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				return text.String(), tools, errors.New("the event channel closed")
			}
			switch e.Kind {
			case EventTextDelta:
				text.WriteString(e.Text)
			case EventToolCall:
				tools++
			case EventIdle:
				return text.String(), tools, nil
			case EventError:
				var rf *RunFailure
				if errors.As(e.Err, &rf) {
					return text.String(), tools, e.Err
				}
			}
			time.Sleep(delay)
		case <-deadline:
			return text.String(), tools, errors.New("no end in time")
		}
	}
}

// cuttingProxy fronts the server at target, aborting every event stream
// it relays after a random one to four seconds when cut is set; other
// requests pass through. It returns the proxy's URL.
func cuttingProxy(t *testing.T, target string, cut bool, cuts *atomic.Int64) string {
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1
	rp.ErrorLog = log.New(io.Discard, "", 0) // the readiness polls before the server listens
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cut || r.URL.Path != "/event" && r.URL.Path != "/api/event" {
			rp.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		timer := time.AfterFunc(time.Second+time.Duration(rand.Int64N(int64(3*time.Second))), func() {
			cuts.Add(1)
			cancel()
		})
		defer timer.Stop()
		rp.ServeHTTP(w, r.WithContext(ctx))
		if ctx.Err() != nil && r.Context().Err() == nil {
			panic(http.ErrAbortHandler) // drop the connection mid-body: an unexpected EOF
		}
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

func envInt(name string, def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(name), &n); err != nil || n < 1 {
		return def
	}
	return n
}
