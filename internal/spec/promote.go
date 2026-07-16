package spec

import (
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// Promote materializes the item's artifact (spec or bug report) at its
// workspace home artifactPath — .gummi/specs|bugs in the main checkout —
// consuming the draft that carried it through the design stages. Content
// preference: an artifact already at home wins (promotion already
// happened); else the draft; else the copy at legacyPath (items
// mid-flight from the era when the artifact was committed to the feature
// branch, read from their worktree); else a fresh template, so an agent
// never starts against a missing artifact.
//
// Crash-safe and idempotent: the draft is removed only after the
// artifact is durably in place, and a rerun that finds the artifact just
// retires any draft remnant.
func Promote(artifactPath, draftPath, legacyPath string, f *domain.Feature) error {
	unlock := LockFile(artifactPath)
	defer unlock()
	if _, err := os.Stat(artifactPath); err == nil {
		return os.RemoveAll(draftPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	content := blankTemplate(f)
	if raw, err := os.ReadFile(draftPath); err == nil {
		content = string(raw)
	} else if !os.IsNotExist(err) {
		return err
	} else if raw, err := os.ReadFile(legacyPath); err == nil {
		content = string(raw)
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o750); err != nil {
		return err
	}
	if err := atomicfile.Write(artifactPath, []byte(content), 0o600); err != nil {
		return err
	}
	return os.RemoveAll(draftPath)
}
