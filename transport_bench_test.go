package typesafe

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkClientSystemOneCompressed measures end-to-end System One execution when gzip request
// compression is enabled across payload sizes above MinCompressionBytes (1 KiB).
func BenchmarkClientSystemOneCompressed(b *testing.B) {
	for _, size := range []int{2048, 16384} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			client, transport := benchmarkMockClient(b, fixedAnswerResponse)
			questions := fixedQuestions()
			chunk := "Customer reported duplicate invoice billing and webhook delivery latency. "
			repeats := max(1, size/len(chunk))
			state := map[string]any{
				"report": strings.Repeat(chunk, repeats),
			}
			ctx := b.Context()

			b.ReportAllocs()
			for iteration := 0; b.Loop(); iteration++ {
				result, err := client.SystemOne(ctx, state, questions, WithCallCompression(true))
				if err != nil {
					b.Fatalf("SystemOne(WithCallCompression) error = %v", err)
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
}

// BenchmarkCompressGzip measures pooled gzip compression across realistic JSON payload sizes.
func BenchmarkCompressGzip(b *testing.B) {
	pattern := []byte(`{"model":"jev-latest","state":{"ticket":"Duplicate charge of $49 on monthly plan."},"questions":{"billing":{"type":"noul","instructions":"Is this about billing?"}}}`)
	for _, size := range []int{1024, 4096, 32768} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			payload := bytes.Repeat(pattern, (size+len(pattern)-1)/len(pattern))[:size]
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				compressed, err := compressGzip(payload)
				if err != nil {
					b.Fatalf("compressGzip() error = %v", err)
				}
				benchSinkBytes = compressed
			}
		})
	}
}

// BenchmarkReadResponseBody measures bounded body reading on both 2xx success responses and
// non-2xx error responses capped at MaxErrorResponseBodyLimit.
func BenchmarkReadResponseBody(b *testing.B) {
	successPayload := []byte(strings.Repeat(fixedAnswerResponse, 8))
	errorPayload := []byte(`{"error":{"message":"Rate limit exceeded for organization","type":"rate_limit_error"}}`)

	b.Run("Status200", func(b *testing.B) {
		var reader bytes.Reader
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: int64(len(successPayload)),
			Body:          io.NopCloser(&reader),
		}
		b.ReportAllocs()
		b.SetBytes(int64(len(successPayload)))
		for b.Loop() {
			reader.Reset(successPayload)
			body, err := readResponseBody(resp)
			if err != nil {
				b.Fatalf("readResponseBody(200) error = %v", err)
			}
			benchSinkBytes = body
		}
	})

	b.Run("Status429", func(b *testing.B) {
		var reader bytes.Reader
		resp := &http.Response{
			StatusCode:    http.StatusTooManyRequests,
			ContentLength: int64(len(errorPayload)),
			Body:          io.NopCloser(&reader),
		}
		b.ReportAllocs()
		b.SetBytes(int64(len(errorPayload)))
		for b.Loop() {
			reader.Reset(errorPayload)
			body, err := readResponseBody(resp)
			if err != nil {
				b.Fatalf("readResponseBody(429) error = %v", err)
			}
			benchSinkBytes = body
		}
	})
}
