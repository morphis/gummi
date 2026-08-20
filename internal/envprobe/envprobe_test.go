package envprobe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/config"
)

func TestProbeTrueIsPresent(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), map[string]config.EnvPrereq{"ok": {Probe: "true"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if !res[0].Present || res[0].Err != nil {
		t.Errorf("true probe = Present=%v Err=%v, want Present=true Err=nil", res[0].Present, res[0].Err)
	}
}

func TestProbeFalseIsAbsent(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), map[string]config.EnvPrereq{"missing": {Probe: "false"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].Present || res[0].Err != nil {
		t.Errorf("false probe = Present=%v Err=%v, want Present=false Err=nil", res[0].Present, res[0].Err)
	}
}

func TestProbeMissingBinaryIsErrored(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), map[string]config.EnvPrereq{"bad": {Probe: "this-binary-does-not-exist-7f3a"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].Err == nil {
		t.Errorf("missing binary probe Err=%v, want non-nil", res[0].Err)
	}
	if res[0].Present {
		t.Errorf("missing binary probe Present=%v, want false", res[0].Present)
	}
}

func TestProbeTimeoutIsErrored(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), map[string]config.EnvPrereq{"slow": {Probe: "sleep 120"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].Err == nil {
		t.Errorf("timeout probe Err=%v, want non-nil", res[0].Err)
	}
	if res[0].Present {
		t.Errorf("timeout probe Present=%v, want false", res[0].Present)
	}
}

func TestProbeOrderIsSorted(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), map[string]config.EnvPrereq{
		"b": {Probe: "true"},
		"a": {Probe: "true"},
	})
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}
	if res[0].Name != "a" || res[1].Name != "b" {
		t.Errorf("order = %q, %q, want a, b", res[0].Name, res[1].Name)
	}
}

func TestProbeUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	want := "marker.txt"
	if err := os.WriteFile(filepath.Join(dir, want), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), dir, map[string]config.EnvPrereq{"cwd": {Probe: "test -f marker.txt && echo found"}})
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if !res[0].Present || res[0].Err != nil {
		t.Errorf("cwd probe = Present=%v Err=%v, want Present=true Err=nil", res[0].Present, res[0].Err)
	}
	if !strings.Contains(res[0].Output, "found") {
		t.Errorf("cwd probe output = %q, want 'found'", res[0].Output)
	}
}

func TestStatusString(t *testing.T) {
	cases := []struct {
		res  Result
		want string
	}{
		{Result{Present: true, Err: nil}, "PRESENT"},
		{Result{Present: false, Err: nil}, "ABSENT"},
		{Result{Present: false, Err: context.DeadlineExceeded}, "errored"},
	}
	for _, tc := range cases {
		if got := StatusString(tc.res); got != tc.want {
			t.Errorf("StatusString(%+v) = %q, want %q", tc.res, got, tc.want)
		}
	}
}
