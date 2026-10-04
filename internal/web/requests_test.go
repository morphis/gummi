package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/webapi"
)

// What a request may say is checked at the door: a malformed query or
// body is a 400 that names the mistake, never a 500 and never a call
// into the board with a value it cannot read.
func TestRequestsAreCheckedBeforeTheBoardSeesThem(t *testing.T) {
	b := newBoardHarness(t)
	for _, c := range []struct {
		name, method, path string
		body               any
		want               string
	}{
		{"bugs, unknown state", http.MethodGet, "/api/bugs?state=stale", nil, "state is open, closed or all"},
		{"bugs, limit not a number", http.MethodGet, "/api/bugs?limit=lots", nil, "limit is a number"},
		{"bugs, negative limit", http.MethodGet, "/api/bugs?limit=-1", nil, "limit is a number"},
		{"import, negative limit", http.MethodPost, "/api/bugs", webapi.BugsRequest{Limit: -1}, "limit is a number"},
		{"resume, wrong shape", http.MethodPost, "/api/board/resume", map[string]any{"cards": "FD-001"}, "expected"},
		// a budget is whole credits: a fraction, or a number past an int,
		// is the person's typing and is answered in the field's words
		{"new card, fractional budget", http.MethodPost, "/api/cards", map[string]any{"kind": "feature", "title": "x", "envelope": 2.5}, "Budget must be a whole, non-negative number of credits"},
		{"new card, huge budget", http.MethodPost, "/api/cards", map[string]any{"kind": "feature", "title": "x", "envelope": 1e20}, "Budget must be a whole, non-negative number of credits"},
		{"budget action, fractional", http.MethodPost, "/api/cards/FD-001/actions/envelope", map[string]any{"number": 1.5}, "Budget must be a whole, non-negative number of credits"},
		{"goal, fractional budget", http.MethodPost, "/api/goals", map[string]any{"description": "x", "envelope": 1.5}, "Budget must be a whole, non-negative number of credits"},
		{"goal action, fractional budget", http.MethodPost, "/api/goals/GL-001/actions/budget", map[string]any{"envelope": 1.5}, "Budget must be a whole, non-negative number of credits"},
		// refused before the pass runs, not at approve after the review
		{"ingest, negative envelope", http.MethodPost, "/api/ingest", map[string]any{"markdown": "# x\n\n## one\n", "envelope": -50}, "whole, non-negative number of credits"},
		{"ingest, fractional envelope", http.MethodPost, "/api/ingest", map[string]any{"markdown": "# x\n\n## one\n", "envelope": 1.5}, "Budget must be a whole"},
	} {
		var e webapi.Error
		if got := b.call(c.method, c.path, c.body, &e); got != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", c.name, got)
			continue
		}
		if !strings.Contains(e.Error, c.want) {
			t.Errorf("%s: error = %q, want it to say %q", c.name, e.Error, c.want)
		}
	}
	// a board with nothing parked has no question to answer
	if got := b.call(http.MethodPost, "/api/board/resume", webapi.ResumeRequest{None: true}, nil); got < 400 || got >= 500 {
		t.Errorf("answering a resume question nobody asked = %d, want a 4xx", got)
	}
}

func (b *boardHarness) upload(fields map[string]string, file, content string) (int, webapi.Error) {
	b.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			b.t.Fatal(err)
		}
	}
	if file != "" {
		fw, err := mw.CreateFormFile("file", file)
		if err != nil {
			b.t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
	}
	if err := mw.Close(); err != nil {
		b.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, b.http.URL+"/api/ingest", &buf)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Origin", b.http.URL)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	var e webapi.Error
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &e)
	return res.StatusCode, e
}

func TestIngestAcceptsAnUploadedFile(t *testing.T) {
	b := newBoardHarness(t)
	if got, e := b.upload(map[string]string{"envelope": "many"}, "", ""); got != http.StatusBadRequest ||
		!strings.Contains(e.Error, "envelope per card must be a whole, non-negative number") {
		t.Fatalf("a non-numeric envelope = %d %q", got, e.Error)
	}
	if got, _ := b.upload(map[string]string{"envelope": "-5"}, "", ""); got != http.StatusBadRequest {
		t.Fatalf("a negative envelope = %d, want 400", got)
	}

	got, e := b.upload(map[string]string{"envelope": "300"}, "rows.md", "# Rows\n\n## §1 load\n\n## §2 cache\n")
	if got != http.StatusAccepted {
		t.Fatalf("an uploaded document = %d %q, want 202", got, e.Error)
	}
	var run webapi.IngestRun
	b.must(http.StatusOK, http.MethodGet, "/api/ingest/1", nil, &run)
	if !strings.HasSuffix(run.Source, "/.gummi/ingest/rows.md") {
		t.Fatalf("run = %+v, want the upload saved under its own name", run)
	}
}
