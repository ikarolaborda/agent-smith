package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ikarolaborda/agent-smith/web"
)

/*
The web/dist directory holds the production build of the React SPA. A
placeholder index.html is committed so the embed directive in package web
resolves even before `make web` is run. The startup banner warns when only
the placeholder is being served so the operator does not think the UI is
"broken" — they just have not run the frontend build.
*/

/* distFS aliases the embedded tree exported by package web. */
var distFS = web.Dist

/* placeholderMarker is a string found only in the committed placeholder. */
const placeholderMarker = "agent-smith placeholder UI"

/*
staticHandler returns the SPA file server. It serves files out of the
embedded dist/ tree when present, falling back to dist/index.html for any
GET/HEAD request that does not match a known asset — the standard SPA
routing pattern. POST/PUT/etc. to unknown paths return 405.

The handler also masks the embedded "dist/" prefix so the URL path matches
the on-disk layout under /web/dist.
*/
func staticHandler(logger *slog.Logger) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		logger.Error("embedded dist not available", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "embedded UI missing", http.StatusInternalServerError)
		})
	}

	if isPlaceholder(sub) {
		logger.Warn("serving placeholder web UI — run `make web` (or `cd web && npm install && npm run build`) for the real chat interface")
	}

	fileServer := http.FileServer(http.FS(sub))
	indexBytes, indexErr := fs.ReadFile(sub, "index.html")
	if indexErr != nil {
		logger.Error("embedded dist/index.html missing", "err", indexErr)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		/* never SPA-fallback the API surface */
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/healthz" {
			http.NotFound(w, r)
			return
		}

		/*
			index.html names the current content-hashed asset files, so it must
			never be cached: a stale cached copy would point at asset hashes the
			server no longer has after a rebuild, reproducing the blank page.
			Serve it through one no-cache path for both the direct "/" request
			and the SPA client-side-route fallback.
		*/
		serveIndex := func() {
			if indexErr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexBytes)
		}

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" || clean == "index.html" {
			serveIndex()
			return
		}
		if _, err := fs.Stat(sub, clean); err == nil {
			/* Build assets are content-hashed, so they are safe to cache hard. */
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		/*
			A missing asset must 404 — never fall back to index.html for it.
			Returning HTML for a .js/.css module makes the browser reject it on a
			MIME mismatch ("Expected a JavaScript module … responded with text/html")
			and blanks the whole SPA, while a 200 masks the real cause (a stale or
			incomplete embedded build). Only genuine client-side routes fall back.
		*/
		if isAssetPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		serveIndex()
	})
}

/*
isAssetPath reports whether a request path names a static build file (by
extension) rather than a client-side SPA route. Such a path must 404 when
absent instead of falling back to HTML.
*/
func isAssetPath(p string) bool {
	for _, ext := range []string{
		".js", ".mjs", ".css", ".map", ".json", ".webmanifest",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".svg", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp",
	} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

/* isPlaceholder reports whether dist/index.html is the committed placeholder. */
func isPlaceholder(sub fs.FS) bool {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return false
	}
	return strings.Contains(string(b), placeholderMarker)
}
