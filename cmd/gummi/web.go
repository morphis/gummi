package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/agentplugins"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/web"
	"github.com/morphis/gummi/internal/web/push"
	"github.com/morphis/gummi/internal/webapi"
)

// `gummi web` serves the board to a browser (DESIGN §20). It is a board
// host without a terminal: it builds the board exactly as the TUI does
// (openBoard), runs the TUI's own model with no renderer, and serves the
// page from the same process. It takes the workspace's instance lock like
// the TUI, so one board has one interactive host at a time.
const (
	// defaultWebAddr is loopback on purpose: a board on 0.0.0.0 is a
	// terminal on your repository, open to the network. --tailscale is how
	// a phone reaches it (or `tailscale serve` in front of it).
	defaultWebAddr = "127.0.0.1:7878"
	// defaultTSHostname is the board's node name on the tailnet.
	defaultTSHostname = "gummi"
	// tsnetDir holds the tailnet node's identity under the workspace's web
	// state, so the board keeps its name and login between runs.
	tsnetDir = "tsnet"
	// webGrace is how long the server and the board get to unwind on
	// Ctrl-C.
	webGrace = 5 * time.Second
	// webIdleTimeout closes a keep-alive connection nobody is using.
	webIdleTimeout = 2 * time.Minute
	// pushAllowPrivateEnv set to 1 lets push endpoints resolve to loopback
	// and private addresses. It exists for a test's stand-in push service
	// and nothing else: a paired device could otherwise point the host's
	// POSTs at anything on its network.
	pushAllowPrivateEnv = "GUMMI_WEB_PUSH_ALLOW_PRIVATE"
	// serverFile records where a running server listens and the token that
	// authenticates `gummi web pair` to it; devicesFile is the paired
	// devices.
	serverFile  = "server.json"
	devicesFile = "devices.json"
)

