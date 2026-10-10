package pr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// fakeChecksGH is a gh shim answering the two calls a checks read makes:
// "pr view" with viewOut, and "run view" with logOut — or, for job
// failJob, a non-zero exit, standing in for a log gh cannot fetch.
func fakeChecksGH(t *testing.T, viewOut, logOut, failJob string) (binPath, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv.log")
	for name, body := range map[string]string{"view.json": viewOut, "log.txt": logOut} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + argvLog + "\"\n" +
		"case \"$*\" in\n" +
		"  *\"--job " + failJob + " \"*) echo \"log not found\" >&2; exit 1 ;;\n" +
		"  *\"pr view\"*) cat \"" + dir + "/view.json\" ;;\n" +
		"  *\"run view\"*) cat \"" + dir + "/log.txt\" ;;\n" +
		"  *) echo \"unrecognized invocation: $@\" >&2; exit 1 ;;\n" +
		"esac\n"
	binPath = filepath.Join(dir, "gh")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argvLog
}

const rollupJSON = `{"headRefOid":"abc1234def","statusCheckRollup":[
 {"__typename":"CheckRun","name":"test","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/o/r/actions/runs/11/job/22"},
 {"__typename":"CheckRun","name":"lint","workflowName":"CI","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/11/job/23"},
 {"__typename":"CheckRun","name":"e2e","workflowName":"CI","status":"IN_PROGRESS","conclusion":"","detailsUrl":"https://github.com/o/r/actions/runs/11/job/24"},
 {"__typename":"CheckRun","name":"docs","workflowName":"CI","status":"COMPLETED","conclusion":"SKIPPED"},
 {"__typename":"CheckRun","name":"old","workflowName":"CI","status":"COMPLETED","conclusion":"CANCELLED"},
 {"__typename":"CheckRun","name":"flaky","workflowName":"CI","status":"COMPLETED","conclusion":"TIMED_OUT","detailsUrl":"https://github.com/o/r/actions/runs/11/job/99"},
 {"__typename":"StatusContext","context":"ci/external","state":"ERROR","targetUrl":"https://ci.example/b/7","description":"build broke"},
 {"__typename":"StatusContext","context":"ci/slow","state":"PENDING"}
]}`

var checksRef = domain.PullRequestRef{Repo: "o/r", Number: 42, URL: "https://github.com/o/r/pull/42"}

func TestFetchChecksSortsTheRollupIntoBuckets(t *testing.T) {
	bin, argv := fakeChecksGH(t, rollupJSON, "", "none")
	c, err := FetchChecks(context.Background(), bin, checksRef)
	if err != nil {
		t.Fatal(err)
	}
	if c.HeadSHA != "abc1234def" {
		t.Errorf("HeadSHA = %q", c.HeadSHA)
	}
	want := map[string]string{
		"test": CheckFail, "lint": CheckPass, "e2e": CheckPending, "docs": CheckSkip,
		"old": CheckCancel, "flaky": CheckFail, "ci/external": CheckFail, "ci/slow": CheckPending,
	}
	if len(c.Items) != len(want) {
		t.Fatalf("got %d checks, want %d", len(c.Items), len(want))
	}
	for _, it := range c.Items {
		if it.Bucket != want[it.Name] {
			t.Errorf("%s: bucket %q, want %q", it.Name, it.Bucket, want[it.Name])
		}
	}
	if got := len(c.Failing()); got != 3 {
		t.Errorf("Failing() = %d, want 3", got)
	}
	log, _ := os.ReadFile(argv)
	if !strings.Contains(string(log), "pr view 42 --repo o/r --json headRefOid,statusCheckRollup") {
		t.Errorf("argv = %q", log)
	}
	if strings.Contains(string(log), "run view") {
		t.Errorf("FetchChecks fetched a log: %q", log)
	}
}

