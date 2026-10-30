package spec

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// The artifact's Implementation notes carry the plan's file manifest as a
// fenced block, the same shape the Verification plan uses for checks:
//
//	```gummi-files
//	- path: internal/engine/engine.go
//	  role: locate() resolves the stage's working directory
//	- path: internal/worktree/scratch.go
//	  role: the scratch tree itself
//	  new: true
//	```
//
// It is written by whichever stage drafts the Implementation notes — Plan
// on the full route, the quick-spec flavor when it folds the plan in —
// and is ordinary artifact content afterwards, edited like prose and
// riding the same approval gates.

// filesFenceRe matches one ```gummi-files … ``` block (the first wins),
// mirroring checksFenceRe.
var filesFenceRe = regexp.MustCompile("(?s)```gummi-files\\s*\\n(.*?)(?m:^)[ ]{0,3}```")

// ParseFiles extracts the artifact's gummi-files block. found reports
// whether a block exists at all — even one that yields no usable entries
// — so a caller can tell "the plan never wrote one" from "the plan wrote
// an empty one". err is non-nil only when a block exists but its YAML
// does not parse: a malformed block is a defect worth surfacing, not an
// empty one.
//
// Entries without a path are dropped. A path that escapes the repo, or
// names an absolute location, is dropped too: the manifest is a reading
// aid, and an entry pointing outside the tree is either a mistake or an
// invitation to go somewhere the work does not live.
func ParseFiles(content string) (files []domain.PlannedFile, found bool, err error) {
	m := filesFenceRe.FindStringSubmatch(content)
	if m == nil {
		return nil, false, nil
	}
	var raw []domain.PlannedFile
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil {
		return nil, true, fmt.Errorf("gummi-files block does not parse: %w", err)
	}
	seen := map[string]bool{}
	for _, f := range raw {
		p := strings.TrimSpace(f.Path)
		// one file listed twice ("a/b", "./a/b") is one entry
		if p == "" || !repoRelative(p) || seen[path.Clean(p)] {
			continue
		}
		seen[path.Clean(p)] = true
		f.Path = p
		f.Role = strings.TrimSpace(f.Role)
		files = append(files, f)
	}
	return files, true, nil
}

// repoRelative reports whether p is a plain repo-relative path: not
// absolute, and not climbing out through "..".
func repoRelative(p string) bool {
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	// a drive letter (C:/x) names a location on a machine, not in a tree
	if len(p) >= 2 && p[1] == ':' && ('a' <= p[0]|0x20 && p[0]|0x20 <= 'z') && (len(p) == 2 || p[2] == '/') {
		return false
	}
	clean := path.Clean(p)
	// "." is the whole tree, not a file in it
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// RenderFiles renders the canonical fenced block for a manifest, so a
// writer and a reader agree on one shape.
func RenderFiles(files []domain.PlannedFile) string {
	body, err := marshalBlock(files)
	if err != nil { // a []PlannedFile of plain scalars cannot fail to marshal
		body = nil
	}
	return "```gummi-files\n" + string(body) + "```"
}
