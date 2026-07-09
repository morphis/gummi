package spec

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// The artifact's Verification section carries the repo's check commands
// as a fenced block gummi can execute deterministically:
//
//	```gummi-checks
//	- name: test
//	  cmd: go test ./...
//	```
//
// The block is written by check auto-discovery at approval and is
// ordinary spec content afterwards: the architect and implementer edit
// it like prose, it rides the approval gates, and it travels with the
// branch. The Verify stage runs exactly what it lists.

// checksFenceRe matches one ```gummi-checks … ``` block (the first wins).
var checksFenceRe = regexp.MustCompile("(?s)```gummi-checks\\s*\\n(.*?)```")

// ParseChecks extracts the artifact's gummi-checks block. found reports
// whether a block exists at all — even one that yields no usable checks —
// so callers can distinguish "never discovered" from "present but empty".
// Entries without a cmd are dropped; a missing name defaults to the cmd,
// mirroring how checks are displayed.
func ParseChecks(content string) (checks []domain.Check, found bool) {
	m := checksFenceRe.FindStringSubmatch(content)
	if m == nil {
		return nil, false
	}
	var raw []domain.Check
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil {
		return nil, true
	}
	for _, c := range raw {
		if strings.TrimSpace(c.Cmd) == "" {
			continue
		}
		if strings.TrimSpace(c.Name) == "" {
			c.Name = c.Cmd
		}
		checks = append(checks, c)
	}
	return checks, true
}

// RenderChecks renders the canonical fenced block for a check list.
func RenderChecks(checks []domain.Check) string {
	body, err := yaml.Marshal(checks)
	if err != nil { // a []Check of plain strings cannot fail to marshal
		body = nil
	}
	return "```gummi-checks\n" + string(body) + "```"
}

// UpsertChecks writes the checks into content as a gummi-checks block:
// an existing block is replaced in place; otherwise the block is
// inserted at the top of the Verification section ("## Verification
// plan" in a spec, "## Verification" in a bug report). An artifact
// without that section is an error — both templates always carry it.
func UpsertChecks(content string, checks []domain.Check) (string, error) {
	block := RenderChecks(checks)
	if loc := checksFenceRe.FindStringIndex(content); loc != nil {
		return content[:loc[0]] + block + content[loc[1]:], nil
	}
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "## Verification") {
			out := make([]string, 0, len(lines)+8)
			out = append(out, lines[:i+1]...)
			out = append(out, "")
			out = append(out, strings.Split(block, "\n")...)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	return "", fmt.Errorf("no Verification section to hold the checks")
}
