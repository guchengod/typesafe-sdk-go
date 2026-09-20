package typesafe

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// ParseLogLevel converts a level name into a [slog.Level]. It accepts "debug", "info", "warn",
// "warning", "error", and "off", and reports false for any other value.
func ParseLogLevel(level string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	case "off":
		return offLevel, true
	default:
		return slog.LevelInfo, false
	}
}

// offLevel is above every level slog records, so a logger set to it emits nothing.
const offLevel = slog.Level(100)

// discardHandler records nothing and reports every level as disabled.
//
// Reporting the level as disabled matters for more than tidiness: the SDK guards its logging with
// Enabled before building attributes, so a logger that claims to be enabled would make every
// request pay for records that are then thrown away.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// defaultLogger returns the logger used when neither [WithLogger] nor TYPESAFE_LOG_LEVEL is set.
//
// The SDK never logs by default: without configuration it discards records, which is what library
// code should do in Go. Setting TYPESAFE_LOG_LEVEL to a level name sends records to stderr.
func defaultLogger() *slog.Logger {
	level, ok := ParseLogLevel(os.Getenv(LogLevelEnv))
	if !ok || level >= offLevel {
		return slog.New(discardHandler{})
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// isSecretHeader reports whether a header name carries a credential and must be redacted.
//
// It matches the well-known credential headers and, in addition, any header whose name mentions
// "token" or "secret", so an unfamiliar credential header is redacted rather than logged.
func isSecretHeader(name string) bool {
	lowered := strings.ToLower(strings.TrimSpace(name))
	if _, known := secretHeaders[lowered]; known {
		return true
	}
	return strings.Contains(lowered, "token") || strings.Contains(lowered, "secret")
}

// redactHeaders returns a copy of headers with every credential-bearing value replaced by "***".
// It is the single redaction point for the SDK's own log records.
func redactHeaders(headers map[string][]string) map[string]string {
	if headers == nil {
		return nil
	}
	redacted := make(map[string]string, len(headers))
	for name, values := range headers {
		if isSecretHeader(name) {
			redacted[name] = "***"
			continue
		}
		redacted[name] = strings.Join(values, ", ")
	}
	return redacted
}

// RedactingHandler wraps an [slog.Handler] and redacts credential-bearing attributes before they
// reach it.
//
// The SDK already redacts the headers it logs itself; this handler extends that guarantee to
// records the caller routes through the SDK's logger:
//
//	logger := slog.New(typesafe.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, nil)))
//	client, err := typesafe.NewClient(typesafe.WithLogger(logger))
//
// Values held in a map[string]string under any attribute are inspected key by key.
type RedactingHandler struct {
	handler slog.Handler
}

// NewRedactingHandler wraps handler so that credential-bearing attributes are redacted.
func NewRedactingHandler(handler slog.Handler) *RedactingHandler {
	return &RedactingHandler{handler: handler}
}

// Enabled reports whether the wrapped handler records the level.
func (r *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return r.handler.Enabled(ctx, level)
}

// Handle redacts the record's attributes and forwards it to the wrapped handler. Attributes that
// carry no credential are passed through unchanged.
func (r *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	changed := false
	record.Attrs(func(attr slog.Attr) bool {
		if replacement, ok := redactAttr(attr); ok {
			attr, changed = replacement, true
		}
		attrs = append(attrs, attr)
		return true
	})
	if !changed {
		return r.handler.Handle(ctx, record)
	}
	// Only rebuild the record when something actually needed redaction.
	clone := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	clone.AddAttrs(attrs...)
	return r.handler.Handle(ctx, clone)
}

// redactAttr returns the redacted form of an attribute and whether it changed.
func redactAttr(attr slog.Attr) (slog.Attr, bool) {
	if isSecretHeader(attr.Key) {
		return slog.String(attr.Key, "***"), true
	}
	switch values := attr.Value.Any().(type) {
	case map[string]string:
		changed := false
		redacted := make(map[string]string, len(values))
		for name, value := range values {
			if isSecretHeader(name) {
				redacted[name] = "***"
				changed = true
				continue
			}
			redacted[name] = value
		}
		if !changed {
			return attr, false
		}
		return slog.Any(attr.Key, redacted), true
	case http.Header:
		changed := false
		redacted := make(http.Header, len(values))
		for name, vals := range values {
			if isSecretHeader(name) {
				redacted[name] = []string{"***"}
				changed = true
				continue
			}
			redacted[name] = vals
		}
		if !changed {
			return attr, false
		}
		return slog.Any(attr.Key, redacted), true
	case map[string][]string:
		changed := false
		redacted := make(map[string][]string, len(values))
		for name, vals := range values {
			if isSecretHeader(name) {
				redacted[name] = []string{"***"}
				changed = true
				continue
			}
			redacted[name] = vals
		}
		if !changed {
			return attr, false
		}
		return slog.Any(attr.Key, redacted), true
	case map[string]any:
		changed := false
		redacted := make(map[string]any, len(values))
		for name, val := range values {
			if isSecretHeader(name) {
				redacted[name] = "***"
				changed = true
				continue
			}
			redacted[name] = val
		}
		if !changed {
			return attr, false
		}
		return slog.Any(attr.Key, redacted), true
	default:
		return attr, false
	}
}

// WithAttrs returns a handler whose records carry the given attributes.
func (r *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if replacement, ok := redactAttr(attr); ok {
			attr = replacement
		}
		redacted = append(redacted, attr)
	}
	return &RedactingHandler{handler: r.handler.WithAttrs(redacted)}
}

// WithGroup returns a handler that groups subsequent attributes under name.
func (r *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{handler: r.handler.WithGroup(name)}
}
