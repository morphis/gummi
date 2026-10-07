package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Urgency is RFC 8030 §5.3's hint to the push service about how soon to
// wake the device.
type Urgency string

const (
	UrgencyVeryLow Urgency = "very-low"
	UrgencyLow     Urgency = "low"
	UrgencyNormal  Urgency = "normal"
	UrgencyHigh    Urgency = "high"
)

// Options are the per-message delivery headers (RFC 8030 §5).
type Options struct {
	// TTL is how long the push service keeps an undelivered message.
	// Zero means deliver now or drop it; negative is treated as zero.
	TTL time.Duration
	// Urgency is left off the request when empty (the service's default
	// is normal).
	Urgency Urgency
	// Topic, when set, makes a newer message replace an undelivered one
	// with the same topic. At most 32 characters of the base64url
	// alphabet; see TopicFor.
	Topic string
}

// ErrGone is returned by Send when the push service reports the
// subscription no longer exists (404 or 410): the caller should forget it.
var ErrGone = errors.New("push: subscription is gone")

// StatusError is a push service response that was neither success nor
// gone.
type StatusError struct {
	Code int
	Body string // the first few hundred bytes, for a log line
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("push: push service answered %d", e.Code)
	}
	return fmt.Sprintf("push: push service answered %d: %s", e.Code, e.Body)
}

// DefaultTimeout bounds one Send when the Sender sets none.
const DefaultTimeout = 10 * time.Second

// Sender POSTs encrypted messages to push services, signed with the
// host's VAPID key.
type Sender struct {
	vapid   *VAPID
	subject string

	// Client is the HTTP client used; nil means a client that never
	// follows redirects and dials only public addresses (addr.go). Tests
	// point it at an httptest TLS server.
	Client *http.Client
	// Timeout bounds one Send; zero means DefaultTimeout.
	Timeout time.Duration
	// AllowPrivate lets the default client dial loopback and private
	// addresses. It exists for a test's stand-in push service and is
	// never on for a real board (GUMMI_WEB_PUSH_ALLOW_PRIVATE).
	AllowPrivate bool

	now func() time.Time
}

// NewSender builds a Sender signing with v and naming subject (a mailto:
// or https: contact; empty means DefaultSubject) in its JWTs.
func NewSender(v *VAPID, subject string) *Sender {
	if subject == "" {
		subject = DefaultSubject
	}
	return &Sender{vapid: v, subject: subject, now: time.Now}
}

// VAPID is the key the sender signs with.
func (s *Sender) VAPID() *VAPID { return s.vapid }

// newClient is the client a Sender uses when given none. A push endpoint
// that redirects is not one to follow: the URL came from a browser, and
// the host should POST nowhere else. It dials directly, never through a
// proxy from the environment, so the address it checks is the address it
// connects to.
func newClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = dialControl
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = dialer.DialContext
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var (
	publicClient  = newClient(false)
	privateClient = newClient(true)
)

// Send encrypts payload for sub and POSTs it to sub's endpoint. It returns
// ErrGone for a subscription the push service no longer knows, a
// *StatusError for any other non-2xx answer, and the transport's error
// (including the timeout) otherwise.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	if err := validTopic(opts.Topic); err != nil {
		return err
	}
	body, err := Encrypt(sub.Keys, payload)
	if err != nil {
		return err
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	auth, err := s.vapid.Authorization(sub.Endpoint, s.subject, now())
	if err != nil {
		return err
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	ttl := max(opts.TTL, 0)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	if opts.Urgency != "" {
		req.Header.Set("Urgency", string(opts.Urgency))
	}
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}

	client := s.Client
	switch {
	case client != nil:
	case s.AllowPrivate:
		client = privateClient
	default:
		client = publicClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}
}

func validTopic(t string) error {
	if len(t) > 32 {
		return fmt.Errorf("push: topic %q is longer than 32 characters", t)
	}
	for _, c := range t {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return fmt.Errorf("push: topic %q has a character outside the base64url alphabet", t)
		}
	}
	return nil
}
