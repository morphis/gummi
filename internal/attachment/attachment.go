// Package attachment is the content-addressed store for images a person
// attaches through the web page — a card description, a spec note, or a
// thread turn. Every accepted image is written once under a workspace's
// gitignored .gummi/attachments, keyed by the hex sha256 of its bytes, so
// the same screenshot uploaded twice (or named twice, from the composer
// and a spec note) lands as one file with one id.
package attachment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/atomicfile"
)

// MaxSize is the largest accepted image, in bytes.
const MaxSize = 5 * 1024 * 1024

// MaxPerRequest is the most attachment ids Resolve accepts in one
// message, spec note, or card description.
const MaxPerRequest = 8

var (
	// ErrNotImage is Put's refusal for bytes that don't sniff as one of
	// the accepted media types.
	ErrNotImage = errors.New("not an accepted image type")
	// ErrTooLarge is Put's refusal for bytes over MaxSize.
	ErrTooLarge = errors.New("image too large")
	// ErrTooMany is Resolve's refusal for more than MaxPerRequest ids.
	ErrTooMany = errors.New("too many attachments")
	// ErrUnknown is Resolve's refusal for an id not present in the store.
	ErrUnknown = errors.New("unknown attachment")
)

// extByMediaType are the only media types Put accepts, and the extension
// each is stored under.
var extByMediaType = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// linkPattern matches the markdown image-link grammar a spec-anchored
// attachment uses: ![name](.gummi/attachments/<sha256>.<ext>). Capture
// group 1 is the id.
var linkPattern = regexp.MustCompile(`!\[[^\]]*\]\(\.gummi/attachments/([0-9a-f]{64})\.[A-Za-z0-9]+\)`)

// Ref is one stored image: its content-addressed id, the name it was
// uploaded under, its sniffed media type, and its size in bytes. Put
// writes the uploaded name into a sidecar file (<id>.<ext>.name) next to
// the image bytes, so Get, Resolve and stat recover the original name too;
// only an id stored before that sidecar existed falls back to the stored
// filename.
type Ref struct {
	ID        string
	Name      string
	MediaType string
	Size      int64
}

// Store is a workspace's attachment directory, rooted under its
// gitignored .gummi so images never land in a card's worktree or branch.
type Store struct {
	// Dir is the store's root, e.g. <workspace>/.gummi/attachments.
	Dir string
}

// ext returns the filename ref is stored under.
func (r Ref) filename() string {
	return fmt.Sprintf("%s.%s", r.ID, extByMediaType[r.MediaType])
}

// nameSidecar returns the path of the file that holds the name a stored
// image was uploaded under, alongside its bytes.
func (s *Store) nameSidecar(filename string) string {
	return filepath.Join(s.Dir, filename+".name")
}

// writeSidecar records name as the most recent name ref was uploaded under,
// overwriting any name a prior Put recorded for the same id — including when
// this call's bytes deduplicated against an id already on disk, so the
// last upload's filename always wins over an earlier one's.
func (s *Store) writeSidecar(ref Ref, name string) error {
	if name == "" {
		return nil
	}
	return atomicfile.Write(s.nameSidecar(ref.filename()), []byte(name), 0o644)
}

// Put reads r (refusing anything over MaxSize), sniffs its media type, and
// writes it content-addressed by sha256 under the store's directory if not
// already present. name is the caller-supplied filename; it plays no part
// in the id, but every call — including one that dedups against bytes
// already on disk — persists it to a sidecar file so later lookups (Get,
// Resolve, stat) recover the most recently uploaded name, not necessarily
// the first.
func (s *Store) Put(r io.Reader, name string) (Ref, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return Ref{}, err
	}
	if len(data) > MaxSize {
		return Ref{}, ErrTooLarge
	}
	mediaType := http.DetectContentType(data)
	if _, ok := extByMediaType[mediaType]; !ok {
		return Ref{}, ErrNotImage
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	ref := Ref{ID: id, Name: name, MediaType: mediaType, Size: int64(len(data))}

	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return Ref{}, err
	}
	path := filepath.Join(s.Dir, ref.filename())
	if _, err := os.Stat(path); err == nil {
		return ref, s.writeSidecar(ref, name) // already stored under this id; still record this call's name
	} else if !os.IsNotExist(err) {
		return Ref{}, err
	}
	if err := atomicfile.Write(path, data, 0o644); err != nil {
		return Ref{}, err
	}
	return ref, s.writeSidecar(ref, name)
}

// Path validates id (64 lowercase hex) and returns the absolute path of
// the stored file, searching for its extension on disk since the id alone
// doesn't carry the media type.
func (s *Store) Path(id string) (string, error) {
	ref, err := s.stat(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, ref.filename()), nil
}

// Get returns the Ref for a stored id, without reading its bytes.
func (s *Store) Get(id string) (Ref, error) {
	return s.stat(id)
}

// stat validates id and finds the one file on disk stored under it, across
// every accepted extension, reporting Name from its sidecar file when one
// was written at upload time, falling back to the stored filename for an
// id stored before the sidecar existed.
func (s *Store) stat(id string) (Ref, error) {
	if !idPattern.MatchString(id) {
		return Ref{}, fmt.Errorf("%w: %q", ErrUnknown, id)
	}
	for mediaType, ext := range extByMediaType {
		filename := id + "." + ext
		path := filepath.Join(s.Dir, filename)
		fi, err := os.Stat(path)
		if err == nil {
			name := filename
			if data, err := os.ReadFile(s.nameSidecar(filename)); err == nil && len(data) > 0 {
				name = string(data)
			}
			return Ref{ID: id, Name: name, MediaType: mediaType, Size: fi.Size()}, nil
		}
	}
	return Ref{}, fmt.Errorf("%w: %q", ErrUnknown, id)
}

// Resolve looks up every id, in order, refusing more than MaxPerRequest
// or any id not present in the store. It is the one gate every
// send/create/note path uses before accepting attachment ids from a
// request.
func (s *Store) Resolve(ids []string) ([]Ref, error) {
	if len(ids) > MaxPerRequest {
		return nil, fmt.Errorf("%w: %d ids, max %d", ErrTooMany, len(ids), MaxPerRequest)
	}
	refs := make([]Ref, 0, len(ids))
	for _, id := range ids {
		ref, err := s.stat(id)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// Link renders the markdown image-link grammar a spec (a card description,
// a spec note) uses to anchor ref: ![name](.gummi/attachments/<id>.<ext>).
// A name containing ']' or a newline would break that grammar's own
// alt-text capture (here and in markdown.js's matching regex, which also
// excludes '\n') and silently fail to round-trip through Refs or render as
// an image in the page, so such a name falls back to the id-based
// filename — the same fallback already used for a pre-sidecar id.
func Link(ref Ref) string {
	ext := extByMediaType[ref.MediaType]
	name := ref.Name
	if name == "" || strings.ContainsAny(name, "]\n") {
		name = ref.ID + "." + ext
	}
	return fmt.Sprintf("![%s](.gummi/attachments/%s.%s)", name, ref.ID, ext)
}

// Refs scans text for the Link grammar, in document order, deduplicated by
// id. A later reader (Resolve, or a caller wanting Ref.Name/MediaType) is
// expected to look the id up in the store; Refs itself reports only what
// the link names.
func Refs(text string) []Ref {
	matches := linkPattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	refs := make([]Ref, 0, len(matches))
	for _, m := range matches {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		refs = append(refs, Ref{ID: id})
	}
	return refs
}
