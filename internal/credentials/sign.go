package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Signing commits with the stored key (DESIGN §22.2). git signs through a
// program it is told to run as `ssh-keygen -Y sign`; gummi names itself as
// that program, so the key is read by gummi and by nothing else: no
// ssh-keygen opens it and no agent holds it. The configuration that says
// so is git's own, carried in the environment of this process and
// therefore of every process it starts — its own git commands, an agent
// backend, a person's terminal — and written to no repository. It names a
// program and the public key file, never a secret.

const (
	signFile = "sign-commits"
	pubFile  = "ssh-key.pub"
	// sigNamespace is the one namespace git signs in; the signer refuses
	// any other, so what it puts the key's name to is always a git object.
	sigNamespace = "git"
)

// SetSigning switches signing commits with the stored key on or off. It
// may only be switched on with a key held.
func (s Store) SetSigning(on bool) error {
	if !on {
		return s.write(signFile, "")
	}
	if s.key() == nil {
		return errors.New("store or generate an SSH key first: there is nothing to sign commits with")
	}
	if err := s.syncPublic(); err != nil {
		return err
	}
	return s.write(signFile, "on")
}

// Signing reports whether commits are signed with the stored key: the
// switch is on and there is a key to sign with.
func (s Store) Signing() bool { return s.read(signFile) != "" && s.key() != nil }

// syncPublic keeps the public half beside the key, for git to name as the
// key it signs with; with no key it removes the file and the switch.
func (s Store) syncPublic() error {
	k := s.key()
	if k == nil {
		if err := s.write(signFile, ""); err != nil {
			return err
		}
		return s.write(pubFile, "")
	}
	signer, err := ssh.NewSignerFromKey(k)
	if err != nil {
		return err
	}
	return s.write(pubFile, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))+" gummi")
}

var (
	signer string
	// signEnv is what applySigning last did to the process environment:
	// the entries it added, the index they start at and whether
	// GIT_CONFIG_COUNT was set before them.
	signEnv struct {
		cfg      [][2]string
		base     int
		hadCount bool
	}
)

// UseSigner names the program git is told to sign with: gummi's own
// binary, which main answers as (RunSigner) when git calls it. Until it is
// named nothing is signed, whatever the store says.
func UseSigner(path string) {
	mu.Lock()
	defer mu.Unlock()
	signer = path
	applySigning()
}

// signConfig is the git configuration that signs every commit with the
// key in s through program.
func signConfig(s Store, program string) [][2]string {
	return [][2]string{
		{"gpg.format", "ssh"},
		{"gpg.ssh.program", program},
		{"user.signingkey", filepath.Join(s.Dir, pubFile)},
		{"commit.gpgsign", "true"},
	}
}

// applySigning makes the process environment agree with the current
// store: git's GIT_CONFIG_COUNT/KEY/VALUE entries for signing while it is
// on, after any the environment already carried, and none of gummi's when
// it is off. mu must be held.
//
// A git command may be started at any moment, so the environment is never
// left counting entries that are not there: the count is lowered before
// entries are removed and raised after they are set, and a call that
// changes nothing touches nothing.
func applySigning() {
	var want [][2]string
	if signer != "" && current.Signing() {
		want = signConfig(current, signer)
	}
	if slices.Equal(want, signEnv.cfg) {
		return
	}
	if signEnv.cfg != nil {
		if signEnv.hadCount {
			_ = os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(signEnv.base))
		} else {
			_ = os.Unsetenv("GIT_CONFIG_COUNT")
		}
		for i := range signEnv.cfg {
			n := strconv.Itoa(signEnv.base + i)
			_ = os.Unsetenv("GIT_CONFIG_KEY_" + n)
			_ = os.Unsetenv("GIT_CONFIG_VALUE_" + n)
		}
		signEnv.cfg = nil
	}
	if want == nil {
		return
	}
	raw, had := os.LookupEnv("GIT_CONFIG_COUNT")
	base := 0
	if had {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 0 {
			// git refuses this environment as it stands; adding to it
			// would only overwrite entries that are not gummi's
			return
		}
		base = n
	}
	for i, kv := range want {
		n := strconv.Itoa(base + i)
		_ = os.Setenv("GIT_CONFIG_KEY_"+n, kv[0])
		_ = os.Setenv("GIT_CONFIG_VALUE_"+n, kv[1])
	}
	_ = os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(base+len(want)))
	signEnv.cfg, signEnv.base, signEnv.hadCount = want, base, had
}