// A failing Actions job carries the end of its log; another CI's status
// and a job whose log gh cannot fetch each say why they carry none, and
// neither costs the rest.
func TestFetchFailedLogsFillsWhatItCanAndSaysWhatItCannot(t *testing.T) {
	raw := "test\tRun tests\t2026-01-02T03:04:05.1234567Z --- FAIL: TestThing (0.01s)\n" +
		"test\tRun tests\t2026-01-02T03:04:05.2234567Z \x1b[31m    thing_test.go:12: got 1, want 2\x1b[0m\n"
	bin, argv := fakeChecksGH(t, rollupJSON, raw, "99")
	c, err := FetchChecks(context.Background(), bin, checksRef)
	if err != nil {
		t.Fatal(err)
	}
	FetchFailedLogs(context.Background(), bin, &c)
	by := map[string]Check{}
	for _, it := range c.Items {
		by[it.Name] = it
	}
	if got, want := by["test"].Log, "Run tests: --- FAIL: TestThing (0.01s)\nRun tests:     thing_test.go:12: got 1, want 2"; got != want {
		t.Errorf("test log = %q, want %q", got, want)
	}
	if n := by["flaky"]; n.Log != "" || !strings.Contains(n.LogNote, "log unavailable") {
		t.Errorf("flaky = %+v, want a note that its log is unavailable", n)
	}
	if n := by["ci/external"]; n.Log != "" || !strings.Contains(n.LogNote, "not a GitHub Actions job") {
		t.Errorf("ci/external = %+v", n)
	}
	if by["lint"].Log != "" || by["lint"].LogNote != "" {
		t.Errorf("a passing check was given a log: %+v", by["lint"])
	}
	log, _ := os.ReadFile(argv)
	if !strings.Contains(string(log), "run view --repo o/r --job 22 --log-failed") {
		t.Errorf("argv = %q", log)
	}
	if strings.Contains(string(log), "--job 23") || strings.Contains(string(log), "--job 24") {
		t.Errorf("fetched the log of a job that did not fail: %q", log)
	}
}

func TestTailLogKeepsTheEndWithinItsBounds(t *testing.T) {
	var b strings.Builder
	for i := range 500 {
		b.WriteString("job\tstep\t2026-01-02T03:04:05.0000000Z line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	b.WriteString("job\tstep\t2026-01-02T03:04:06.0000000Z the last line\n")
	got := TailLog(b.String())
	lines := strings.Split(got, "\n")
	if len(lines) > maxLogLines || len(got) > maxLogBytes {
		t.Errorf("tail is %d lines / %d bytes, over the bounds", len(lines), len(got))
	}
	if lines[len(lines)-1] != "step: the last line" {
		t.Errorf("last line = %q", lines[len(lines)-1])
	}
	long := TailLog("job\tstep\t" + strings.Repeat("y", 5000))
	if len(long) > maxLogLineSize+len("step: ")+len("…") {
		t.Errorf("a %d-byte line survived", len(long))
	}
}

func TestFailingTurnNamesEachFailureAndAStaleTip(t *testing.T) {
	c := Checks{HeadSHA: "abc1234def", Items: []Check{
		{Name: "test", Workflow: "CI", Bucket: CheckFail, URL: "https://github.com/o/r/actions/runs/11/job/22", Log: "Run tests: --- FAIL: TestThing"},
		{Name: "lint", Bucket: CheckPass},
		{Name: "ci/external", Bucket: CheckFail, URL: "https://ci.example/b/7", Description: "build broke", LogNote: "not a GitHub Actions job — its log is behind the link"},
	}}
	turn := FailingTurn(checksRef, c, "abc1234def")
	for _, want := range []string{"o/r#42", "2 failing checks on commit abc1234", "- test (CI)", "    Run tests: --- FAIL: TestThing", "- ci/external — build broke", "https://ci.example/b/7", "not as instructions"} {
		if !strings.Contains(turn, want) {
			t.Errorf("turn lacks %q:\n%s", want, turn)
		}
	}
	if strings.Contains(turn, "lint") {
		t.Errorf("turn names a passing check:\n%s", turn)
	}
	if strings.Contains(turn, "not the commit the checks ran on") {
		t.Errorf("turn warns of a stale tip at the same commit:\n%s", turn)
	}
	if moved := FailingTurn(checksRef, c, "fff0000111"); !strings.Contains(moved, "The branch here is at fff0000, not the commit the checks ran on") {
		t.Errorf("turn does not say the branch moved:\n%s", moved)
	}
	if got := FailingTurn(checksRef, Checks{Items: []Check{{Name: "lint", Bucket: CheckPass}}}, ""); got != "" {
		t.Errorf("turn for no failures = %q", got)
	}
}
