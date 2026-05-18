/*
Package logging configures the application's slog.Logger. It supports the
two stdlib handlers (text and JSON) and the four standard levels.
*/
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

/*
Options control logger construction. Empty fields fall back to sensible
defaults (text format, info level, stderr writer).
*/
type Options struct {
	Format string
	Level  string
	Writer io.Writer
}

/*
New builds a slog.Logger from opts. Format may be "text" or "json"; Level may
be "debug", "info", "warn", or "error" (case-insensitive). Unknown values
fall back to defaults rather than returning an error so logging is always
available even when config is partially broken.
*/
func New(opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{Level: parseLevel(opts.Level)}

	var handler slog.Handler
	switch strings.ToLower(opts.Format) {
	case "json":
		handler = slog.NewJSONHandler(w, handlerOpts)
	default:
		handler = slog.NewTextHandler(w, handlerOpts)
	}
	return slog.New(handler)
}

/* parseLevel maps human level names onto slog.Level, defaulting to info. */
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
