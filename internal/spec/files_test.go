package spec

import (
	"reflect"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestParseFiles(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []domain.PlannedFile
		found   bool
		wantErr bool
	}{
		{
			name:    "no block",
			content: "# FD-001\n\n## Implementation notes\n\n1. do the thing\n",
		},
		{
			name: "paths and roles",
			content: "## Implementation notes\n\n```gummi-files\n" +
				"- path: internal/engine/engine.go\n  role: locate resolves the workdir\n" +
				"- path: internal/worktree/scratch.go\n  role: the tree itself\n  new: true\n" +
				"```\n",
			found: true,
			want: []domain.PlannedFile{
				{Path: "internal/engine/engine.go", Role: "locate resolves the workdir"},
				{Path: "internal/worktree/scratch.go", Role: "the tree itself", New: true},
			},
		},
		{
			// found=true with no entries is a plan that wrote an empty
			// manifest — distinguishable from one that wrote none.
			name:    "present but empty",
			content: "```gummi-files\n[]\n```\n",
			found:   true,
		},
		{
			name:    "entries without a path are dropped",
			content: "```gummi-files\n- role: nowhere in particular\n- path: a.go\n```\n",
			found:   true,
			want:    []domain.PlannedFile{{Path: "a.go"}},
		},
		{
			// the manifest is a reading aid; an entry pointing outside the
			// tree is a mistake or an invitation to go somewhere the work
			// does not live.
			name:    "paths outside the repo are dropped",
			content: "```gummi-files\n- path: /etc/passwd\n- path: ../../secrets\n- path: ok.go\n```\n",
			found:   true,
			want:    []domain.PlannedFile{{Path: "ok.go"}},
		},
		{
			name:    "duplicates collapse",
			content: "```gummi-files\n- path: a.go\n  role: first\n- path: a.go\n  role: again\n```\n",
			found:   true,
			want:    []domain.PlannedFile{{Path: "a.go", Role: "first"}},
		},
		{
			// a malformed block is a defect worth surfacing, not an empty one
			name:    "malformed yaml",
			content: "```gummi-files\n- path: [unclosed\n```\n",
			found:   true,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := ParseFiles(tc.content)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if found != tc.found {
				t.Errorf("found = %v, want %v", found, tc.found)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("files = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// RenderFiles emits the shape ParseFiles reads, so a writer and a reader
// cannot drift.
func TestRenderFilesRoundTrips(t *testing.T) {
	want := []domain.PlannedFile{
		{Path: "internal/a.go", Role: "the seam"},
		{Path: "internal/b.go", New: true},
	}
	got, found, err := ParseFiles("## Implementation notes\n\n" + RenderFiles(want) + "\n")
	if err != nil || !found {
		t.Fatalf("ParseFiles(RenderFiles(...)) = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
