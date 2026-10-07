package attachment

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a minimal 1x1 PNG.
var onePxPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestPutSniffsAndDedupes(t *testing.T) {
	s := &Store{Dir: t.TempDir()}

	ref1, err := s.Put(bytes.NewReader(onePxPNG), "shot-1.png")
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if ref1.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", ref1.MediaType)
	}
	if ref1.Size != int64(len(onePxPNG)) {
		t.Errorf("Size = %d, want %d", ref1.Size, len(onePxPNG))
	}

	ref2, err := s.Put(bytes.NewReader(onePxPNG), "shot-2.png")
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if ref2.ID != ref1.ID {
		t.Errorf("second Put id = %q, want %q (same bytes)", ref2.ID, ref1.ID)
	}
	if ref2.Name != "shot-2.png" {
		t.Errorf("second Put Name = %q, want %q", ref2.Name, "shot-2.png")
	}
	if got, err := s.Get(ref1.ID); err != nil || got.Name != "shot-2.png" {
		t.Errorf("Get after second Put = %q, %v, want name %q (last upload wins)", got.Name, err, "shot-2.png")
	}

	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("store dir has %d entries, want 2 (image + name sidecar): %v", len(entries), entries)
	}
	names := []string{entries[0].Name(), entries[1].Name()}
	if (names[0] != ref1.ID+".png" || names[1] != ref1.ID+".png.name") &&
		(names[1] != ref1.ID+".png" || names[0] != ref1.ID+".png.name") {
		t.Errorf("stored entries = %v, want %q and %q", names, ref1.ID+".png", ref1.ID+".png.name")
	}
}

func TestPutRefusesNonImage(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	_, err := s.Put(bytes.NewReader([]byte("just some text, not an image")), "notes.txt")
	if !errors.Is(err, ErrNotImage) {
		t.Fatalf("err = %v, want ErrNotImage", err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 0 {
		t.Errorf("refused upload wrote %d files, want 0", len(entries))
	}
}

func TestPutRefusesTooLarge(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	big := make([]byte, MaxSize+1)
	copy(big, onePxPNG)
	_, err := s.Put(bytes.NewReader(big), "huge.png")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestResolveRefusesTooManyAndUnknown(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	ref, err := s.Put(bytes.NewReader(onePxPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Resolve([]string{ref.ID}); err != nil {
		t.Fatalf("Resolve known id: %v", err)
	}

	ids := make([]string, MaxPerRequest+1)
	for i := range ids {
		ids[i] = ref.ID
	}
	if _, err := s.Resolve(ids); !errors.Is(err, ErrTooMany) {
		t.Fatalf("err = %v, want ErrTooMany", err)
	}

	if _, err := s.Resolve([]string{"0000000000000000000000000000000000000000000000000000000000000000"}); err == nil {
		t.Fatal("want error for malformed id")
	}
	unknown := "ff" + ref.ID[2:]
	if _, err := s.Resolve([]string{unknown}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}

func TestResolveRecoversUploadedName(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	put, err := s.Put(bytes.NewReader(onePxPNG), "screenshot-of-bug.png")
	if err != nil {
		t.Fatal(err)
	}

	refs, err := s.Resolve([]string{put.ID})
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].Name != "screenshot-of-bug.png" {
		t.Errorf("Resolve name = %q, want %q", refs[0].Name, "screenshot-of-bug.png")
	}

	got, err := s.Get(put.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "screenshot-of-bug.png" {
		t.Errorf("Get name = %q, want %q", got.Name, "screenshot-of-bug.png")
	}
}

func TestResolveFallsBackToFilenameWithoutSidecar(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	put, err := s.Put(bytes.NewReader(onePxPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an id stored before the name sidecar existed.
	if err := os.Remove(filepath.Join(s.Dir, put.ID+".png.name")); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(put.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != put.ID+".png" {
		t.Errorf("Get name = %q, want fallback %q", got.Name, put.ID+".png")
	}
}

func TestRefsFindsLinksInOrder(t *testing.T) {
	a := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"[:64]
	b := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"[:64]
	text := "See ![first](.gummi/attachments/" + a + ".png) and ![again](.gummi/attachments/" + a + ".png) then ![second](.gummi/attachments/" + b + ".jpg)."

	refs := Refs(text)
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2 (deduped): %+v", len(refs), refs)
	}
	if refs[0].ID != a || refs[1].ID != b {
		t.Errorf("refs = %+v, want ids %q then %q", refs, a, b)
	}
}

func TestLinkRendersStorePath(t *testing.T) {
	ref := Ref{ID: "abc", Name: "shot.png", MediaType: "image/png"}
	got := Link(ref)
	want := "![shot.png](.gummi/attachments/abc.png)"
	if got != want {
		t.Errorf("Link = %q, want %q", got, want)
	}
}

func TestLinkFallsBackToFilenameWhenNameBreaksGrammar(t *testing.T) {
	for _, name := range []string{"shot]one.png", "shot\none.png"} {
		t.Run(name, func(t *testing.T) {
			ref := Ref{ID: strings.Repeat("a", 64), Name: name, MediaType: "image/png"}
			got := Link(ref)
			want := "![" + ref.ID + ".png](.gummi/attachments/" + ref.ID + ".png)"
			if got != want {
				t.Errorf("Link = %q, want %q", got, want)
			}
			if refs := Refs(got); len(refs) != 1 || refs[0].ID != ref.ID {
				t.Errorf("Refs(Link(ref)) = %+v, want one ref with id %q", refs, ref.ID)
			}
		})
	}
}

func TestPathValidatesID(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	ref, err := s.Put(bytes.NewReader(onePxPNG), "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Path(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(s.Dir, ref.ID+".png") {
		t.Errorf("Path = %q", p)
	}
	if _, err := s.Path("not-an-id"); !errors.Is(err, ErrUnknown) {
		t.Errorf("err = %v, want ErrUnknown", err)
	}
}
