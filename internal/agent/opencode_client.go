package agent

import (
	"bufio"
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
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// opencodeServeReady bounds how long a caller waits for `opencode serve`
// to answer before giving up on it.
const opencodeServeReady = 30 * time.Second

// ocTurnTail is how long a turn waits for its event bus to relay the last
// events after the message POST has returned: the server publishes a
// turn's final parts on the bus microseconds before it completes the
// POST, and finishing without them would read a complete turn as empty.
const ocTurnTail = 250 * time.Millisecond

// errOcLostSession reports that the server has no session under the id a
// turn addressed. A resume handed an id the server cannot find is not a
// failed turn: the adapter drops the id and runs the turn again on a
// fresh conversation, at most once.
var errOcLostSession = errors.New("opencode no longer holds the session")

// opencodeServer is the HTTP client for one `opencode serve` process: the
// slice of its API the adapter speaks. It is a value type — a handle, not
// a connection — and every call bounds itself by its context.
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

// waitReady polls until the server answers at all, or within is spent.
// A request that reaches `opencode serve` while it is still starting can
// be held unanswered; each poll runs on its own short clock so one can
// give up and ask again rather than wait on it for good.
func (o opencodeServer) waitReady(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	for {
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := o.do(pctx, http.MethodGet, "/config", nil)
		pcancel()
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %s", resp.Status)
		}
		last = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("opencode serve did not come up within %s: %w", within, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// events opens the server's global event bus (a text/event-stream that
// emits one JSON object per event as a `data:` line). The stream stays
// open until the server dies or the context ends; a read error on it is
// the caller's signal to reconnect or to give up on the server.
func (o opencodeServer) events(ctx context.Context) (io.ReadCloser, error) {
	resp, err := o.do(ctx, http.MethodGet, "/event", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("opencode event stream: status %s", resp.Status)
	}
	return resp.Body, nil
}

// create opens a server session and returns its id. The id is read from
// the create response — never scraped from later event lines.
func (o opencodeServer) create(ctx context.Context) (string, error) {
	resp, err := o.do(ctx, http.MethodPost, "/session", map[string]any{})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("creating the session: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("creating the session: no session id in the response")
	}
	return out.ID, nil
}

// message posts one prompt to the session and blocks until the turn
// resolves — a clean end, an abort, or a server-side error. A session the
// server does not know answers ocLostSession.
func (o opencodeServer) message(ctx context.Context, id string, body any) error {
	resp, err := o.do(ctx, http.MethodPost, "/session/"+url.PathEscape(id)+"/message", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", errOcLostSession, id)
	default:
		return fmt.Errorf("sending the message: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
}

// abort stops the session's in-flight turn server-side. The turn's own
// POST returns as aborted; partial work is aborted, not lost.
func (o opencodeServer) abort(ctx context.Context, id string) error {
	resp, err := o.do(ctx, http.MethodPost, "/session/"+url.PathEscape(id)+"/abort", nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aborting the session: %s", resp.Status)
	}
	return nil
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

// respond answers a permission request the server raised: "once" lets
// this one call through, "reject" refuses it.
func (o opencodeServer) respond(ctx context.Context, sessionID, requestID, reply string) error {
	resp, err := o.do(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/permissions/"+url.PathEscape(requestID),
		map[string]string{"response": reply})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answering the permission: %s", resp.Status)
	}
	return nil
}

// rejectQuestion refuses a question opencode's own question tool raised:
// the tool call fails and the turn carries on, instead of holding until
// an answer gummi has no way to give.
func (o opencodeServer) rejectQuestion(ctx context.Context, requestID string) error {
	resp, err := o.do(ctx, http.MethodPost, "/question/"+url.PathEscape(requestID)+"/reject", map[string]any{})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rejecting the question: %s", resp.Status)
	}
	return nil
}

// providers returns the model ids the server's own catalog offers — the
// full provider/model pairs under /config/providers, one per model,
// sorted for a stable picker.
func (o opencodeServer) providers(ctx context.Context) ([]string, error) {
	cat, err := o.catalog(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, p := range cat.Providers {
		for mid := range p.Models {
			ids = append(ids, p.ID+"/"+mid)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// contextLimit returns the context window opencode's catalog declares for
// one provider/model, or 0 when the catalog does not know the model.
func (o opencodeServer) contextLimit(ctx context.Context, provider, model string) (int64, error) {
	cat, err := o.catalog(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range cat.Providers {
		if p.ID == provider {
			return p.Models[model].Limit.Context, nil
		}
	}
	return 0, nil
}

// ocCatalog is the slice of /config/providers the adapter reads.
type ocCatalog struct {
	Providers []struct {
		ID     string `json:"id"`
		Models map[string]struct {
			ID    string `json:"id"`
			Limit struct {
				Context int64 `json:"context"`
			} `json:"limit"`
		} `json:"models"`
	} `json:"providers"`
}

func (o opencodeServer) catalog(ctx context.Context) (*ocCatalog, error) {
	resp, err := o.do(ctx, http.MethodGet, "/config/providers", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode providers: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out ocCatalog
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("opencode providers: %w", err)
	}
	return &out, nil
}

// opencodeProc is one running `opencode serve` process: the handle a
// session holds so Close kills it.
type opencodeProc struct {
	cmd *exec.Cmd
	// cancel kills the process group (serve spawns tool and MCP children
	// that would otherwise outlive it).
	cancel context.CancelFunc
	// url is where the server answers — what the spawned process listens
	// on. The real spawn reports its own port; a test's stand-in reports
	// the URL its fake runs on.
	url string
	// stderr is the process's bounded stderr capture, for diagnostics when
	// the server never comes up.
	stderr fmt.Stringer
}

// serveOpencode starts one `opencode serve` bound to port in dir, running
// with env. It returns once the process is running — readiness is the
// caller's poll, not its problem. Production uses the real binary; tests
// rebind the variable to run their fake server instead (see
// opencode_client_test.go).
var serveOpencode = func(ctx context.Context, bin string, port int, dir string, env []string) (*opencodeProc, error) {
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)) //nolint:gosec // bin is operator config, args are gummi-built
	cmd.Dir = dir
	cmd.Env = env
	setOpencodeGroup(cmd)
	stderr := &capWriter{max: 16 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return &opencodeProc{
		cmd: cmd, cancel: cancel, stderr: stderr,
		url: "http://127.0.0.1:" + strconv.Itoa(port),
	}, nil
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

// envWithout is os.Environ() minus every variable named — the environment
// a probe's own process runs with, when one variable must not reach it.
func envWithout(names ...string) []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		drop := false
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// scanSSELines reads a text/event-stream body, handing each event's data
// payload to fn. opencode's bus frames every event as a `data: {json}`
// line followed by a blank line; other lines (comments, heartbeats'
// keep-alives) are ignored.
func scanSSELines(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		if err := fn(line[len("data: "):]); err != nil {
			return err
		}
	}
	return sc.Err()
}
