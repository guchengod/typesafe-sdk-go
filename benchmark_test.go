package typesafe

// Benchmarks for the SDK's hot paths. Everything here runs in-process against the
// mockTransport from helpers_test.go, so no benchmark needs a network or an API key.
//
// Every benchmark carries an "allocs/op" note recording what was observed when the benchmark
// was written, so a review can spot an allocation regression without re-running the suite. The
// figures in those notes come from:
//
//	go test -run XXX -bench . -benchtime 200000x .
//
// on go1.27 darwin/arm64 (Apple M4 Pro). They are a budget, not a contract: treat a higher
// number as a regression to explain, not an automatic failure.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"
)

// benchSink* keep the measured results reachable so the compiler cannot eliminate the work
// being benchmarked.
var (
	benchSinkResponse *SystemOneResponse
	benchSinkError    error
	benchSinkString   string
	benchSinkBytes    []byte
	benchSinkHeader   http.Header
	benchSinkDelay    time.Duration
)

// benchmarkSubsetResponse is the smallest complete System One payload: the required model and
// usage fields plus a single answer.
const benchmarkSubsetResponse = `{"model":"jev-latest","usage":{"input_tokens":12,"output_tokens":1},"answers":{"billing":{"type":"noul","noul":0.98}}}`

// benchmarkLogger discards log output. parseSystemOneResponse needs a logger even when no
// unrecognized answer type forces a warning.
func benchmarkLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// benchmarkResponse builds the internal response value a decoded payload arrives in.
func benchmarkResponse(body string) *response {
	return &response{
		StatusCode: http.StatusOK,
		Header:     http.Header{HeaderRequestID: []string{"req_bench"}},
		Body:       []byte(body),
		RequestID:  "req_bench",
		Endpoint:   "POST " + DefaultBaseURL + SystemOnePath,
	}
}

// benchmarkMockClient returns a client whose transport answers every request with body. It
// mirrors helpers_test.go's newMockClient, which cannot be reused here because it is typed to
// *testing.T.
func benchmarkMockClient(b *testing.B, body string) (*Client, *mockTransport) {
	b.Helper()
	transport := &mockTransport{handler: func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, body)
	}}
	client, err := NewClient(
		WithAPIKey("bench-key"),
		WithTransport(transport),
		WithLogger(benchmarkLogger()),
		WithRetryPolicy(noRetryPolicy()),
	)
	if err != nil {
		b.Fatalf("NewClient() error = %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return client, transport
}

// BenchmarkSystemOneResponseDecode measures decoding a System One success payload: once for the
// smallest complete response, once for a payload carrying all three answer kinds.
//
// Allocs/op: subset 47 (3.5 KB), full 121 (8.1 KB).
func BenchmarkSystemOneResponseDecode(b *testing.B) {
	logger := benchmarkLogger()
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"Subset", benchmarkSubsetResponse},
		{"Full", fixedAnswerResponse},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			resp := benchmarkResponse(testCase.body)
			b.ReportAllocs()
			b.SetBytes(int64(len(testCase.body)))
			for b.Loop() {
				parsed, err := parseSystemOneResponse(resp, logger)
				if err != nil {
					b.Fatalf("parseSystemOneResponse() error = %v", err)
				}
				benchSinkResponse = parsed
			}
		})
	}
}

