/*
Package web exposes the built React SPA as an embedded file system. The
production bundle lives under web/dist and is committed only as a small
placeholder until `make web` (or `cd web && npm install && npm run build`)
populates it with the real assets. The embedded FS is consumed by
internal/server when --serve is set.
*/
package web

import "embed"

/*
Dist is the embedded /web/dist tree. The placeholder index.html ensures the
embed directive resolves on a fresh checkout; replace it by running the
frontend build.
*/
//go:embed all:dist
var Dist embed.FS
