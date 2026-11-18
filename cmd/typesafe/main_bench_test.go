package main

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

var (
	benchCLISinkCode   int
	benchCLISinkString string
)

type benchCLITransport struct {
	body string
}

func (t benchCLITransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {typesafe.ContentTypeJSON}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

const benchCLIResponse = `{
	"model": "jev-latest",
	"usage": {"input_tokens": 120, "output_tokens": 12},
	"answers": {
		"billing": {"type": "noul", "noul": 0.98},
		"tone": {"type": "choice", "choice": "angry", "confidence": 0.9, "probabilities": {"calm": 0.1, "angry": 0.9}},
		"urgency": {"type": "score", "score": 1.7, "confidence": 0.8, "legend": {"0": "can wait", "1": "this week", "2": "today"}, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}
}`

// BenchmarkRunAsk measures end-to-end CLI flag parsing, state decoding, SDK dispatch, and output
// formatting for both JSON and human-readable text modes.
func BenchmarkRunAsk(b *testing.B) {
	saved := clientOptions
	clientOptions = []typesafe.Option{
		typesafe.WithTransport(benchCLITransport{body: benchCLIResponse}),
		typesafe.WithRetryPolicy(&typesafe.RetryPolicy{MaxRetries: 0}),
	}
	b.Cleanup(func() { clientOptions = saved })

	baseArgs := []string{
		"ask",
		"--api-key", "bench-key",
		"--state-raw", `{"subject":"Charged twice","body":"Duplicate monthly invoice"}`,
		"--noul", "billing=Is this about billing?",
		"--choice", "tone=calm,angry:Hostile message",
		"--score", "urgency=can wait,this week,today",
	}

	for _, format := range []string{"json", "text"} {
		b.Run("format="+format, func(b *testing.B) {
			args := slices.Concat(baseArgs, []string{"--format", format})
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				code := run(ctx, args, io.Discard, io.Discard, strings.NewReader(""))
				if code != exitOK {
					b.Fatalf("run(format=%s) exit code = %d, want %d", format, code, exitOK)
				}
				benchCLISinkCode = code
			}
		})
	}
}

// BenchmarkSanitizeTerminalText measures terminal escape stripping on clean strings (fast path)
// versus strings carrying CSI and OSC escape sequences.
func BenchmarkSanitizeTerminalText(b *testing.B) {
	b.Run("CleanFastPath", func(b *testing.B) {
		input := "model jev-1.14.0 answered 3 questions with high confidence"
		b.ReportAllocs()
		b.SetBytes(int64(len(input)))
		for b.Loop() {
			benchCLISinkString = sanitizeTerminalText(input)
		}
	})

	b.Run("WithCSIAndOSCEscapes", func(b *testing.B) {
		input := "bad\x1b[31;1m_choice\x1b[0m and \x1b]52;c;cGF5bG9hZA==\x07 osc\r\ninjection"
		b.ReportAllocs()
		b.SetBytes(int64(len(input)))
		for b.Loop() {
			benchCLISinkString = sanitizeTerminalText(input)
		}
	})
}
