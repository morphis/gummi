package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// The page ships inside the binary, so `gummi web` needs nothing on disk
// and no network to serve the board. There is no build step: the page is
// plain ES modules and a stylesheet.
//
//go:embed assets
var assetsFS embed.FS

// Cache-Control for the two kinds of thing this server hands out: the
// page, which is never cached, and the assets beside it, which revalidate.
// Neither may be served stale — both change when the binary does, and a
// phone holding yesterday's app.js against today's server is a bug report
// nobody can reproduce. The ETag makes revalidation cheap.
const (
	noCache    = "no-store"
	assetCache = "no-cache"
)

// assetVersionToken is replaced, in every file that carries it, by a hash
// of everything this binary ships.
//
// It exists because Cache-Control alone cannot save a browser that already
// cached the old file: the fix that matters is the one that reaches a
// phone which loaded the page five minutes ago, and only a changed URL
// guarantees that. The page itself is never cached, so the versioned links
// it carries are always this binary's. A module importing another writes
// `import … from './x.js?v=__ASSET_V__'` for the same reason.
const assetVersionToken = "__ASSET_V__" //nolint:gosec // a placeholder in the page text, not a credential

// asset is one prepared file: its bytes, its type, and a validator over
// the content.
type asset struct {
	body  []byte
	etag  string
	ctype string
}

func loadAssets() (map[string]asset, error) {
	page, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("web assets are missing from the binary: %w", err)
	}
	return buildAssets(page)
}

// buildAssets reads every embedded file once, stamps the version into the
// ones that link to others, and precomputes each one's validator.
func buildAssets(page fs.FS) (map[string]asset, error) {
	raw := map[string][]byte{}
	err := fs.WalkDir(page, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(page, path)
		if err != nil {
			return err
		}
		raw[path] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("no assets are embedded in this binary")
	}

	version := assetVersion(raw)
	out := make(map[string]asset, len(raw))
	for name, b := range raw {
		if bytes.Contains(b, []byte(assetVersionToken)) {
			b = bytes.ReplaceAll(b, []byte(assetVersionToken), []byte(version))
		}
		out[name] = asset{body: b, etag: etagFor(b), ctype: contentType(name)}
	}
	return out, nil
}

// assetVersion hashes every file, name included, so any change to what
// this binary serves changes every versioned link.
func assetVersion(raw map[string][]byte) string {
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write(raw[name])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// assetRoutes serves the page, the files beside it, and the two that must
// sit at the root: the manifest, and the service worker, whose scope is
// the directory it is served from.
func (s *Server) assetRoutes() {
	s.mux.Handle("GET /", s.serveOne("/", "index.html"))
	s.mux.Handle("GET /manifest.webmanifest", s.serveOne("/manifest.webmanifest", "manifest.webmanifest"))
	s.mux.Handle("GET /sw.js", s.serveOne("/sw.js", "sw.js"))
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", s.serveTree()))
}

// serveOne serves a named file at exactly one route. The exact-match check
// matters for "/", which Go's mux treats as a catch-all: without it every
// unknown path would answer with the page instead of a 404.
func (s *Server) serveOne(route, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != route {
			http.NotFound(w, r)
			return
		}
		s.writeAsset(w, r, name, noCache)
	})
}

// serveTree serves the asset directory — files only, never a listing.
func (s *Server) serveTree() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.writeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), assetCache)
	})
}

func (s *Server) writeAsset(w http.ResponseWriter, r *http.Request, name, cache string) {
	a, ok := s.assets[name]
	if !ok || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.ctype)
	w.Header().Set("Cache-Control", cache)
	// The content is the version: an embedded file has no useful mod time,
	// so ServeContent has nothing to answer a conditional request with
	// unless we hand it one.
	w.Header().Set("ETag", a.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.body))
}

// etagFor is a strong validator over the bytes themselves.
func etagFor(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// contentType names the types the page actually serves. mime's table does
// not carry .mjs or .webmanifest everywhere, and with nosniff set a wrong
// guess is a blank page rather than a warning.
func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".mjs"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".webmanifest"):
		return "application/manifest+json"
	case strings.HasSuffix(name, ".md"), strings.HasSuffix(name, ".txt"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// secureHeaders locks the page down to exactly what it needs: its own
// scripts, styles and endpoints, and nothing embedded, framed, or
// referred anywhere.
//
// Neither script-src nor style-src allows 'unsafe-inline': the page has
// no inline script and no style attribute in its markup. Styling set from
// a script (element.style) is not an inline style and is unaffected.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; "+
				"img-src 'self' data:; font-src 'self'; connect-src 'self'; "+
				"manifest-src 'self'; worker-src 'self'; base-uri 'none'; "+
				"form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil && hstsHost(r.Host) {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

// hstsValue keeps a browser that reached the board over HTTPS from being
// talked down to HTTP for half a year. It covers the one name only, not
// its subdomains.
const hstsValue = "max-age=15552000"

// hstsHost reports whether a Host may be pinned to HTTPS. Browsers ignore
// the header for an address, and pinning localhost would reach every
// other server on this machine's loopback, whatever its port.
func hstsHost(host string) bool {
	h := hostName(host)
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	_, err := netip.ParseAddr(h)
	return err != nil
}
