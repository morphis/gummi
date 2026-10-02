package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/webapi"
)

// An agent names a file it wrote by its absolute path; the card's head
// tells a paired device where that worktree is served, and the file comes
// back sandboxed, with no cookie, from that URL alone.
func TestFilesServesTheWorktreeSandboxed(t *testing.T) {
	b := newDocsBoard(t, agent.NewFake("ok"))
	writeFile(t, filepath.Join(b.wt, "mockup", "page.html"), "<h1>mockup</h1>")
	writeFile(t, filepath.Join(b.root, "secret.txt"), "outside")
	if err := os.Symlink(filepath.Join(b.root, "secret.txt"), filepath.Join(b.wt, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	var c webapi.Card
	if st := b.get("/api/cards/FD-001", &c); st != http.StatusOK {
		t.Fatalf("card = %d", st)
	}
	if c.Files == nil || c.Files.Dir != b.wt || c.Files.URL != b.srv.filesURL("FD-001") {
		t.Fatalf("card files = %+v, want dir %q", c.Files, b.wt)
	}

	// a fresh client: the URL is the key, not the cookie
	get := func(path string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, b.http.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(body)
	}
	code, hdr, body := get(c.Files.URL + "mockup/page.html")
	if code != http.StatusOK || body != "<h1>mockup</h1>" {
		t.Fatalf("page = %d %q", code, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
	if csp := hdr.Get("Content-Security-Policy"); csp != filesCSP {
		t.Errorf("csp = %q, want the sandbox", csp)
	}

	for name, path := range map[string]string{
		"wrong key":        "/files/FD-001/nope/mockup/page.html",
		"another card's":   b.srv.filesURL("FD-002") + "mockup/page.html",
		"a directory":      c.Files.URL + "mockup",
		"a symlink out":    c.Files.URL + "escape.txt",
		"dot-dot":          c.Files.URL + "..%2fsecret.txt",
		"git's own":        c.Files.URL + ".git",
		"a file not there": c.Files.URL + "nope.html",
	} {
		if code, _, body := get(path); code == http.StatusOK {
			t.Errorf("%s: %s = 200 %q", name, path, body)
		}
	}
}