// runWeb implements `gummi web`.
func runWeb(fl cliFlags, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("gummi web takes no arguments (got %q)", args[0])
	}
	certFile, keyFile := fl.String("tls-cert"), fl.String("tls-key")
	if (certFile == "") != (keyFile == "") {
		return errors.New("--tls-cert and --tls-key go together: give both, or neither")
	}
	secure := certFile != ""
	tailscale := fl.Bool("tailscale")
	if !tailscale {
		for _, name := range []string{"ts-hostname", "ts-authkey", "ts-tls", "verbose"} {
			if fl.Changed(name) {
				return fmt.Errorf("--%s configures the tailnet node; it needs --tailscale", name)
			}
		}
	}
	noPairing := fl.Bool("no-pairing")
	if noPairing && tailscale {
		return errors.New("--no-pairing serves the board to anyone who can reach it, so it is only allowed " +
			"when every listener is loopback — drop --tailscale, or pair the browser instead")
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05")+" "+format+"\n", args...)
	}
	addr := cmp.Or(fl.String("addr"), os.Getenv("GUMMI_WEB_ADDR"), defaultWebAddr)
	if noPairing && !loopbackAddr(addr) {
		return errNoPairingOffLoopback
	}
	// The names the board answers to besides its own address and loopback
	// (DNS rebinding: internal/web's checkHost). Read before anything is
	// held, so a certificate that will not load fails the start cleanly.
	hosts, err := webHosts(addr, certFile, keyFile, fl.String("allow-host"))
	if err != nil {
		return err
	}
	if noPairing {
		// a name the board answers to beyond loopback is a proxy in front
		// of it (`tailscale serve`, a reverse proxy): through it, an
		// unpaired board is open to whoever reaches the proxy
		if err := noPairingHosts(fl.String("allow-host")); err != nil {
			return err
		}
	}

	// Signals are caught before anything is held. SIGHUP too: a closed
	// terminal or a killed tmux pane is a quit like Ctrl-C, and must unwind
	// the same way — the instance record and server.json removed, the lock
	// released — rather than die where it stands and leave them behind for
	// the next host to trip over.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// Take the board first, then listen: a second `gummi web` on the same
	// address must be refused by the lock, which names the holder and
	// where it serves, not by the kernel's "address already in use". The
	// record's URL is filled in once the listener has one.
	h, err := openBoard(boardOpts{
		holder: state.InstanceHolder{Host: state.HostWeb},
		// No bell: this terminal is the server's log. A person away from it
		// is told by the page (and, once paired for it, by Web Push).
		notifyDefault: notify.Off,
		notifyOut:     os.Stderr,
	})
	if err != nil {
		return err
	}
	defer h.Close()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	defer func() { _ = ln.Close() }()
	if noPairing && !loopbackOnly(ln) {
		return errNoPairingOffLoopback
	}
	url := listenURL(ln, secure)
	if err := state.SetInstanceURL(h.ws, url); err != nil {
		logf("web: %v", err)
	}
	if !loopbackOnly(ln) {
		// Off loopback a person types this machine's name as often as its
		// address; neither is a name somebody else's DNS can point here.
		if name, err := os.Hostname(); err == nil && name != "" {
			hosts = append(hosts, name, name+".local")
		}
	}

	if err := os.MkdirAll(h.ws.WebDir(), 0o700); err != nil {
		return fmt.Errorf("preparing %s: %w", h.ws.WebDir(), err)
	}
	devices, err := web.OpenDevices(filepath.Join(h.ws.WebDir(), devicesFile), nil)
	if err != nil {
		return err
	}
	pairing := web.NewPairing(nil)
	adminToken, err := web.NewAdminToken()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()
	_, defaultRepo, namedRepos, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	pluginRepos := make([]agentplugins.Repo, 0, len(namedRepos)+1)
	if defaultRepo != "" {
		pluginRepos = append(pluginRepos, agentplugins.Repo{Name: "default", Root: defaultRepo})
	}
	for _, repo := range namedRepos {
		pluginRepos = append(pluginRepos, agentplugins.Repo{Name: repo.Name, Root: repo.Root})
	}
	pluginStore, err := agentplugins.New(h.ws.Root, pluginRepos)
	if err != nil {
		return fmt.Errorf("preparing agent plugin manager: %w", err)
	}

	// Web Push: the VAPID key and the subscriptions live beside the
	// paired devices; a card that starts needing someone is a
	// notification on every subscribed device.
	pusher, err := web.OpenPush(h.ws.WebDir())
	if err != nil {
		return err
	}
	pusher.Notifier.OnError = func(sub push.Subscription, err error) {
		logf("web: notifying %s failed: %v", sub.Device, err)
	}
	if os.Getenv(pushAllowPrivateEnv) == "1" {
		// test-only: a stand-in push service on loopback (the e2e suite)
		pusher.Sender.AllowPrivate = true
		logf("web: %s=1 — push endpoints on loopback and private addresses are allowed (for tests only)", pushAllowPrivateEnv)
	}
	h.shell.AddAttentionNotifier(pusher)

	bridge := ui.NewHeadless(h.shell)
	srv, err := web.New(web.Options{
		Push:       pusher,
		Board:      bridge,
		Devices:    devices,
		Pairing:    pairing,
		Log:        logf,
		Repo:       filepath.Base(h.ws.Root),
		Host:       hostname,
		Version:    version(),
		WebDir:     h.ws.WebDir(),
		Plugins:    pluginStore,
		OpenAccess: noPairing,
		Hosts:      hosts,
		Secure:     secure,
		AdminToken: adminToken,
		// ?deep=1 is `gummi doctor --deep`: the live per-role probe, opt-in
		// on both surfaces because it asks every backend for a turn.
		Doctor: func(r *http.Request) webapi.Doctor {
			deep := r.URL.Query().Get("deep") == "1"
			opts := doctorOpts{Deep: deep, Probe: probeModel}
			if h.engine != nil {
				opts.Board = h.engine
			}
			return doctorForWeb(buildDoctorReport(cwd, opts))
		},
	})
	if err != nil {
		return err
	}
	// The model tells the server what changed; the server tells the pages.
	h.shell.SetChangeHook(srv.Publish)

	boardDone := make(chan error, 1)
	go func() { boardDone <- bridge.Run() }()

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       webIdleTimeout,
		// No ReadTimeout and no WriteTimeout: the event stream is a
		// response that lasts as long as the page is open, and either
		// would cut it. A request's body gets its own read deadline
		// (internal/web's readDeadline), and each write to the stream
		// carries its own deadline (internal/web's hub).
	}
	// One per listener, so neither blocks when both end at Shutdown.
	serveDone := make(chan error, 2)
	go func() {
		if secure {
			serveDone <- httpSrv.ServeTLS(ln, certFile, keyFile)
		} else {
			serveDone <- httpSrv.Serve(ln)
		}
	}()

	if err := writeServerFile(h.ws, ln.Addr().String(), adminToken, secure); err != nil {
		logf("web: %v (`gummi web pair` will not find this server)", err)
	}
	defer func() { _ = os.Remove(filepath.Join(h.ws.WebDir(), serverFile)) }()

	announce(logf, url, noPairing, devices, pairing)
	if !secure && !loopbackOnly(ln) {
		logf("web: WARNING — serving plain HTTP on %s, which is not loopback: the pairing code and every device's token cross the network in clear, readable by anyone on the path. "+
			"Serve on 127.0.0.1 behind `tailscale serve`, use --tailscale, or give --tls-cert/--tls-key.", ln.Addr())
	}

	// The tailnet node joins after the lock is taken, not before: a second
	// `gummi web --tailscale` on this workspace must be refused by the
	// lock, never bring up a second node on the same identity. Joining may
	// wait on a person opening a login URL on another device, so it runs
	// beside the loopback listener, which is serving already.
	var tailnetUp chan tailnetResult
	if tailscale {
		tailnetUp = make(chan tailnetResult, 1)
		go func() {
			tn, err := joinTailnet(ctx, fl, h.ws, ln, logf)
			tailnetUp <- tailnetResult{tn, err}
		}()
	}
	var tailnet *web.Tailnet
	defer func() {
		if tailnet != nil {
			_ = tailnet.Close()
		}
	}()

	var runErr error
