package typesafe

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

var (
	benchSinkBool        bool
	benchSinkRedactedMap map[string]string
)

// BenchmarkIsSecretHeader measures header/attribute name secret classification across known exact
// matches, benign headers, and separator-normalized marker matches.
func BenchmarkIsSecretHeader(b *testing.B) {
	b.Run("KnownSecret", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSinkBool = isSecretHeader("Authorization")
		}
	})

	b.Run("CleanHeader", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSinkBool = isSecretHeader("Content-Type")
		}
	})

	b.Run("NormalizedMarker", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSinkBool = isSecretHeader("X-Custom-Api_Key")
		}
	})
}

// BenchmarkRedactHeaders measures wire-header map redaction for a typical 8-header HTTP exchange.
func BenchmarkRedactHeaders(b *testing.B) {
	headers := http.Header{
		"Accept":                {ContentTypeJSON},
		"Authorization":         {"Bearer ts_live_secret_benchmark_token"},
		"Content-Type":          {ContentTypeJSON},
		"Set-Cookie":            {"__cf_bm=secret_cookie_value; Secure; HttpOnly"},
		"User-Agent":            {sdkUserAgent()},
		"X-Typesafe-Request-Id": {"req_bench_123456"},
		"X-Typesafe-Runtime":    {RuntimeString()},
		"X-Typesafe-Sdk":        {sdkUserAgent()},
	}
	b.ReportAllocs()
	for b.Loop() {
		benchSinkRedactedMap = redactHeaders(headers)
	}
}

// BenchmarkRedactingHandler measures slog record inspection and redaction across clean pass-through
// records, top-level secret attributes, nested groups/maps/LogValuers, and full wire-debug calls.
func BenchmarkRedactingHandler(b *testing.B) {
	ctx := b.Context()
	handler := NewRedactingHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	now := time.Now()

	b.Run("PassThroughClean", func(b *testing.B) {
		rec := slog.NewRecord(now, slog.LevelInfo, "response", 0)
		rec.AddAttrs(
			slog.String("method", http.MethodPost),
			slog.String("url", DefaultBaseURL+SystemOnePath),
			slog.Int("status", http.StatusOK),
			slog.Int64("elapsed_ms", 240),
			slog.String("request_id", "req_bench_clean"),
		)
		b.ReportAllocs()
		for b.Loop() {
			if err := handler.Handle(ctx, rec); err != nil {
				b.Fatalf("Handle(PassThroughClean) error = %v", err)
			}
		}
	})

	b.Run("TopLevelSecret", func(b *testing.B) {
		rec := slog.NewRecord(now, slog.LevelInfo, "auth.check", 0)
		rec.AddAttrs(
			slog.String("endpoint", SystemOnePath),
			slog.String("api_key", "bench-key"),
			slog.Int("attempt", 1),
		)
		b.ReportAllocs()
		for b.Loop() {
			if err := handler.Handle(ctx, rec); err != nil {
				b.Fatalf("Handle(TopLevelSecret) error = %v", err)
			}
		}
	})

	b.Run("NestedGroupAndMap", func(b *testing.B) {
		cfg := &Config{
			APIKey:       "bench-key",
			BaseURL:      DefaultBaseURL,
			DefaultModel: DefaultModel,
			Timeout:      DefaultTimeout,
		}
		rec := slog.NewRecord(now, slog.LevelDebug, "diagnostic", 0)
		rec.AddAttrs(
			slog.Any("config", cfg),
			slog.Group("request",
				slog.String("method", http.MethodPost),
				slog.Any("meta", map[string]any{
					"trace_id":      "tr-123",
					"authorization": "Bearer bench-val",
				}),
			),
		)
		b.ReportAllocs()
		for b.Loop() {
			if err := handler.Handle(ctx, rec); err != nil {
				b.Fatalf("Handle(NestedGroupAndMap) error = %v", err)
			}
		}
	})

	b.Run("ClientSystemOneDebugWire", func(b *testing.B) {
		transport := &mockTransport{handler: func(*http.Request, int) *http.Response {
			return JSONResponse(http.StatusOK, fixedAnswerResponse)
		}}
		debugLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
		client, err := NewClient(
			WithAPIKey("bench-key"),
			WithTransport(transport),
			WithLogger(debugLogger),
			WithRetryPolicy(noRetryPolicy()),
		)
		if err != nil {
			b.Fatalf("NewClient() error = %v", err)
		}
		b.Cleanup(func() { _ = client.Close() })
		questions := fixedQuestions()
		state := map[string]any{"message": "I was charged twice. Please help."}

		b.ReportAllocs()
		b.SetBytes(int64(len(fixedAnswerResponse)))
		for iteration := 0; b.Loop(); iteration++ {
			result, err := client.SystemOne(ctx, state, questions)
			if err != nil {
				b.Fatalf("SystemOne() error = %v", err)
			}
			benchSinkResponse = result
			if iteration%1024 == 1023 {
				transport.mu.Lock()
				transport.calls = transport.calls[:0]
				transport.mu.Unlock()
			}
		}
	})
}

// BenchmarkCleanURL measures URL credential and query-string scrubbing used on transport logs and errors.
func BenchmarkCleanURL(b *testing.B) {
	b.Run("CleanHTTPS", func(b *testing.B) {
		raw := DefaultBaseURL + SystemOnePath
		b.ReportAllocs()
		for b.Loop() {
			benchSinkString = cleanURL(raw)
		}
	})

	b.Run("WithUserinfoAndQuery", func(b *testing.B) {
		raw := "https://user:p@ssword@api.typesafe.ai/v1/systemone?api_key=secret&foo=bar#frag"
		b.ReportAllocs()
		for b.Loop() {
			benchSinkString = cleanURL(raw)
		}
	})
}
