package typesafe

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
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

// isSecretHeader reports whether a header or attribute name carries a credential and must be redacted.
//
// It matches the well-known credential headers and, in addition, any name mentioning common
// credential terms ("token", "secret", "apikey", "password", "privatekey", "credential",
// "authkey", "subscriptionkey", "accesskey", "bearer"), regardless of separator style.
func isSecretHeader(name string) bool {
	// Zero-allocation stack path for ASCII header and attribute names up to 64 bytes.
	if trimmed, ok := trimShortASCII(name); ok {
		if trimmed == "" {
			return false
		}
		var lowerBuf [64]byte
		var normBuf [64]byte
		normLen := 0
		for i := range len(trimmed) {
			c := trimmed[i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			lowerBuf[i] = c
			if c != '-' && c != '_' {
				normBuf[normLen] = c
				normLen++
			}
		}
		if _, known := secretHeaders[string(lowerBuf[:len(trimmed)])]; known {
			return true
		}
		if normLen < 5 {
			return false
		}
		norm := normBuf[:normLen]
		for _, marker := range secretKeyMarkerBytes {
			if bytes.Contains(norm, marker) {
				return true
			}
		}
		return false
	}

	lowered := strings.ToLower(strings.TrimSpace(name))
	if lowered == "" {
		return false
	}
	if _, known := secretHeaders[lowered]; known {
		return true
	}
	normalized := stripKeySeparators(lowered)
	for _, marker := range secretKeyMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func trimShortASCII(s string) (string, bool) {
	start := 0
	for start < len(s) && isASCIISpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isASCIISpace(s[end-1]) {
		end--
	}
	trimmed := s[start:end]
	if len(trimmed) > 64 {
		return "", false
	}
	for i := range len(trimmed) {
		if trimmed[i] >= 0x80 {
			return "", false
		}
	}
	return trimmed, true
}

func isASCIISpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}

var secretKeyMarkers = []string{
	"token",
	"secret",
	"apikey",
	"password",
	"passwd",
	"passphrase",
	"privatekey",
	"credential",
	"authkey",
	"subscriptionkey",
	"accesskey",
	"clientkey",
	"bearer",
	"wwwauthenticate",
	"proxyauthenticate",
}

var secretKeyMarkerBytes = func() [][]byte {
	out := make([][]byte, len(secretKeyMarkers))
	for i, m := range secretKeyMarkers {
		out[i] = []byte(m)
	}
	return out
}()

func stripKeySeparators(s string) string {
	if !strings.ContainsAny(s, "-_") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		if s[i] != '-' && s[i] != '_' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
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
	if handler == nil {
		handler = discardHandler{}
	}
	return &RedactingHandler{handler: handler}
}

// Enabled reports whether the wrapped handler records the level.
func (r *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if r == nil || r.handler == nil {
		return false
	}
	return r.handler.Enabled(ctx, level)
}

// Handle redacts the record's attributes and forwards it to the wrapped handler. Attributes that
// carry no credential are passed through unchanged without allocating a replacement slice.
func (r *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	if r == nil || r.handler == nil {
		return nil
	}
	if record.NumAttrs() == 0 {
		return r.handler.Handle(ctx, record)
	}
	firstChanged := -1
	var firstReplacement slog.Attr
	idx := 0
	record.Attrs(func(attr slog.Attr) bool {
		if replacement, ok := redactAttr(attr); ok {
			firstChanged = idx
			firstReplacement = replacement
			return false
		}
		idx++
		return true
	})
	if firstChanged < 0 {
		return r.handler.Handle(ctx, record)
	}
	// Rebuild the record only when an attribute needed redaction, preserving already-checked prefix attrs.
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	idx = 0
	record.Attrs(func(attr slog.Attr) bool {
		switch {
		case idx < firstChanged:
			attrs = append(attrs, attr)
		case idx == firstChanged:
			attrs = append(attrs, firstReplacement)
		default:
			if replacement, ok := redactAttr(attr); ok {
				attr = replacement
			}
			attrs = append(attrs, attr)
		}
		idx++
		return true
	})
	clone := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	clone.AddAttrs(attrs...)
	return r.handler.Handle(ctx, clone)
}

const maxRedactDepth = 16

// redactAttr returns the redacted form of an attribute and whether it changed.
func redactAttr(attr slog.Attr) (slog.Attr, bool) {
	return redactAttrDepth(attr, 0)
}

func redactAttrDepth(attr slog.Attr, depth int) (slog.Attr, bool) {
	if isSecretHeader(attr.Key) {
		return slog.String(attr.Key, "***"), true
	}
	if depth >= maxRedactDepth {
		return slog.String(attr.Key, "[max-depth]"), true
	}
	wasLogValuer := attr.Value.Kind() == slog.KindLogValuer
	val := attr.Value.Resolve()
	if val.Kind() == slog.KindGroup {
		group := val.Group()
		var redacted []slog.Attr
		for i, sub := range group {
			if replacement, ok := redactAttrDepth(sub, depth+1); ok {
				if redacted == nil {
					redacted = slices.Clone(group)
				}
				redacted[i] = replacement
			}
		}
		if redacted == nil {
			if !wasLogValuer {
				return attr, false
			}
			return slog.Attr{Key: attr.Key, Value: val}, true
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(redacted...)}, true
	}
	if val.Kind() == slog.KindAny {
		if redactedVal, ok := redactAnyValue(val.Any(), depth+1); ok {
			return slog.Attr{Key: attr.Key, Value: slog.AnyValue(redactedVal)}, true
		}
	}
	if wasLogValuer {
		return slog.Attr{Key: attr.Key, Value: val}, true
	}
	return attr, false
}

func redactAnyValue(raw any, depth int) (any, bool) {
	if depth > maxRedactDepth {
		return "[max-depth]", true
	}
	switch values := raw.(type) {
	case map[string]string:
		var redacted map[string]string
		for name := range values {
			if isSecretHeader(name) {
				if redacted == nil {
					redacted = maps.Clone(values)
				}
				redacted[name] = "***"
			}
		}
		if redacted == nil {
			return raw, false
		}
		return redacted, true
	case http.Header:
		var redacted http.Header
		for name := range values {
			if isSecretHeader(name) {
				if redacted == nil {
					redacted = maps.Clone(values)
				}
				redacted[name] = []string{"***"}
			}
		}
		if redacted == nil {
			return raw, false
		}
		return redacted, true
	case map[string][]string:
		var redacted map[string][]string
		for name := range values {
			if isSecretHeader(name) {
				if redacted == nil {
					redacted = maps.Clone(values)
				}
				redacted[name] = []string{"***"}
			}
		}
		if redacted == nil {
			return raw, false
		}
		return redacted, true
	case map[string]any:
		var redacted map[string]any
		for name, val := range values {
			if isSecretHeader(name) {
				if redacted == nil {
					redacted = maps.Clone(values)
				}
				redacted[name] = "***"
				continue
			}
			if nested, ok := redactAnyValue(val, depth+1); ok {
				if redacted == nil {
					redacted = maps.Clone(values)
				}
				redacted[name] = nested
			}
		}
		if redacted == nil {
			return raw, false
		}
		return redacted, true
	case []any:
		var redacted []any
		for i, item := range values {
			if nested, ok := redactAnyValue(item, depth+1); ok {
				if redacted == nil {
					redacted = slices.Clone(values)
				}
				redacted[i] = nested
			}
		}
		if redacted == nil {
			return raw, false
		}
		return redacted, true
	default:
		return raw, false
	}
}

// WithAttrs returns a handler whose records carry the given attributes.
func (r *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if r == nil || r.handler == nil {
		return NewRedactingHandler(nil)
	}
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
	if r == nil || r.handler == nil {
		return NewRedactingHandler(nil)
	}
	return &RedactingHandler{handler: r.handler.WithGroup(name)}
}
