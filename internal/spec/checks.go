package spec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// maxCheckTimeout mirrors verify.MaxCheckTimeout so spec parsing can
// reject per-check timeouts without importing the verify package.
const maxCheckTimeout = 30 * time.Minute

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
// it like prose, and it rides the approval gates. The Verify stage runs
// exactly what it lists.
//
// That last sentence is the whole difficulty: the block is a strict-YAML
// island inside a section three roles rewrite as prose, and prose habits
// do not survive a YAML parser. Parsing is therefore forgiving (see
// repairChecksBlock) and rendering is always canonical — RenderChecks
// marshals, so anything gummi writes itself is correct by construction.

// checksFenceRe matches one ```gummi-checks … ``` block (the first wins).
var checksFenceRe = regexp.MustCompile("(?s)```gummi-checks\\s*\\n(.*?)```")

// ParseChecks extracts the artifact's gummi-checks block. found reports
// whether a block exists at all — even one that yields no usable checks —
// so callers can distinguish "never discovered" from "present but empty".
// err is non-nil only when a block exists and neither it nor its repaired
// form parses: a malformed block is a defect worth surfacing, not an
// empty one. Entries without a cmd are dropped; a missing name defaults
// to the cmd, mirroring how checks are displayed.
func ParseChecks(content string) (checks []domain.Check, found bool, err error) {
	m := checksFenceRe.FindStringSubmatch(content)
	if m == nil {
		return nil, false, nil
	}
	body := m[1]
	raw, strictErr := decodeChecks(body)
	// A block an agent wrote by hand is far more often lightly malformed
	// than meaningless. The repair runs unconditionally rather than only
	// as a rescue, because two of the mistakes it fixes do not fail the
	// parse at all — a [CI-only] tag glued to a command parses cleanly
	// and then runs as part of the command, which is worse than an
	// error. Where the repair cannot help, the ORIGINAL error is what
	// gets reported: its line numbers point at the file the reader is
	// looking at, not at a body they never see.
	if repaired, changed := repairChecksBlock(body); changed {
		if reparsed, rerr := decodeChecks(repaired); rerr == nil {
			raw, strictErr = reparsed, nil
		}
	}
	if strictErr != nil {
		return nil, true, fmt.Errorf("gummi-checks block does not parse: %s", checksShapeError(strictErr, body))
	}
	for _, c := range raw {
		if strings.TrimSpace(c.Cmd) == "" {
			continue
		}
		if strings.TrimSpace(c.Name) == "" {
			c.Name = c.Cmd
		}
		if err := validateCheckTimeout(c); err != nil {
			return nil, true, fmt.Errorf("gummi-checks block does not parse: %w", err)
		}
		checks = append(checks, c)
	}
	return checks, true, nil
}

