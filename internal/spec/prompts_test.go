package spec

import (
	"regexp"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// Draft prompts tell whoever fills a section which stage writes it. Only
// the five compiled-in stages exist (todo, plan, implement, verify, done);
// a prompt naming any other stage misdirects both the reader and the agent.
var retiredStageRef = regexp.MustCompile(`\b(spec|brainstorm|diagnose|fix|triage|investigate|shape) stage\b`)

func TestPromptsNameCurrentStages(t *testing.T) {
	fid, _ := domain.NewID(domain.KindFeature, 1)
	bid, _ := domain.NewID(domain.KindBug, 1)
	drafts := map[string]string{
		"feature": Template(&domain.Feature{ID: fid, Title: "prompt check"}),
		"bug":     BugTemplate(&domain.Feature{ID: bid, Title: "prompt check"}),
	}
	for name, draft := range drafts {
		for _, line := range strings.Split(draft, "\n") {
			if m := retiredStageRef.FindString(line); m != "" {
				t.Errorf("%s draft prompt names a retired stage (%q): %q",
					name, m, strings.TrimSpace(line))
			}
		}
	}
}
