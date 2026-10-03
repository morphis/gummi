package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// opencodeServeReady bounds how long Compact waits for `opencode serve` to
// answer before giving up on it.
const opencodeServeReady = 30 * time.Second

// Compact implements Compactor. `opencode run` has no way to compact a
// session — a "/compact" line is just a prompt to it — so Compact starts a
// short-lived `opencode serve` in the worktree, with the same config and
// environment a turn runs with, asks it to summarize this session, and
// stops it. The next `run --session` turn then continues from the summary.
func (s *opencodeSession) Compact(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session closed")
	}
	if s.cancel != nil {
		return ErrBusy
	}
	if s.sessionID == "" {
		go func() {
			s.emit(Event{Kind: EventMessage, Text: "Nothing to compact yet: this conversation has not started."})
			s.emit(Event{Kind: EventIdle})
		}()
		return nil
	}
	provider, model, ok := strings.Cut(s.model, "/")
	if !ok {
		return fmt.Errorf("opencode model %q is not provider/model", s.model)
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return fmt.Errorf("opencode compact: %w", err)
	}
	password, err := randomToken()
	if err != nil {
		return fmt.Errorf("opencode compact: %w", err)
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, s.o.bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)) //nolint:gosec // bin is operator config, args are gummi-built
	cmd.Dir = s.workdir
	// a password, so nothing else on this host can drive the server for the
	// moment it is up
	cmd.Env = append(s.childEnv(), "OPENCODE_SERVER_PASSWORD="+password)
	setOpencodeGroup(cmd)
	stderr := &capWriter{max: 16 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting opencode serve: %w", err)
	}
	s.cancel = cancel
	srv := opencodeServer{
		base:     "http://127.0.0.1:" + strconv.Itoa(port),
		password: password,
	}
	go s.runCompact(procCtx, cmd, cancel, stderr, srv, s.sessionID, provider, model)
	return nil
}

func (s *opencodeSession) runCompact(ctx context.Context, cmd *exec.Cmd, cancel context.CancelFunc, stderr fmt.Stringer, srv opencodeServer, id, provider, model string) {
	err := srv.waitReady(ctx, id, opencodeServeReady)
	if err == nil {
		err = srv.summarize(ctx, id, provider, model)
	}
	cancel()
	_ = cmd.Wait()
	s.mu.Lock()
	s.cancel = nil
	closed := s.closed
	aborted := s.interrupted
	s.interrupted = false
	s.mu.Unlock()
	switch {
	case closed:
		return
	case aborted:
		s.emit(Event{Kind: EventMessage, Text: "Compaction stopped; the conversation is as it was."})
	case err != nil:
		s.emit(Event{Kind: EventError, Err: &RunFailure{
			Backend: "opencode", Diagnostic: strings.TrimSpace(stderr.String()),
			Err: fmt.Errorf("compacting the session: %w", err),
		}})
		return
	default:
		s.emit(Event{Kind: EventMessage, Text: "Compacted the conversation: the agent now carries a summary of it instead of the whole history."})
	}
	s.emit(Event{Kind: EventIdle})
}

// opencodeServer is the slice of `opencode serve`'s HTTP API Compact uses.
type opencodeServer struct {
	base     string
	password string
	client   *http.Client // nil → a client with no timeout; ctx bounds it
}

func (o opencodeServer) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.password != "" {
		req.SetBasicAuth("opencode", o.password)
	}
	c := o.client
	if c == nil {
		c = http.DefaultClient
	}
	return c.Do(req)
}

// waitReady polls until the server knows session id, or within is spent.
func (o opencodeServer) waitReady(ctx context.Context, id string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		// each poll on its own short clock: a request that reaches the
		// server while it is still starting can be held unanswered
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := o.do(pctx, http.MethodGet, "/session/"+url.PathEscape(id), nil)
		pcancel()
		if err == nil {
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				return nil
			case http.StatusNotFound:
				return fmt.Errorf("opencode no longer holds session %s", id)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("status %s", resp.Status)
			}
			return fmt.Errorf("opencode serve did not come up within %s: %w", within, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// summarize compacts session id with the given model. It blocks until
// opencode has written the summary.
func (o opencodeServer) summarize(ctx context.Context, id, provider, model string) error {
	resp, err := o.do(ctx, http.MethodPost, "/session/"+url.PathEscape(id)+"/summarize",
		map[string]string{"providerID": provider, "modelID": model})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("summarize: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func freeLoopbackPort() (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