wait:
	for {
		select {
		case <-ctx.Done():
			logf("web: closing the board…")
			// Closing the web host is quitting the board, as q is in the TUI
			// (Shell.quitNow): autopilot cards still running are stopped and
			// marked, so the next host to open the board offers to pick them
			// back up (Board.Resume) instead of leaving them silently parked.
			if h.engine != nil {
				h.engine.StopForQuit(context.Background())
			}
			break wait
		case r := <-tailnetUp:
			tailnetUp = nil
			if r.err != nil {
				if ctx.Err() != nil {
					continue // Ctrl-C while waiting for the login; the case above says so
				}
				// --tailscale was asked for: a board that is not where it
				// was promised is a failure, not a quieter loopback board.
				runErr = r.err
				break wait
			}
			tailnet = r.tn
			srv.AllowHosts(tailnetHosts(tailnet)...)
			go func(l net.Listener) { serveDone <- httpSrv.Serve(l) }(tailnet.Listener)
			tsURL := tailnet.URL()
			if err := state.SetInstanceURL(h.ws, tsURL); err != nil {
				logf("web: %v", err)
			}
			fmt.Printf("gummi web: serving %s\n", tsURL)
		case err := <-boardDone:
			runErr = fmt.Errorf("the board stopped: %w", err)
			break wait
		case err := <-serveDone:
			if !errors.Is(err, http.ErrServerClosed) {
				runErr = fmt.Errorf("serving the board: %w", err)
			}
			break wait
		}
	}
	if tailnetUp != nil {
		// Stopped while the node was still joining: it returns as soon as
		// it sees ctx end, and a node that made it up anyway is closed.
		select {
		case r := <-tailnetUp:
			if r.tn != nil {
				_ = r.tn.Close()
			}
		case <-time.After(webGrace):
		}
	}
	// Streams first, so Shutdown is not left waiting on connections that
	// never go idle; then the listener; then the model.
	srv.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), webGrace)
	defer cancel()
	_ = httpSrv.Shutdown(shutdown)
	select {
	case <-bridge.Done():
	default:
		bridge.Stop()
	}
	return runErr
}