// decodeChecks is the raw YAML step, split out so a body can be decoded
// twice — once as written, once repaired.
func decodeChecks(body string) ([]domain.Check, error) {
	var out []domain.Check
	if err := yaml.Unmarshal([]byte(body), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// gummiMarkerRe matches gummi's own comment markers (%% @reviewer:,
// %% @gummi:, %% @user:), which agents append to the Verification
// section and sometimes land inside the fence. A line opening with %% is
// a YAML directive indicator, so one stray marker takes the whole block
// down ("could not find expected directive name").
var gummiMarkerRe = regexp.MustCompile(`^\s*%%`)

// skipTagRe matches the allowed-skip tags — [CI-only] and [env: <prereq>]
// — that belong on prose live-check lines. Inside the block they are a
// plan defect the verify agent is told to flag, but they must not also
// stop the block from parsing: [env: docker] carries a colon-space, which
// is exactly the "mapping values are not allowed in this context" break.
// Stripping them is strictly better than quoting them, because the tag
// was never part of the command a shell should run.
var skipTagRe = regexp.MustCompile(`\s*\[(?i:ci-only|env:[^\]]*)\]`)

// checkKeyRe splits a check entry's line into its indent, optional "- "
// sequence dash, key, and value.
var checkKeyRe = regexp.MustCompile(`^(\s*)(-\s+)?(name|cmd|timeout|baseline):[ \t]+(\S.*)$`)

// repairChecksBlock rewrites the block body into the YAML an agent meant
// to write, and reports whether it changed anything. It repairs only the
// three mistakes that actually happen — each one observed in a real
// artifact — and leaves everything else to fail loudly:
//
//	%% markers inside the fence      dropped (they are section prose)
//	[env: …] / [CI-only] in a value  stripped (tags are not commands)
//	an unquoted value holding ": "   single-quoted
//	a tab-indented continuation      re-indented with spaces
//
// The last is the common one: a command or name containing a colon-space
// reads as a second mapping key, and yaml reports "mapping values are not
// allowed in this context" against a line the reader cannot see a
// numbering for.
//
// A continuation key at column zero is also re-indented under its entry,
// because "with cmd: <command> on the next line" is how the block's shape
// is described to the roles that write it, and the next line taken
// literally is not indented.
func repairChecksBlock(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	inEntry := false
	// A literal/folded scalar's body is content, not structure: a line
	// under `cmd: |` that happens to open with "cmd: " is part of the
	// command, and rewriting it would corrupt what the block says. -1
	// means "not inside one".
	blockScalar := -1
	for _, ln := range lines {
		if blockScalar >= 0 {
			if strings.TrimSpace(ln) == "" || leadingSpaces(ln) > blockScalar {
				out = append(out, ln)
				continue
			}
			blockScalar = -1
		}
		if gummiMarkerRe.MatchString(ln) {
			changed = true
			continue
		}
		if detabbed := detabIndent(ln); detabbed != ln {
			ln, changed = detabbed, true
		}
		m := checkKeyRe.FindStringSubmatch(ln)
		if m == nil {
			if strings.HasPrefix(strings.TrimSpace(ln), "- ") {
				inEntry = true
			}
			out = append(out, ln)
			continue
		}
		indent, dash, key, val := m[1], m[2], m[3], m[4]
		if dash != "" {
			inEntry = true
		} else if inEntry && indent == "" {
			// a continuation key written flush left
			indent = "  "
			changed = true
		}
		if stripped := strings.TrimSpace(skipTagRe.ReplaceAllString(val, "")); stripped != val {
			if stripped == "" { // the value was nothing but a tag
				changed = true
				continue
			}
			val, changed = stripped, true
		}
		if strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">") {
			blockScalar = len(indent) + len(dash)
		} else if quoted, ok := quoteYAMLValue(val); ok {
			val, changed = quoted, true
		}
		out = append(out, indent+dash+key+": "+val)
	}
	if !changed {
		return body, false
	}
	return strings.Join(out, "\n"), true
}

// detabIndent rewrites a line's leading tabs as two spaces each. YAML
// forbids tabs in indentation outright ("found a tab character that
// violates indentation"), so a line carrying them was already broken and
// nothing is lost by normalizing.
func detabIndent(ln string) string {
	i := 0
	for i < len(ln) && (ln[i] == ' ' || ln[i] == '\t') {
		i++
	}
	if !strings.Contains(ln[:i], "\t") {
		return ln
	}
	return strings.ReplaceAll(ln[:i], "\t", "  ") + ln[i:]
}

// leadingSpaces counts a line's indentation, after detabbing has made it
// meaningful.
func leadingSpaces(ln string) int {
	return len(ln) - len(strings.TrimLeft(ln, " "))
}

// quoteYAMLValue single-quotes a plain scalar that YAML would refuse (or
// silently truncate), reporting whether it did. An already-quoted value,
// a block scalar, and a value with no colon-space are left alone.
func quoteYAMLValue(val string) (string, bool) {
	if val == "" || strings.ContainsAny(val[:1], `'"|>&*!%@`+"`") {
		return val, false
	}
	if !strings.Contains(val, ": ") && !strings.HasSuffix(val, ":") {
		return val, false
	}
	return "'" + strings.ReplaceAll(val, "'", "''") + "'", true
}

// yamlLineRe pulls the line number out of a yaml.v3 error so the offending
// text can be quoted back.
var yamlLineRe = regexp.MustCompile(`line (\d+):`)

// checksShapeError turns a yaml decode failure on the checks block into a
// sentence the person reading it can act on.
//
// The raw error is written for whoever wrote the Go type — round 3 §1.5
// put "cannot unmarshal !!str `go buil...` into domain.Check" on screen,
// four times, at a user who has never heard of domain.Check and is given
// no way to learn what the block should look like. Every fact needed to
// fix it is here and none of it was being said.
//
// The plain-string list is called out by name because it is the mistake
// that actually happens: the schema example lives in the discovery prompt,
// which only the scribe ever reads, so an architect asked to REWRITE the
// block (which is what a review comment about the checks provokes) writes
// the obvious thing — a YAML list of command strings — and the block stops
// parsing. spec.go's section prompts now carry the shape too, so this
// error is the second line of defence rather than the only one.
//
// "line 4" is likewise unactionable on its own: it counts from inside the
// fence, not from the top of the file, so the reader has nothing to count
// against. The offending line is quoted back instead.
func checksShapeError(err error, body string) string {
	msg := "each entry needs a name: and a cmd:, like\n" +
		"    - name: test\n" +
		"      cmd: go test ./...\n" +
		"  (indent the cmd: line under its entry, and quote any value containing a colon)"
	if strings.Contains(err.Error(), "cannot unmarshal !!str") {
		return "the entries are plain command strings, not name/cmd pairs — " + msg
	}
	if offender := offendingLine(err, body); offender != "" {
		return "at " + offender + "\n  " + msg
	}
	return msg + "\n  (" + err.Error() + ")"
}

// offendingLine renders the block line a yaml error points at, since the
// error's own line number is relative to the fence.
func offendingLine(err error, body string) string {
	m := yamlLineRe.FindStringSubmatch(err.Error())
	if m == nil {
		return ""
	}
	n, convErr := strconv.Atoi(m[1])
	lines := strings.Split(body, "\n")
	if convErr != nil || n < 1 || n > len(lines) {
		return ""
	}
	text := strings.TrimSpace(lines[n-1])
	if len(text) > 120 {
		text = text[:117] + "..."
	}
	return fmt.Sprintf("block line %d, %q: %s", n, text, yamlLineRe.ReplaceAllString(err.Error(), ""))
}

// validateCheckTimeout rejects malformed or over-ceiling per-check timeout
// values, naming the offending check for the caller's error message.
func validateCheckTimeout(c domain.Check) error {
	if c.Timeout == "" {
		return nil
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return fmt.Errorf("check %q: invalid timeout %q: %w", c.Name, c.Timeout, err)
	}
	if d > maxCheckTimeout {
		return fmt.Errorf("check %q: timeout %s exceeds maximum %s", c.Name, d, maxCheckTimeout)
	}
	return nil
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
