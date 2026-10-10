package credentials

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestMain lets the test binary stand in for gummi as git's signing
// program, the way cmd/gummi's main does.
func TestMain(m *testing.M) {
	if IsSignerCall(os.Args[1:]) {
		os.Exit(RunSigner(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// signingRepo is a throwaway repository with an identity and nothing of
// the machine's git configuration, and the allowed-signers file naming
// the store's key.
func signingRepo(t *testing.T, s Store) (repo, allowed string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo = t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Ada Lovelace")
	git(t, repo, "config", "user.email", "ada@example.com")
	allowed = filepath.Join(t.TempDir(), "allowed")
	line := "ada@example.com " + strings.TrimSuffix(s.Status().KeyPublic, " gummi") + "\n"
	if err := os.WriteFile(allowed, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, allowed
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func useSigner(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	UseSigner(exe)
	t.Cleanup(func() { UseSigner("") })
}

func TestCommitsAreSignedWithTheStoredKeyWhileTheSwitchIsOn(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	if err := s.SetSSHKey(testKey(t, "")); err != nil {
		t.Fatal(err)
	}
	repo, allowed := signingRepo(t, s)
	// what the environment already configured stays, ahead of gummi's
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
	t.Setenv("GIT_CONFIG_VALUE_0", "12")
	useSigner(t)
	use(t, s)

	git(t, repo, "commit", "-q", "--allow-empty", "-m", "before the switch")
	if raw := git(t, repo, "cat-file", "commit", "HEAD"); strings.Contains(raw, "gpgsig") {
		t.Fatalf("a commit is signed with the switch off:\n%s", raw)
	}

	if err := s.SetSigning(true); err != nil {
		t.Fatal(err)
	}
	Use(s)
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "5" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want the environment's one entry and gummi's four", got)
	}
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "signed")
	out := git(t, repo, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", "HEAD")
	if !strings.Contains(out, `Good "git" signature for ada@example.com`) {
		t.Fatalf("verify-commit = %q", out)
	}
	if got := strings.TrimSpace(git(t, repo, "rev-parse", "--short", "HEAD")); len(got) != 12 {
		t.Fatalf("the environment's own git configuration was lost: short sha %q", got)
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, "PRIVATE KEY") {
			t.Fatalf("the environment carries the key: %.40s", kv)
		}
	}

	if err := s.SetSigning(false); err != nil {
		t.Fatal(err)
	}
	Use(s)
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "1" {
		t.Fatalf("GIT_CONFIG_COUNT = %q after switching off, want the environment's own 1", got)
	}
	if _, set := os.LookupEnv("GIT_CONFIG_KEY_1"); set {
		t.Fatal("gummi's signing configuration outlived the switch")
	}
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "after the switch")
	if raw := git(t, repo, "cat-file", "commit", "HEAD"); strings.Contains(raw, "gpgsig") {
		t.Fatalf("a commit is signed after the switch went off:\n%s", raw)
	}
}

func TestSigningNeedsAKeyAndIsForgottenWithIt(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	if err := s.SetSigning(true); err == nil || !strings.Contains(err.Error(), "SSH key first") {
		t.Fatalf("switching on with no key = %v", err)
	}
	if err := s.SetSSHKey(testKey(t, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSigning(true); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !s.Signing() || !st.Signing {
		t.Fatalf("signing = %v, status %+v", s.Signing(), st)
	}
	// a replaced key is the one named to git from then on
	if err := s.GenerateSSHKey(); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(filepath.Join(s.Dir, pubFile))
	if err != nil || strings.TrimSpace(string(pub)) != s.Status().KeyPublic || !s.Signing() {
		t.Fatalf("public half = %q, %v; want the new key's, still signing", pub, err)
	}
	if err := s.SetSSHKey(""); err != nil {
		t.Fatal(err)
	}
	if s.Signing() {
		t.Fatal("still signing with the key forgotten")
	}
	if err := s.SetSSHKey(testKey(t, "")); err != nil {
		t.Fatal(err)
	}
	if s.Signing() {
		t.Fatal("a key stored after one was forgotten signs without being asked to")
	}
}

func TestTheSignerSignsGitObjectsOnlyAndAnRSAKeyVerifies(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	if err := s.SetSSHKey(string(pem.EncodeToMemory(block))); err != nil {
		t.Fatal(err)
	}
	_, allowed := signingRepo(t, s)
	pub := filepath.Join(s.Dir, pubFile)
	msg := filepath.Join(t.TempDir(), "buffer")
	if err := os.WriteFile(msg, []byte("tree 4b825dc\n\nsigned\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if code := RunSigner([]string{"-Y", "sign", "-n", "file", "-f", pub, msg}, nil, nil, &stderr); code == 0 || !strings.Contains(stderr.String(), "git objects only") {
		t.Fatalf("signing outside git's namespace = %d, %q", code, stderr.String())
	}
	if code := RunSigner([]string{"-Y", "sign", "-n", "git", "-f", filepath.Join(t.TempDir(), pubFile), msg}, nil, nil, &stderr); code == 0 {
		t.Fatal("signed with no key stored")
	}
	if code := RunSigner([]string{"-Y", "sign", "-n", "git", "-f", pub, "-U", msg}, nil, nil, &stderr); code != 0 {
		t.Fatalf("sign = %d, %q", code, stderr.String())
	}
	in, err := os.Open(msg)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	verify := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "ada@example.com", "-n", "git", "-s", msg+".sig")
	verify.Stdin = in
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen does not accept the signature: %v\n%s", err, out)
	}
}