type tailnetResult struct {
	tn  *web.Tailnet
	err error
}

// joinTailnet brings up the board's own tailnet node (DESIGN §20.4) and
// listens on it. A first run prints the login URL on stdout and waits,
// without a timeout, for a person to open it; ctx ends the wait.
func joinTailnet(ctx context.Context, fl cliFlags, ws state.Workspace, local net.Listener, logf func(string, ...any)) (*web.Tailnet, error) {
	o := tailnetOptions(fl, ws, local)
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("preparing %s: %w", o.StateDir, err)
	}
	o.Logf = logf
	o.OnLogin = func(url string) {
		fmt.Printf("tailscale: open %s to add this board to your tailnet\n", url)
	}
	logf("web: joining your tailnet as %q…", o.Hostname)
	return web.ListenTailnet(ctx, o)
}

// tailnetHosts are the names the board answers to on the tailnet: the
// node's MagicDNS name, its first label (what a tailnet device's search
// domain resolves), and its addresses.
func tailnetHosts(t *web.Tailnet) []string {
	var out []string
	if t.DNSName != "" {
		out = append(out, t.DNSName)
		if short, _, ok := strings.Cut(t.DNSName, "."); ok && short != "" {
			out = append(out, short)
		}
	}
	for _, ip := range t.IPs {
		out = append(out, ip.String())
	}
	return out
}

// tailnetOptions reads the node's configuration off the flags: its state
// under the workspace's web dir, HTTPS on 443 with --ts-tls or else plain
// HTTP on the loopback listener's port, and the auth key from --ts-authkey
// or else TS_AUTHKEY, tailscale's own variable (which keeps it out of ps).
func tailnetOptions(fl cliFlags, ws state.Workspace, local net.Listener) web.TailnetOptions {
	tlsOn := fl.Bool("ts-tls")
	port := 0
	if !tlsOn {
		if _, p, err := net.SplitHostPort(local.Addr().String()); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	return web.TailnetOptions{
		Hostname: cmp.Or(fl.String("ts-hostname"), defaultTSHostname),
		StateDir: filepath.Join(ws.WebDir(), tsnetDir),
		Port:     port,
		TLS:      tlsOn,
		AuthKey:  cmp.Or(fl.String("ts-authkey"), os.Getenv("TS_AUTHKEY")),
		Verbose:  fl.Bool("verbose"),
	}
}

// listenURL is the address a person opens.
func listenURL(ln net.Listener, secure bool) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return scheme + "://" + ln.Addr().String()
}

// doctorForWeb is the readiness report in the contract's shape.
func doctorForWeb(r doctorReport) webapi.Doctor {
	out := webapi.Doctor{Ready: r.Ready, Checks: make([]webapi.DoctorCheck, 0, len(r.Checks))}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, webapi.DoctorCheck{Name: c.Name, Status: c.Status, Detail: c.Detail, Remediation: c.Remediation})
	}
	return out
}

// announce prints what an operator needs: where the board is, and how to
// get a browser onto it.
//
// The code printed here goes to this terminal and nowhere else — not to
// server.json, not to any file under .gummi — which is why a device
// paired with it is let in without asking a paired one (internal/web's
// approval.go). Keep it that way: a code that crosses a file an agent
// can read is a code an agent can redeem. (If this terminal's output is
// itself sent to a file, that file carries the code.)
func announce(logf func(string, ...any), url string, open bool, devices *web.Devices, pairing *web.Pairing) {
	fmt.Printf("gummi web: serving %s\n", url)
	switch {
	case open:
		logf("web: pairing is off — anything that can reach this listener has the board")
	case devices.Count() == 0:
		code, expires, err := pairing.Mint()
		if err != nil {
			logf("web: could not mint a pairing code: %v", err)
			return
		}
		logf("web: pairing code %s — enter it in the browser (good for %s)", code, time.Until(expires).Round(time.Second))
	default:
		n := devices.Count()
		logf("web: %d paired device%s; `gummi web pair` prints a code for a new one, which a paired device then lets in", n, cardPlural(n))
	}
}

