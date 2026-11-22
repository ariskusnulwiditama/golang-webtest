// Package logger builds the structured logger used by every process.
//
// JSON is the default because the output is meant to be shipped to an aggregator;
// text is available for local console work.
package logger

import (
	"io"
	"log/slog"

	"github.com/ariskusnulwiditama/golang-webtest/internal/platform/config"
)

// New returns a logger that writes to w and tags every record with the service
// name, so api and worker logs stay distinguishable when collected together.
func New(cfg config.Log, service string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     cfg.Level,
		AddSource: cfg.Source,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Normalise timestamps to UTC so records from different machines
			// line up when aggregated.
			if a.Key == slog.TimeKey && len(groups) == 0 {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	}

	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler).With(slog.String("service", service))
}
