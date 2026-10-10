package credentials

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// testKey is a fresh ed25519 private key in OpenSSH form, protected by
// passphrase when one is given.
func testKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func use(t *testing.T, s Store) {
	t.Helper()
	Use(s)
	t.Cleanup(func() { Use(Store{}) })
}

func TestTokenIsStoredPrivatelyAndNamedNotShown(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	if st := s.Status(); st.TokenSet || st.KeySet {
		t.Fatalf("an empty store = %+v", st)
	}
	if err := s.SetToken("  ghp_abcdefghijklmnop1234\n"); err != nil {
		t.Fatal(err)
	}
	if got := s.Token(); got != "ghp_abcdefghijklmnop1234" {
		t.Fatalf("token = %q", got)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, tokenFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file = %v, %v; want 0600", fi, err)
	}
	if di, _ := os.Stat(s.Dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("store dir = %v, want 0700", di.Mode().Perm())
	}
	if st := s.Status(); !st.TokenSet || st.TokenHint != "1234" {
		t.Fatalf("status = %+v", st)
	}
	for _, bad := range []string{"two words", "line\nbreak", strings.Repeat("x", maxToken+1)} {
		if err := s.SetToken(bad); err == nil {
			t.Errorf("SetToken(%q) was accepted", bad)
		}
	}
	if err := s.SetToken(""); err != nil {
		t.Fatal(err)
	}
	if s.Token() != "" || s.Status().TokenSet {
		t.Fatal("a forgotten token is still stored")
	}
	if err := (Store{}).SetToken("ghp_x"); err == nil {
		t.Fatal("a store with no directory accepted a token")
	}
}

func TestSSHKeyIsValidatedAndNamedByItsPublicHalf(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.SetSSHKey("not a key"); err == nil {
		t.Fatal("garbage was accepted as a key")
	}
	if err := s.SetSSHKey(testKey(t, "hunter2")); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("a passphrase-protected key = %v, want a refusal that says why", err)
	}
	if s.Status().KeySet {
		t.Fatal("a refused key was stored")
	}
	key := testKey(t, "")
	if err := s.SetSSHKey(strings.ReplaceAll(key, "\n", "\r\n")); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.KeySet || st.KeyType != "ssh-ed25519" || !strings.HasPrefix(st.KeyFingerprint, "SHA256:") || !strings.HasPrefix(st.KeyPublic, "ssh-ed25519 ") {
		t.Fatalf("status = %+v", st)
	}
	if strings.Contains(st.KeyPublic, "PRIVATE") {
		t.Fatal("the status carries the private key")
	}
	before := s.Identity()
	if err := s.SetSSHKey(testKey(t, "")); err != nil {
		t.Fatal(err)
	}
	if s.Identity() == before || before == "" {
		t.Fatal("the identity did not follow the key")
	}
	if err := s.SetSSHKey(""); err != nil {
		t.Fatal(err)
	}
	if s.Status().KeySet || s.Identity() != "" {
		t.Fatal("a forgotten key is still stored")
	}
}

func TestWithTokenReplacesTheEnvironmentsOwn(t *testing.T) {
	env := []string{"PATH=/bin", "GH_TOKEN=theirs", "GITHUB_TOKEN=theirs-too"}
	if got := WithToken(env); !slices.Equal(got, env) {
		t.Fatalf("with nothing stored = %v", got)
	}
	if WithToken(nil) != nil {
		t.Fatal("with nothing stored a nil env must stay nil, so the command inherits")
	}
	s := Store{Dir: t.TempDir()}
	use(t, s)
	if err := s.SetToken("ghp_mine"); err != nil {
		t.Fatal(err)
	}
	if got := WithToken(env); !slices.Equal(got, []string{"PATH=/bin", "GH_TOKEN=ghp_mine"}) {
		t.Fatalf("env = %v", got)
	}
	if got := WithToken(nil); !slices.Contains(got, "GH_TOKEN=ghp_mine") {
		t.Fatalf("a nil env = %v, want the process's own plus the token", got)
	}
}

func TestWithAgentSignsWithTheStoredKeyOnlyWhileItRuns(t *testing.T) {
	env := []string{"SSH_AUTH_SOCK=/their/agent"}
	got, stop := WithAgent(env)
	stop()
	if !slices.Equal(got, env) {
		t.Fatalf("with no key stored = %v", got)
	}

	s := Store{Dir: t.TempDir()}
	use(t, s)
	if err := s.SetSSHKey(testKey(t, "")); err != nil {
		t.Fatal(err)
	}
	got, stop = WithAgent(env)
	if len(got) != 1 || !strings.HasPrefix(got[0], "SSH_AUTH_SOCK=") || got[0] == env[0] {
		t.Fatalf("env = %v", got)
	}
	sock := strings.TrimPrefix(got[0], "SSH_AUTH_SOCK=")
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket = %v, %v; want 0600", fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(sock)); di.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir = %v, want 0700", di.Mode().Perm())
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agent.NewClient(conn)
	keys, err := client.List()
	if err != nil || len(keys) != 1 || ssh.FingerprintSHA256(keys[0]) != s.Status().KeyFingerprint {
		t.Fatalf("listed %v, %v; want the stored key", keys, err)
	}
	sig, err := client.Sign(keys[0], []byte("challenge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys[0].Verify([]byte("challenge"), sig); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
	// the same answer to OpenSSH's own client, which is what git runs
	if sshAdd, lerr := exec.LookPath("ssh-add"); lerr == nil {
		cmd := exec.CommandContext(context.Background(), sshAdd, "-l")
		cmd.Env = got
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), s.Status().KeyFingerprint) {
			t.Fatalf("ssh-add -l = %q, %v; want the stored key's fingerprint", out, err)
		}
	}
	if err := client.RemoveAll(); err == nil {
		t.Fatal("the agent let a command empty it")
	}
	if err := client.Add(agent.AddedKey{PrivateKey: Store{Dir: s.Dir}.key()}); err == nil {
		t.Fatal("the agent let a command add a key")
	}

	stop()
	if _, err := os.Stat(filepath.Dir(sock)); !os.IsNotExist(err) {
		t.Fatalf("the socket outlived the command: %v", err)
	}
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		t.Fatal("the agent still answers after stop")
	}
}
