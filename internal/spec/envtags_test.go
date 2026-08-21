package spec

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestVerificationSection(t *testing.T) {
	if got := VerificationSection(domain.KindBug); got != "Verification" {
		t.Errorf("VerificationSection(bug) = %q, want Verification", got)
	}
	if got := VerificationSection(domain.KindFeature); got != "Verification plan" {
		t.Errorf("VerificationSection(feature) = %q, want Verification plan", got)
	}
}

func TestEnvTags(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		kind domain.Kind
		want []string
	}{
		{
			name: "prose tag counted",
			doc:  "# bug\n\n## Verification\n\nRun the docker check [env: docker].\n",
			kind: domain.KindBug,
			want: []string{"docker"},
		},
		{
			name: "tag inside fenced block ignored",
			doc: `# bug

## Verification

` + "```gummi-checks\n# [env: docker]\ntrue\n```\n\nNo prose tags.\n",
			kind: domain.KindBug,
			want: nil,
		},
		{
			name: "feature verification plan section",
			doc:  "# spec\n\n## Verification plan\n\nCheck [env: gpu] and [env: cuda].\n",
			kind: domain.KindFeature,
			want: []string{"gpu", "cuda"},
		},
		{
			name: "no verification section",
			doc:  "# bug\n\n## Problem\n\nNo verification.\n",
			kind: domain.KindBug,
			want: nil,
		},
		{
			name: "deduplicates repeated tags",
			doc:  "# bug\n\n## Verification\n\n[env: docker] and later [env: docker].\n",
			kind: domain.KindBug,
			want: []string{"docker"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EnvTags(tc.doc, tc.kind)
			if len(got) != len(tc.want) {
				t.Fatalf("EnvTags = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("EnvTags[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNoLiveCheckWaiver(t *testing.T) {
	cases := []struct {
		name       string
		doc        string
		kind       domain.Kind
		wantOK     bool
		wantReason string
	}{
		{
			name:       "valid prose waiver",
			doc:        "# bug\n\n## Verification\n\n%% @user: no-live-check local GPU unavailable\n\n prose.\n",
			kind:       domain.KindBug,
			wantOK:     true,
			wantReason: "local GPU unavailable",
		},
		{
			name:   "empty reason not a waiver",
			doc:    "# bug\n\n## Verification\n\n%% @user: no-live-check\n\n prose.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
		{
			name:   "agent marker not a waiver",
			doc:    "# bug\n\n## Verification\n\n%% @architect: no-live-check gpu missing\n\n prose.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
		{
			name:   "reviewer marker not a waiver",
			doc:    "# bug\n\n## Verification\n\n%% @reviewer: no-live-check gpu missing\n\n prose.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
		{
			name: "waiver inside fenced block ignored",
			doc: `# bug

## Verification

` + "```gummi-checks\n%% @user: no-live-check example\ntrue\n```\n\n prose.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
		{
			name: "tag and waiver inside fenced block both ignored",
			doc: `# bug

## Verification

` + "```gummi-checks\n[env: docker]\n%% @user: no-live-check example\ntrue\n```\n\nNo prose signals.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
		{
			name:   "missing section",
			doc:    "# bug\n\n## Problem\n\nNo verification.\n",
			kind:   domain.KindBug,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := NoLiveCheckWaiver(tc.doc, tc.kind)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
