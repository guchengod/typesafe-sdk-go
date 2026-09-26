package typesafe

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// recordedCall captures one request the SDK sent through a mockTransport.
type recordedCall struct {
	Method  string
	URL     string
	Header  http.Header
	Body    []byte
	Attempt int
}

// mockTransport answers requests with canned responses and records what it received.
type mockTransport struct {
	mu       sync.Mutex
	calls    []recordedCall
	handler  func(req *http.Request, attempt int) *http.Response
	closed   int
	closeErr error
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		read, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = read
	}

	m.mu.Lock()
	attempt := len(m.calls)
	call := recordedCall{
		Method:  req.Method,
		URL:     req.URL.String(),
		Header:  req.Header.Clone(),
		Body:    body,
		Attempt: attempt,
	}
	if retryCount := req.Header.Get(HeaderRetryCount); retryCount != "" {
		call.Attempt, _ = strconv.Atoi(retryCount)
	}
	m.calls = append(m.calls, call)
	handler := m.handler
	m.mu.Unlock()

	return handler(req, attempt), nil
}

func (m *mockTransport) CloseIdleConnections() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return m.closeErr
}

// callsSnapshot returns a copy of the recorded calls.
func (m *mockTransport) callsSnapshot() []recordedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedCall(nil), m.calls...)
}

// lastCall returns the most recent recorded call.
func (m *mockTransport) lastCall(t *testing.T) recordedCall {
	t.Helper()
	calls := m.callsSnapshot()
	if len(calls) == 0 {
		t.Fatal("no requests were recorded")
	}
	return calls[len(calls)-1]
}

// JSONResponse builds a response carrying body as JSON.
func JSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{ContentTypeJSON}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// JSONResponseWithHeaders builds a JSON response with additional headers.
func JSONResponseWithHeaders(status int, body string, headers map[string]string) *http.Response {
	response := JSONResponse(status, body)
	for name, value := range headers {
		response.Header.Set(name, value)
	}
	return response
}

// newMockClient returns a client wired to a transport that always answers the same way.
func newMockClient(t *testing.T, handler func(req *http.Request, attempt int) *http.Response, opts ...Option) (*Client, *mockTransport) {
	t.Helper()
	transport := &mockTransport{handler: handler}
	options := append([]Option{
		WithAPIKey("test-key"),
		WithTransport(transport),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithRetryPolicy(noRetryPolicy()),
	}, opts...)
	client, err := NewClient(options...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, transport
}

// newJSONClient returns a client that answers every request with the same JSON body.
func newJSONClient(t *testing.T, status int, body string, opts ...Option) (*Client, *mockTransport) {
	t.Helper()
	return newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(status, body)
	}, opts...)
}

// noRetryPolicy disables retries so a test observes exactly one attempt unless it asks otherwise.
func noRetryPolicy() *RetryPolicy {
	policy := DefaultRetryPolicy()
	policy.MaxRetries = 0
	return policy
}

// decodingBody reads a recorded request body into a generic map.
func decodingBody(t *testing.T, call recordedCall) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(call.Body, &decoded); err != nil {
		t.Fatalf("request body is not JSON: %v (body=%q)", err, call.Body)
	}
	return decoded
}

// fixedAnswerResponse is a complete System One payload used across tests.
const fixedAnswerResponse = `{
	"model": "jev-latest",
	"usage": {"input_tokens": 120, "output_tokens": 12},
	"answers": {
		"billing": {"type": "noul", "noul": 0.98},
		"tone": {"type": "choice", "choice": "angry", "confidence": 0.9, "probabilities": {"calm": 0.1, "angry": 0.9}},
		"urgency": {"type": "score", "score": 1.7, "confidence": 0.8, "legend": {"0": "can wait", "1": "this week", "2": "today"}, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}
}`

// fixedAnswersJSON is an answers object covering every question in fixedQuestions, for payloads that
// exercise an envelope field rather than the answers themselves.
const fixedAnswersJSON = `{"billing":{"type":"noul","noul":0.98},"tone":{"type":"choice","choice":"angry","confidence":0.9,"probabilities":{"angry":0.9,"calm":0.1}},"urgency":{"type":"score","score":1.7,"confidence":0.8,"legend":{"0":"can wait","1":"this week","2":"today"},"probabilities":{"0":0.1,"1":0.1,"2":0.8}}}`

// fixedQuestions is the question set paired with fixedAnswerResponse.
func fixedQuestions() Questions {
	return Questions{
		"billing": NewNoul("Is this message about billing?"),
		"tone":    NewChoice(map[string]any{"calm": nil, "angry": nil}, WithInstructions("What is the tone?")),
		"urgency": NewScore([]any{"can wait", "this week", "today"}, WithInstructions("How urgent is this?")),
	}
}

// capturedLogs collects log output from a logger for assertions.
func capturedLogs() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})), buffer
}
