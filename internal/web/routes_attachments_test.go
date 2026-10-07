package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

// testPNG is the smallest valid PNG: a 1x1 image.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// putRaw uploads body as POST /api/attachments with the given headers,
// returning the status and decoded JSON body.
func (h *cardBoard) putRaw(c *http.Client, body []byte, hdr map[string]string) (int, map[string]any) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/attachments", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Origin", h.http.URL)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(res.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return res.StatusCode, out
}

func TestAttachmentUpload(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ack"))
	status, body := h.putRaw(h.c, testPNG, map[string]string{"X-Filename": "shot.png"})
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	id, _ := body["id"].(string)
	if id == "" || body["name"] != "shot.png" || body["mediaType"] != "image/png" {
		t.Fatalf("upload body = %v", body)
	}

	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/api/attachments/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET attachment = %d", res.StatusCode)
	}
	if res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing X-Content-Type-Options: nosniff")
	}
	// authenticated content: a shared cache must never keep it
	if cc := res.Header.Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want private", cc)
	}
	if !bytes.Equal(got, testPNG) {
		t.Errorf("served bytes differ from the upload")
	}
}

func TestAttachmentUploadRefusesNonImage(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ack"))
	status, body := h.putRaw(h.c, []byte("just some text, not an image"), map[string]string{"X-Filename": "notes.txt"})
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("upload = %d %v, want 415", status, body)
	}
}

func TestAttachmentUploadTooLarge(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ack"))
	big := make([]byte, 5<<20+2)
	copy(big, testPNG)
	status, body := h.putRaw(h.c, big, map[string]string{"X-Filename": "big.png"})
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload = %d %v, want 413", status, body)
	}
}

func TestAttachmentServeRequiresDevice(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ack"))
	status, body := h.putRaw(h.c, testPNG, map[string]string{"X-Filename": "shot.png"})
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	id := body["id"].(string)

	unpaired := h.client()
	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/api/attachments/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := unpaired.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unpaired GET = %d, want 401", res.StatusCode)
	}
}

// TestAttachmentNotInWorktree asserts that an uploaded image lands only
// under the workspace's gitignored .gummi/attachments — never inside any
// card's worktree, since a card never gets one just by an image being
// uploaded, and the store is by design independent of any card.
func TestAttachmentNotInWorktree(t *testing.T) {
	h := newCardBoard(t, agent.NewFake("ack"))
	status, body := h.putRaw(h.c, testPNG, map[string]string{"X-Filename": "shot.png"})
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %v", status, body)
	}
	id := body["id"].(string)
	wantDir := filepath.Join(h.ws.AttachmentsDir())
	path, _, err := h.board.AttachmentPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, wantDir) {
		t.Errorf("stored at %q, want it under %q", path, wantDir)
	}
	if strings.Contains(path, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
		t.Errorf("stored path %q looks like it landed inside a card's worktree", path)
	}
}
