package worktree

import (
	"context"
	"errors"
	"strings"
)

// maxIdentity bounds a name or an email a person may set.
const maxIdentity = 200

// Identity is who git would write a commit as in the repository at root:
// user.name and user.email as git resolves them there, from any scope.
// Either is "" where nothing sets it.
func Identity(ctx context.Context, root string) (name, email string) {
	name, _ = runGit(ctx, root, "config", "--get", "user.name")
	email, _ = runGit(ctx, root, "config", "--get", "user.email")
	return strings.TrimSpace(name), strings.TrimSpace(email)
}

// CheckIdentity refuses a name and email git could not write a commit
// with. Both empty is allowed: it means "whatever the machine says".
func CheckIdentity(name, email string) error {
	if name == "" && email == "" {
		return nil
	}
	if name == "" || email == "" {
		return errors.New("a git identity is a name and an email; give both, or neither to use the machine's own")
	}
	if len(name) > maxIdentity || len(email) > maxIdentity {
		return errors.New("that name or email is too long")
	}
	bad := func(r rune) bool { return r < ' ' || r == '<' || r == '>' || r == 0x7f }
	if strings.ContainsFunc(name, bad) {
		return errors.New("a name may not hold < > or control characters")
	}
	if strings.ContainsFunc(email, bad) || strings.ContainsAny(email, " \t") || !strings.Contains(email, "@") {
		return errors.New("that does not look like an email address")
	}
	return nil
}

// SetIdentity writes name and email as user.name and user.email in the
// local configuration of the repository at root, which every worktree of
// it shares: the commits a card's agent makes there and the ones gummi
// makes when it lands a card are written as that person. Both empty
// removes the repository's own setting, leaving the machine's. It never
// touches the global configuration, which other repositories read.
func SetIdentity(ctx context.Context, root, name, email string) error {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if err := CheckIdentity(name, email); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"user.name", name}, {"user.email", email}} {
		if kv[1] == "" {
			// unsetting a key that is not set is git's exit 5, and fine
			if ok, _ := gitOK(ctx, root, "config", "--local", "--get", kv[0]); !ok {
				continue
			}
			if _, err := runGit(ctx, root, "config", "--local", "--unset-all", kv[0]); err != nil {
				return err
			}
			continue
		}
		if _, err := runGit(ctx, root, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}