// IsSignerCall reports whether args are git calling its signing program,
// which it does as `<program> -Y <verb> …`.
func IsSignerCall(args []string) bool { return len(args) > 1 && args[0] == "-Y" }

// RunSigner answers args as git's gpg.ssh.program and returns the exit
// status. `-Y sign` is signed here with the key stored beside the file -f
// names; every other verb (verifying, finding principals) needs no key
// and is ssh-keygen's.
func RunSigner(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var verb, namespace, keyPath, file string
	for rest := args; len(rest) > 0; rest = rest[1:] {
		a := rest[0]
		switch a {
		case "-Y", "-n", "-f", "-O":
			if len(rest) < 2 {
				fmt.Fprintf(stderr, "gummi: %s needs a value\n", a)
				return 2
			}
			rest = rest[1:]
			switch a {
			case "-Y":
				verb = rest[0]
			case "-n":
				namespace = rest[0]
			case "-f":
				keyPath = rest[0]
			}
		case "-U":
		default:
			file = a
		}
	}
	if verb != "sign" {
		return execSSHKeygen(args, stdin, stdout, stderr)
	}
	if namespace != sigNamespace {
		fmt.Fprintf(stderr, "gummi: the stored key signs git objects only, not %q\n", namespace)
		return 1
	}
	// the switch is read here too: a session started while it was on
	// still carries the instruction to sign, and off has to mean off
	store := Store{Dir: filepath.Dir(keyPath)}
	if keyPath == "" || !store.Signing() {
		fmt.Fprintln(stderr, "gummi: signing commits with the stored SSH key is switched off (or the key is gone); switch it on in settings, or start this session again to commit unsigned")
		return 1
	}
	key := store.key()
	var msg []byte
	var err error
	if file == "" {
		msg, err = io.ReadAll(stdin)
	} else {
		msg, err = os.ReadFile(file) //nolint:gosec // the buffer git asks its signing program to sign
	}
	if err != nil {
		fmt.Fprintln(stderr, "gummi:", err)
		return 1
	}
	sig, err := signMessage(key, msg)
	if err != nil {
		fmt.Fprintln(stderr, "gummi:", err)
		return 1
	}
	if file == "" {
		_, err = stdout.Write(sig)
	} else {
		err = os.WriteFile(file+".sig", sig, 0o600) //nolint:gosec // where git reads the signature back from
	}
	if err != nil {
		fmt.Fprintln(stderr, "gummi:", err)
		return 1
	}
	return 0
}

// execSSHKeygen hands a call that is not a signature to ssh-keygen.
func execSSHKeygen(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	path, err := exec.LookPath("ssh-keygen")
	if err != nil {
		fmt.Fprintln(stderr, "gummi: checking a signature needs ssh-keygen, which is not on PATH")
		return 1
	}
	cmd := exec.CommandContext(context.Background(), path, args...) //nolint:gosec // ssh-keygen from PATH, with the arguments git gave its signing program
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "gummi:", err)
		return 1
	}
	return 0
}

// signMessage is an armored OpenSSH signature (PROTOCOL.sshsig) of msg in
// git's namespace, as `ssh-keygen -Y sign -n git` writes one.
func signMessage(key any, msg []byte) ([]byte, error) {
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	const magic, hashAlg = "SSHSIG", "sha512"
	digest := sha512.Sum512(msg)
	signed := append([]byte(magic), ssh.Marshal(struct {
		Namespace, Reserved, HashAlgorithm string
		Hash                               []byte
	}{sigNamespace, "", hashAlg, digest[:]})...)
	var sig *ssh.Signature
	// an RSA key's default signature is SHA-1, which no verifier accepts
	if as, ok := s.(ssh.AlgorithmSigner); ok && s.PublicKey().Type() == ssh.KeyAlgoRSA {
		sig, err = as.SignWithAlgorithm(rand.Reader, signed, ssh.KeyAlgoRSASHA512)
	} else {
		sig, err = s.Sign(rand.Reader, signed)
	}
	if err != nil {
		return nil, err
	}
	blob := append([]byte(magic), ssh.Marshal(struct {
		Version                            uint32
		PublicKey                          []byte
		Namespace, Reserved, HashAlgorithm string
		Signature                          []byte
	}{1, s.PublicKey().Marshal(), sigNamespace, "", hashAlg, ssh.Marshal(sig)})...)
	// pem wraps at 64 columns where ssh-keygen wraps at 70; both read either
	armored := pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: blob})
	return bytes.TrimLeft(armored, "\n"), nil
}