// BenchmarkQuestionsEncode measures the wire preparation of a mixed question set: validation and
// normalization followed by JSON encoding, which is what systemOne does before sending.
//
// Allocs/op: 34 (1.5 KB).
func BenchmarkQuestionsEncode(b *testing.B) {
	questions := fixedQuestions()

	normalized, err := NormalizeQuestions(questions)
	if err != nil {
		b.Fatalf("NormalizeQuestions() error = %v", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		b.Fatalf("json.Marshal() error = %v", err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(encoded)))
	for b.Loop() {
		normalized, err := NormalizeQuestions(questions)
		if err != nil {
			b.Fatalf("NormalizeQuestions() error = %v", err)
		}
		encoded, err := json.Marshal(normalized)
		if err != nil {
			b.Fatalf("json.Marshal() error = %v", err)
		}
		benchSinkBytes = encoded
	}
}

// BenchmarkRequestHeaders measures header construction for a request that carries both default
// headers and per-call headers, with a caller-supplied retry count that must be stripped.
//
// Allocs/op: 21 (1.1 KB).
func BenchmarkRequestHeaders(b *testing.B) {
	config := &Config{
		APIKey:       "bench-key",
		BaseURL:      DefaultBaseURL,
		DefaultModel: DefaultModel,
		Timeout:      DefaultTimeout,
		DefaultHeaders: http.Header{
			"X-Request-Source": {"benchmark"},
			"X-Trace-Id":       {"trace-1"},
		},
		RetryPolicy: DefaultRetryPolicy(),
	}
	req := request{
		method: http.MethodPost,
		path:   SystemOnePath,
		headers: map[string]string{
			"X-Call-Id":      "call-1",
			HeaderRetryCount: "99",
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		benchSinkHeader = buildHeaders(config, req, true)
	}
}

// BenchmarkErrorConstruction measures mapping a 429 onto a *RateLimitError, and rendering that
// error. Together they are the cost of every failed call that crosses a retry boundary.
//
// Allocs/op: newAPIError429 6, apiErrorError 4.
func BenchmarkErrorConstruction(b *testing.B) {
	body := []byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`)
	headers := http.Header{}
	headers.Set(HeaderContentType, ContentTypeJSON)
	headers.Set(HeaderRequestID, "req_bench_429")
	headers.Set(HeaderRetryAfter, "2")
	endpoint := "POST " + DefaultBaseURL + SystemOnePath
	decoded := decodeErrorBody(body)

	b.Run("newAPIError429", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for b.Loop() {
			err := newAPIError(http.StatusTooManyRequests, decoded, body, headers, endpoint)
			if err == nil {
				b.Fatal("newAPIError() = nil, want a rate limit error")
			}
			benchSinkError = err
		}
	})

	b.Run("apiErrorError", func(b *testing.B) {
		err := newAPIError(http.StatusTooManyRequests, decoded, body, headers, endpoint)
		if _, ok := AsRateLimitError(err); !ok {
			b.Fatalf("newAPIError() = %T, want *RateLimitError", err)
		}
		b.ReportAllocs()
		for b.Loop() {
			benchSinkString = err.Error()
		}
	})
}

// BenchmarkBackoff measures the jittered exponential delay the default policy computes for each
// retry attempt it can produce.
//
// Allocs/op: 0.
func BenchmarkBackoff(b *testing.B) {
	policy := DefaultRetryPolicy()
	b.ReportAllocs()
	for attempt := 0; b.Loop(); attempt++ {
		benchSinkDelay = policy.backoff(attempt%(policy.MaxRetries+1) + 1)
	}
}

// BenchmarkClientSystemOne measures a complete System One call end to end: question
// normalization, JSON encoding, header construction, the HTTP round trip, and response
// decoding. The transport answers in process, so the number is the SDK's own overhead with the
// network removed.
//
// Allocs/op: 210 (15.5 KB).
func BenchmarkClientSystemOne(b *testing.B) {
	client, transport := benchmarkMockClient(b, fixedAnswerResponse)
	questions := fixedQuestions()
	state := map[string]any{"message": "I was charged twice. Please help."}
	ctx := b.Context()

	b.ReportAllocs()
	b.SetBytes(int64(len(fixedAnswerResponse)))
	for iteration := 0; b.Loop(); iteration++ {
		result, err := client.SystemOne(ctx, state, questions)
		if err != nil {
			b.Fatalf("SystemOne() error = %v", err)
		}
		benchSinkResponse = result
		if iteration%1024 == 1023 {
			// The mock transport records every call. Left alone, that log would grow with the
			// benchmark and distort the very allocations being measured, so it is trimmed
			// periodically.
			transport.mu.Lock()
			transport.calls = transport.calls[:0]
			transport.mu.Unlock()
		}
	}
}

// The examples below construct and encode questions only. They need no network access and no API
// key, so `go test` runs them as part of the normal suite.

func ExampleNewNoul() {
	question := NewNoul("Is this message about billing?")
	fmt.Println(question.QuestionType())
	fmt.Println(question.Instructions)
	// Output:
	// noul
	// Is this message about billing?
}

func ExampleWithNoulCriteria() {
	question := NewNoul("Is this spam?", WithNoulCriteria(NoulCriteria{
		True:  "Unsolicited advertising",
		False: "A legitimate conversation",
	}))
	encoded, err := json.Marshal(question)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
	// Output: {"type":"noul","instructions":"Is this spam?","criteria":{"true":"Unsolicited advertising","false":"A legitimate conversation"}}
}

func ExampleNewChoice() {
	question := NewChoice(
		map[string]any{"calm": nil, "angry": "An upset or hostile message"},
		WithInstructions("What is the tone of this message?"),
	)
	encoded, err := json.Marshal(question)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
	// Output: {"type":"choice","criteria":{"angry":"An upset or hostile message","calm":null},"instructions":"What is the tone of this message?"}
}

func ExampleNewScore() {
	question := NewScore(
		[]any{"can wait", "needs attention this week", "needs attention today"},
		WithInstructions("How urgent is this message?"),
	)
	encoded, err := json.Marshal(question)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
	// Output: {"type":"score","criteria":["can wait","needs attention this week","needs attention today"],"instructions":"How urgent is this message?"}
}

func ExampleWithQuestionField() {
	question := NewNoul("Is this message about billing?", WithQuestionField("language", "en"))
	encoded, err := json.Marshal(question)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
	// Output: {"instructions":"Is this message about billing?","language":"en","type":"noul"}
}

func ExampleNormalizeQuestions() {
	normalized, err := NormalizeQuestions(Questions{
		"billing": NewNoul("Is this message about billing?"),
		"tone":    NewChoice(map[string]any{"calm": nil, "angry": nil}),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(slices.Sorted(maps.Keys(normalized)))
	fmt.Println(normalized["tone"].(Question).QuestionType())
	// Output:
	// [billing tone]
	// choice
}
