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

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(sub, clean); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		/* SPA fallback to index.html */
		if indexErr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexBytes)
	})
}

/* isPlaceholder reports whether dist/index.html is the committed placeholder. */
func isPlaceholder(sub fs.FS) bool {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return false
	}
	return strings.Contains(string(b), placeholderMarker)
}
