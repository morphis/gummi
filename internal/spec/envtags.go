package spec

import (
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/domain"
)

// envTagRe matches [env: <name>] tags in prose verification lines.
var envTagRe = regexp.MustCompile(`\[env:\s*([^\]]+)\]`)

// waiverRe matches a human-authored %% @user: no-live-check <reason> marker.
// The reason is constrained to the same line so an empty reason cannot steal
// from the following prose.
var waiverRe = regexp.MustCompile(`(?m)^%%\s*@user:\s*no-live-check[ \t]+(.+)$`)

// VerificationSection returns the on-disk heading name for the verification
// section of an artifact, keyed by card kind.
func VerificationSection(k domain.Kind) string {
	if k == domain.KindBug {
		return "Verification"
	}
	return "Verification plan"
}

// strippedVerification returns the Verification section body with fenced
// gummi-checks blocks removed. Both EnvTags and NoLiveCheckWaiver read this
// stripped content so tags or waiver markers inside a fenced example can
// never influence the omission gate.
func strippedVerification(content string, k domain.Kind) string {
	body, ok := ViewSection(content, VerificationSection(k))
	if !ok {
		return ""
	}
	return stripGummiChecksBlocks(body)
}

// stripGummiChecksBlocks removes ```gummi-checks ... ``` fenced blocks
// from content, preserving everything else verbatim.
func stripGummiChecksBlocks(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "```gummi-checks" {
			inBlock = true
			continue
		}
		if inBlock && trimmed == "```" {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// EnvTags returns the names from every [env: <name>] tag found in the prose
// of the artifact's Verification section. Tags inside the fenced gummi-checks
// block are ignored.
func EnvTags(content string, k domain.Kind) []string {
	body := strippedVerification(content, k)
	matches := envTagRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// NoLiveCheckWaiver reports whether the artifact's Verification section
// contains a human-authored `%% @user: no-live-check <reason>` marker in
// prose (outside the fenced gummi-checks block). The waiver disarms the
// omission gate only when a non-empty reason is present.
func NoLiveCheckWaiver(content string, k domain.Kind) (reason string, ok bool) {
	body := strippedVerification(content, k)
	m := waiverRe.FindStringSubmatch(body)
	if m == nil {
		return "", false
	}
	reason = strings.TrimSpace(m[1])
	return reason, reason != ""
}
