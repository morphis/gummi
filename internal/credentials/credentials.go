// Package credentials holds the two secrets a person may hand gummi from
// the web page's settings instead of setting them up on the machine: a
// GitHub token and an SSH private key (DESIGN §22.2).
//
// Neither is ever exported wholesale. The token is set as GH_TOKEN on the
// gh commands gummi itself runs; the key is never written anywhere a
// command reads it — gummi answers as an ssh-agent, on a private socket,
// for exactly as long as one git command that reaches the remote runs.
// No agent backend's environment carries either. A workspace with nothing
// stored changes nothing: gh and git authenticate as the machine already
// does.
//
// Both live under the workspace's state directory, 0600, beside the web
// face's device tokens: readable by whoever runs gummi, which includes an
// unconfined agent on the same account, exactly as ~/.ssh is.
package credentials

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/morphis/gummi/internal/atomicfile"
)

const (
	tokenFile = "github-token"
	keyFile   = "ssh-key"
	// maxToken and maxKey bound what a person may store; a GitHub token is
	// under 300 characters and an RSA-8192 key under 8 KiB.
	maxToken = 512
	maxKey   = 16 << 10
)

// Store is the directory the secrets are kept in. The zero Store holds
// nothing and refuses writes.
type Store struct{ Dir string }

// Status is what may be said about the stored secrets without saying
// them.
type Status struct {
	// TokenSet reports a stored token; TokenHint is its last four
	// characters, enough to tell two apart.
	TokenSet  bool
	TokenHint string
	// KeySet reports a stored key; KeyType and KeyFingerprint name it as
	// `ssh-keygen -l` would, and KeyPublic is its authorized_keys line.
	KeySet         bool
	KeyType        string
	KeyFingerprint string
	KeyPublic      string
}

func (s Store) read(name string) string {
	if s.Dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s Store) write(name, body string) error {
	if s.Dir == "" {
		return errors.New("this workspace has nowhere to keep credentials")
	}
	path := filepath.Join(s.Dir, name)
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(body+"\n"), 0o600)
}

// Token is the stored GitHub token, "" for none.
func (s Store) Token() string { return s.read(tokenFile) }

// SetToken stores tok as the GitHub token; an empty tok forgets it.
func (s Store) SetToken(tok string) error {
	tok = strings.TrimSpace(tok)
	if len(tok) > maxToken {
		return errors.New("that is too long to be a GitHub token")
	}
	if strings.ContainsFunc(tok, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return errors.New("a GitHub token is one word of printable characters")
	}
	return s.write(tokenFile, tok)
}

// SetSSHKey stores body, a private key in OpenSSH or PEM form, as the key
// gummi answers with; an empty body forgets it. A passphrase-protected key
// is refused: there is nobody to ask for the passphrase when a push runs.
func (s Store) SetSSHKey(body string) error {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return s.write(keyFile, "")
	}
	if len(body) > maxKey {
		return errors.New("that is too long to be an SSH private key")
	}
	if _, err := ssh.ParseRawPrivateKey([]byte(body + "\n")); err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return errors.New("that key is protected by a passphrase; store one without (ssh-keygen -p -N '' -f <copy>), or keep it in your own ssh-agent")
		}
		return errors.New("that is not an SSH private key gummi can read (paste the whole file, from -----BEGIN to -----END)")
	}
	return s.write(keyFile, body)
}

// GenerateSSHKey makes a fresh ed25519 key on this host and stores it in
// place of any key held. The private half never leaves the machine: what
// a person takes away is Status's public line, to add to GitHub.
func (s Store) GenerateSSHKey() error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "gummi")
	if err != nil {
		return err
	}
	return s.write(keyFile, strings.TrimSpace(string(pem.EncodeToMemory(block))))
}

// key is the stored private key, parsed; nil for none or one that no
// longer parses.
func (s Store) key() any {
	body := s.read(keyFile)
	if body == "" {
		return nil
	}
	k, err := ssh.ParseRawPrivateKey([]byte(body + "\n"))
	if err != nil {
		return nil
	}
	return k
}

// Status describes what is stored.
func (s Store) Status() Status {
	var st Status
	if tok := s.Token(); tok != "" {
		st.TokenSet = true
		if len(tok) > 8 {
			st.TokenHint = tok[len(tok)-4:]
		}
	}
	if k := s.key(); k != nil {
		if signer, err := ssh.NewSignerFromKey(k); err == nil {
			pub := signer.PublicKey()
			st.KeySet = true
			st.KeyType = pub.Type()
			st.KeyFingerprint = ssh.FingerprintSHA256(pub)
			st.KeyPublic = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " gummi"
		}
	}
	return st
}

// Identity names what is stored without revealing it: a digest that
// changes when either secret does. Publishing folds it into the facts a
// person confirms, so a credential swapped between the confirm and the
// act refuses the act.
func (s Store) Identity() string {
	st := s.Status()
	if !st.TokenSet && !st.KeySet {
		return ""
	}
	sum := sha256.Sum256([]byte(s.Token()))
	return "token=" + hex.EncodeToString(sum[:8]) + " key=" + st.KeyFingerprint
}

var (
	mu      sync.RWMutex
	current Store
)

// Use names the store this process reads; the commands call it once the
// workspace is known. Until then nothing is stored.
func Use(s Store) {
	mu.Lock()
	defer mu.Unlock()
	current = s
}

// Current is the store Use named.
func Current() Store {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// WithToken is env for a gh command: env itself when no token is stored,
// else env with GH_TOKEN set to it, in place of any token the environment
// already carried. A nil env is the process's own.
func WithToken(env []string) []string {
	tok := Current().Token()
	if tok == "" {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != "GH_TOKEN" && k != "GITHUB_TOKEN" {
			out = append(out, kv)
		}
	}
	return append(out, "GH_TOKEN="+tok)
}

// WithAgent is env for a git command that reaches the remote. With a key
// stored it starts an ssh-agent holding that one key on a socket only
// this user can open and points SSH_AUTH_SOCK at it; stop ends the agent
// and removes the socket, and must be called when the command is done.
// With no key, or where the agent cannot be started, env comes back as
// given and the machine's own agent, if any, is the one ssh finds.
func WithAgent(env []string) (out []string, stop func()) {
	key := Current().key()
	if key == nil {
		return env, func() {}
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		return env, func() {}
	}
	dir, err := os.MkdirTemp("", "gummi-agent-")
	if err != nil {
		return env, func() {}
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return env, func() {}
	}
	_ = os.Chmod(sock, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = agent.ServeAgent(signOnly{ring}, c)
				_ = c.Close()
			}()
		}
	}()
	if env == nil {
		env = os.Environ()
	}
	out = make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != "SSH_AUTH_SOCK" {
			out = append(out, kv)
		}
	}
	return append(out, "SSH_AUTH_SOCK="+sock), func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	}
}

// signOnly is the agent a command is offered: it lists the key and signs
// with it, and refuses everything that would change what it holds.
type signOnly struct{ agent.Agent }

var errReadOnly = errors.New("gummi's ssh-agent only signs")

func (signOnly) Add(agent.AddedKey) error   { return errReadOnly }
func (signOnly) Remove(ssh.PublicKey) error { return errReadOnly }
func (signOnly) RemoveAll() error           { return errReadOnly }
func (signOnly) Lock([]byte) error          { return errReadOnly }
func (signOnly) Unlock([]byte) error        { return errReadOnly }