// serverRef is the file a running server leaves behind so the other web
// subcommands can find it.
type serverRef struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	// TLS says the listener at Addr speaks HTTPS (--tls-cert).
	TLS bool `json:"tls,omitempty"`
}

func writeServerFile(ws state.Workspace, addr, token string, tls bool) error {
	b, err := json.MarshalIndent(serverRef{Addr: addr, Token: token, PID: os.Getpid(), TLS: tls}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(ws.WebDir(), serverFile)
	if err := atomicfile.Write(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func readServerFile(ws state.Workspace) (serverRef, error) {
	path := filepath.Join(ws.WebDir(), serverFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return serverRef{}, errors.New("no `gummi web` is running for this workspace — start one, and it prints a pairing code itself")
	}
	if err != nil {
		return serverRef{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var ref serverRef
	if err := json.Unmarshal(b, &ref); err != nil {
		return serverRef{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return ref, nil
}

// runWebPair implements `gummi web pair [--name]`: ask the running server
// for a code, bound to a person's name when --name is given (the browser
// then asks only for the code). It is printed here, in the terminal of whoever asked — who is
// already on this machine as the operator, since the admin route answers
// only on loopback and only to the token in the server's 0600 file.
func runWebPair(fl cliFlags) error {
	return withWebWorkspace(func(ws state.Workspace) error {
		ref, err := readServerFile(ws)
		if err != nil {
			return err
		}
		target, err := adminTarget(ref.Addr)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		name := fl.String("name")
		body, err := json.Marshal(webapi.AdminPairRequest{Name: name})
		if err != nil {
			return err
		}
		scheme := "http"
		if ref.TLS {
			scheme = "https"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, scheme+"://"+target+"/api/admin/pair", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+ref.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := adminClient(ref.TLS).Do(req)
		if err != nil {
			return fmt.Errorf("reaching the server at %s: %w (is it still running?)", target, err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			var refusal webapi.Error
			if json.NewDecoder(res.Body).Decode(&refusal) == nil && refusal.Error != "" {
				return fmt.Errorf("the server refused to mint a code (%s: %s)", res.Status, refusal.Error)
			}
			return fmt.Errorf("the server refused to mint a code (%s)", res.Status)
		}
		var code webapi.AdminPairResponse
		if err := json.NewDecoder(res.Body).Decode(&code); err != nil {
			return fmt.Errorf("reading the server's answer: %w", err)
		}
		if name != "" {
			fmt.Printf("pairing code %s — pairs a browser as %s (good for %ds)\n", code.Code, name, code.ExpiresInSecs)
		} else {
			fmt.Printf("pairing code %s — enter it in the browser (good for %ds)\n", code.Code, code.ExpiresInSecs)
		}
		if devices, err := web.OpenDevices(filepath.Join(ws.WebDir(), devicesFile), nil); err == nil && devices.Count() > 0 {
			// Said here so nobody waits on the phone wondering: a code
			// from this command pairs a browser that a paired one must
			// then let in (DESIGN §20.3) — and there is deliberately no
			// command that does that instead.
			fmt.Println("the browser then waits until you approve it on a device already paired with this board")
		}
		return nil
	})
}

// adminTarget is where `gummi web pair` reaches a server listening on
// addr. The admin route answers only on loopback, so a server on every
// interface is reached through loopback, and one bound to a single
// non-loopback address cannot be reached by it at all — which is said
// plainly rather than as the server's bare 403.
func adminTarget(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("the running server recorded an address gummi cannot read (%q)", addr)
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil, ip.IsLoopback():
		return addr, nil
	case ip.IsUnspecified():
		return net.JoinHostPort("127.0.0.1", port), nil
	default:
		return "", fmt.Errorf("the running server listens only on %s, and `gummi web pair` talks to it over loopback, "+
			"which that address is not — read the pairing code from the terminal running `gummi web` "+
			"(it prints one at start, and a browser can ask for another), or serve on 0.0.0.0 or 127.0.0.1", addr)
	}
}

// adminClient is the client `gummi web pair` posts with. Over TLS it does
// not check the certificate: it only ever dials loopback (adminTarget), a
// --tls-cert certificate rarely names 127.0.0.1, and the bearer token it
// sends is readable only by whoever could read server.json anyway.
func adminClient(tlsOn bool) *http.Client {
	if !tlsOn {
		return http.DefaultClient
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback only; see above
	return &http.Client{Transport: tr}
}

// runWebDevices implements `gummi web devices`, reading the store directly
// so it works whether or not a server is up.
func runWebDevices(fl cliFlags) error {
	return withWebWorkspace(func(ws state.Workspace) error {
		devices, err := web.OpenDevices(filepath.Join(ws.WebDir(), devicesFile), nil)
		if err != nil {
			return err
		}
		list := devices.List()
		if fl.Bool("json") {
			if list == nil {
				list = []web.Device{}
			}
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		if len(list) == 0 {
			fmt.Println("no paired devices — `gummi web` prints a code for the first one")
			return nil
		}
		waiting := false
		for _, d := range list {
			if d.Status == web.StatusPending {
				waiting = true
			}
			fmt.Printf("%s  %-14s %-18s paired %s%s, %s%s\n",
				d.ID, d.Person, d.Name, d.PairedAt.Format("2006-01-02"), deviceVia(d), deviceStanding(d), deviceAt(d))
		}
		if waiting {
			fmt.Println("a waiting device is let in or turned away from the page on a paired device; `gummi web unpair <id>` withdraws it")
		}
		return nil
	})
}

// runWebUnpair implements `gummi web unpair <id>|--all`. It edits the store
// rather than talking to the server: the file is the source of truth, and a
// running server re-reads it before it trusts a cookie again.
func runWebUnpair(fl cliFlags, args []string) error {
	all := fl.Bool("all")
	switch {
	case all && len(args) > 0:
		return errors.New("--all unpairs everything; it takes no device id")
	case !all && len(args) != 1:
		return errors.New("name one device id (from `gummi web devices`), or pass --all")
	}
	return withWebWorkspace(func(ws state.Workspace) error {
		devices, err := web.OpenDevices(filepath.Join(ws.WebDir(), devicesFile), nil)
		if err != nil {
			return err
		}
		if all {
			var ids []string
			for _, d := range devices.List() {
				ids = append(ids, d.ID)
			}
			n, err := devices.ForgetAll()
			if err != nil {
				return err
			}
			dropPushSubscriptions(ws, ids...)
			fmt.Printf("unpaired %d device%s\n", n, cardPlural(n))
			return nil
		}
		dev, err := devices.Forget(args[0])
		if err != nil {
			return err
		}
		dropPushSubscriptions(ws, dev.ID)
		fmt.Printf("unpaired %s on %s (%s)\n", dev.Person, dev.Name, dev.ID)
		return nil
	})
}

// dropPushSubscriptions removes unpaired devices' notification
// subscriptions, so a revoked phone stops hearing about the board. Like the
// devices file it is a file edit a running server re-reads; a failure is
// reported, not fatal — the device is already unpaired, and the server
// drops a subscription whose device it no longer knows before sending.
// Their open event streams close within a second (the server checks each
// stream's device every second, and before every event).
func dropPushSubscriptions(ws state.Workspace, ids ...string) {
	store, err := push.OpenStore(filepath.Join(ws.WebDir(), web.PushFile), nil)
	if err == nil {
		err = store.RemoveDevices(ids...)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gummi: dropping the notification subscriptions: %v\n", err)
	}
}

// withWebWorkspace resolves the workspace without creating one: reading or
// revoking pairings is not a reason to scaffold a board.
func withWebWorkspace(fn func(state.Workspace) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wsRoot, defaultRoot, _, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := state.Open(wsRoot, defaultRoot)
	if err != nil {
		return err
	}
	return fn(ws)
}

// deviceVia says how a device paired, when the store knows.
func deviceVia(d web.Device) string {
	if d.Via == "" {
		return ""
	}
	return " " + web.CodeOrigin(d.Via).Via()
}

// deviceStanding says where a device stands: when it was last seen, or
// that it waits to be let in, or how its wait ended.
func deviceStanding(d web.Device) string {
	from := ""
	if d.Source != "" {
		from = " from " + d.Source
	}
	by := ""
	if d.DecidedBy != "" {
		by = " by " + d.DecidedBy
	}
	switch d.Status {
	case web.StatusPending:
		return "WAITING to be let in" + from + " (asked " + humanSince(d.PairedAt) + ")"
	case web.StatusRejected:
		return "turned away" + by + " " + humanSince(d.DecidedAt)
	case web.StatusExpired:
		return "not let in: nobody answered" + from
	case web.StatusApproved:
		if by != "" {
			return "let in" + by + ", last seen " + humanSince(d.LastSeen)
		}
	}
	return "last seen " + humanSince(d.LastSeen)
}

// deviceAt names where a device's token is honoured, when it is bound.
func deviceAt(d web.Device) string {
	if d.Origin == "" {
		return ""
	}
	return " (at " + d.Origin + ")"
}

// noPairingHosts refuses --no-pairing beside an --allow-host that is not
// loopback.
func noPairingHosts(allow string) error {
	for _, name := range strings.Split(allow, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		host := name
		if h, _, err := net.SplitHostPort(name); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--no-pairing serves the board to anyone who reaches it, and --allow-host %s is a name something in front "+
				"of this board forwards (a proxy, `tailscale serve`) — drop --no-pairing and pair the browser, or drop --allow-host", name)
		}
	}
	return nil
}

// errNoPairingOffLoopback refuses --no-pairing on a listener other
// machines can reach.
var errNoPairingOffLoopback = errors.New("--no-pairing serves the board to anyone who can reach it, so it is only allowed " +
	"on a loopback address — listen on 127.0.0.1, or pair the browser instead")

// loopbackAddr reports whether addr cannot be anything but loopback: a
// loopback address, or localhost. Anything else (a name, every interface)
// is decided by the listener it gives (loopbackOnly); this only refuses
// early what is refused anyway.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	switch {
	case err != nil, host == "":
		return false
	case host == "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip == nil || ip.IsLoopback()
}

// webHosts are the names `gummi web` answers to beyond its address and
// loopback: the --addr host when it is a name, every name on the --tls-cert
// certificate, and --allow-host's (a reverse proxy in front, such as
// `tailscale serve`, which forwards its own name).
func webHosts(addr, certFile, keyFile, allow string) ([]string, error) {
	var hosts []string
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && net.ParseIP(host) == nil {
		hosts = append(hosts, host)
	}
	if certFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading --tls-cert/--tls-key: %w", err)
		}
		leaf := pair.Leaf
		if leaf == nil && len(pair.Certificate) > 0 {
			if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
				return nil, fmt.Errorf("reading --tls-cert: %w", err)
			}
		}
		if leaf != nil {
			hosts = append(hosts, leaf.DNSNames...)
			for _, ip := range leaf.IPAddresses {
				hosts = append(hosts, ip.String())
			}
		}
	}
	for _, name := range strings.Split(allow, ",") {
		if name = strings.TrimSpace(name); name != "" {
			hosts = append(hosts, name)
		}
	}
	return hosts, nil
}

// loopbackOnly reports whether the listener is on this machine only.
func loopbackOnly(ln net.Listener) bool {
	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
