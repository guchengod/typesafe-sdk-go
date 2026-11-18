package typesafe

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// clientTestSilentLogger keeps SDK records out of the test output.
func clientTestSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// clientTestNewClient builds a client with a credential, a silent logger, and retries disabled
// unless opts override them.
func clientTestNewClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	options := append([]Option{
		WithAPIKey("test-key"),
		WithLogger(clientTestSilentLogger()),
		WithRetryPolicy(noRetryPolicy()),
	}, opts...)
	client, err := NewClient(options...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// clientTestTransport returns a transport that answers through handler.
func clientTestTransport(handler func(req *http.Request, attempt int) *http.Response) *mockTransport {
	return &mockTransport{handler: handler}
}

// clientTestAnsweringTransport returns a transport that answers every request the same way.
func clientTestAnsweringTransport(status int, body string) *mockTransport {
	return clientTestTransport(func(*http.Request, int) *http.Response {
		return JSONResponse(status, body)
	})
}

// clientTestAssertIdentityHeaders checks the headers the SDK always sets for itself on a first
// attempt.
func clientTestAssertIdentityHeaders(t *testing.T, call recordedCall) {
	t.Helper()
	identity := SDKName + "/" + SDKVersion
	for _, check := range []struct {
		name string
		want string
	}{
		{HeaderAuthorization, "Bearer test-key"},
		{HeaderAccept, ContentTypeJSON},
		{HeaderUserAgent, identity},
		{HeaderSDK, identity},
	} {
		if got := call.Header.Get(check.name); got != check.want {
			t.Errorf("%s = %q, want %q", check.name, got, check.want)
		}
		if values := call.Header.Values(check.name); len(values) != 1 {
			t.Errorf("%s has %d values (%v), want exactly one", check.name, len(values), values)
		}
	}
	if got := call.Header.Get(HeaderRuntime); !strings.HasPrefix(got, "go/") {
		t.Errorf("%s = %q, want a go/ prefix", HeaderRuntime, got)
	}
	if got := call.Header.Get(HeaderRetryCount); got != "" {
		t.Errorf("%s = %q on a first attempt, want it absent", HeaderRetryCount, got)
	}
}

// clientTestDeadlineTransport fails a request only once the attempt's deadline has passed.
type clientTestDeadlineTransport struct{}

func (clientTestDeadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// clientTestClosingTransport records the lifecycle calls the SDK makes on a caller-supplied
// transport.
type clientTestClosingTransport struct {
	mu         sync.Mutex
	idleCloses int
	closes     int
	closeErr   error
}

func (c *clientTestClosingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return JSONResponse(http.StatusOK, `{"models":[]}`), nil
}

func (c *clientTestClosingTransport) CloseIdleConnections() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleCloses++
}

func (c *clientTestClosingTransport) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return c.closeErr
}

func (c *clientTestClosingTransport) counts() (idleCloses, closes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idleCloses, c.closes
}

func TestClientSystemOneRequestLine(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, fixedAnswerResponse)
	client := clientTestNewClient(t, WithTransport(transport))

	result, err := client.SystemOne(t.Context(), map[string]any{"document": "Hello 🌍"}, fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	if result.Model != DefaultModel {
		t.Errorf("Model = %q, want %q", result.Model, DefaultModel)
	}

	call := transport.lastCall(t)
	if call.Method != http.MethodPost {
		t.Errorf("method = %q, want %q", call.Method, http.MethodPost)
	}
	if want := "https://api.typesafe.ai/v1/systemone"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	clientTestAssertIdentityHeaders(t, call)
	if got := call.Header.Get(HeaderContentType); got != ContentTypeJSON {
		t.Errorf("%s = %q, want %q", HeaderContentType, got, ContentTypeJSON)
	}

	body := decodingBody(t, call)
	if got := body["model"]; got != DefaultModel {
		t.Errorf("body model = %v, want %q", got, DefaultModel)
	}
	questions, ok := body["questions"].(map[string]any)
	if !ok {
		t.Fatalf("body questions = %#v, want an object", body["questions"])
	}
	if len(questions) != 3 {
		t.Errorf("body questions = %v, want the three supplied questions", body["questions"])
	}
}

func TestClientModelsListRequestLine(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t, WithTransport(transport))

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	if len(response.Models) != 0 {
		t.Errorf("Models = %v, want an empty list", response.Models)
	}

	call := transport.lastCall(t)
	if call.Method != http.MethodGet {
		t.Errorf("method = %q, want %q", call.Method, http.MethodGet)
	}
	if want := "https://api.typesafe.ai/v1/models"; call.URL != want {
		t.Errorf("URL = %q, want %q", call.URL, want)
	}
	if len(call.Body) != 0 {
		t.Errorf("body = %q, want no body on a GET", call.Body)
	}
	clientTestAssertIdentityHeaders(t, call)
	if values := call.Header.Values(HeaderContentType); len(values) != 0 {
		t.Errorf("%s = %v on a bodyless request, want it absent", HeaderContentType, values)
	}
}

func TestClientCallerContentTypeOnBodylessRequest(t *testing.T) {
	// The SDK sets Content-Type itself only when a request carries a body, and it never removes
	// one the caller configured, so a caller-supplied default survives on a GET.
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t, WithTransport(transport), WithHeader(HeaderContentType, "text/plain"))

	if _, err := client.Models.List(t.Context()); err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	if got := transport.lastCall(t).Header.Get(HeaderContentType); got != "text/plain" {
		t.Errorf("%s = %q, want the caller's %q", HeaderContentType, got, "text/plain")
	}
}

func TestClientCustomBaseURL(t *testing.T) {
	t.Run("system one", func(t *testing.T) {
		transport := clientTestAnsweringTransport(http.StatusOK, fixedAnswerResponse)
		client := clientTestNewClient(t, WithTransport(transport), WithBaseURL("https://example.test/prefix/"))

		if got := client.Config().BaseURL; got != "https://example.test/prefix" {
			t.Errorf("BaseURL = %q, want %q", got, "https://example.test/prefix")
		}
		if _, err := client.SystemOne(t.Context(), "hello", fixedQuestions()); err != nil {
			t.Fatalf("SystemOne() error = %v", err)
		}
		if want := "https://example.test/prefix/v1/systemone"; transport.lastCall(t).URL != want {
			t.Errorf("URL = %q, want %q", transport.lastCall(t).URL, want)
		}
	})

	t.Run("models", func(t *testing.T) {
		transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
		client := clientTestNewClient(t, WithTransport(transport), WithBaseURL("https://example.test/prefix/"))

		if _, err := client.Models.List(t.Context()); err != nil {
			t.Fatalf("Models.List() error = %v", err)
		}
		if want := "https://example.test/prefix/v1/models"; transport.lastCall(t).URL != want {
			t.Errorf("URL = %q, want %q", transport.lastCall(t).URL, want)
		}
	})
}

func TestClientProtectedHeadersCannotBeDisplaced(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, fixedAnswerResponse)
	client := clientTestNewClient(t,
		WithTransport(transport),
		WithHeaders(map[string]string{
			"authorization":          "injected-secret",
			"accept":                 "text/plain",
			"user-agent":             "injected-wrong",
			"x-typesafe-sdk":         "injected-wrong",
			"x-typesafe-runtime":     "injected-wrong",
			"x-typesafe-retry-count": "99",
			"content-type":           "injected-wrong",
		}),
		WithHeader("X-Team", "default"),
	)

	_, err := client.SystemOne(t.Context(), "hello", fixedQuestions(), WithCallHeaders(map[string]string{
		"AuThOrIzAtIoN":          "call-secret",
		"ACCEPT":                 "text/plain",
		"User-Agent":             "call-wrong",
		"X-TypeSafe-SDK":         "call-wrong",
		"X-TYPESAFE-RUNTIME":     "call-wrong",
		"X-TYPESAFE-RETRY-COUNT": "42",
		"Content-Type":           "call-wrong",
		"x-team":                 "call",
	}))
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}

	call := transport.lastCall(t)
	clientTestAssertIdentityHeaders(t, call)
	if got := call.Header.Get(HeaderContentType); got != ContentTypeJSON {
		t.Errorf("%s = %q, want %q", HeaderContentType, got, ContentTypeJSON)
	}
	if got := call.Header.Get("X-Team"); got != "call" {
		t.Errorf("X-Team = %q, want the per-call value %q", got, "call")
	}
	if values := call.Header.Values(HeaderAccept); len(values) != 1 {
		t.Errorf("%s = %v, want exactly one value", HeaderAccept, values)
	}

	for name, values := range call.Header {
		for _, value := range values {
			for _, injected := range []string{"injected-secret", "call-secret", "injected-wrong", "call-wrong", "42"} {
				if strings.Contains(value, injected) {
					t.Errorf("header %s carried caller value %q", name, value)
				}
			}
		}
	}
}

func TestClientDefaultHeadersAndCallHeaderOverrides(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t,
		WithTransport(transport),
		WithHeader("X-Default", "kept"),
		WithHeaders(map[string]string{"X-Team": "default", "X-Extra": "extra"}),
	)

	_, err := client.Models.List(t.Context(),
		WithCallHeader("x-team", "call"),
		WithCallHeaders(map[string]string{"X-Call": "called", "X-Default": "overridden"}),
	)
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}

	call := transport.lastCall(t)
	for _, check := range []struct {
		name string
		want string
	}{
		{"X-Default", "overridden"},
		{"X-Team", "call"},
		{"X-Extra", "extra"},
		{"X-Call", "called"},
	} {
		if got := call.Header.Get(check.name); got != check.want {
			t.Errorf("%s = %q, want %q", check.name, got, check.want)
		}
	}
}

func TestClientRetryCountHeader(t *testing.T) {
	t.Run("caller value is stripped on the first attempt", func(t *testing.T) {
		transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
		client := clientTestNewClient(t, WithTransport(transport))

		if _, err := client.Models.List(t.Context(), WithCallHeader(HeaderRetryCount, "99")); err != nil {
			t.Fatalf("Models.List() error = %v", err)
		}
		if got := transport.lastCall(t).Header.Get(HeaderRetryCount); got != "" {
			t.Errorf("%s = %q, want it stripped", HeaderRetryCount, got)
		}
	})

	t.Run("retry index is reported on retries", func(t *testing.T) {
		policy := &RetryPolicy{
			MaxRetries:   2,
			HTTPStatuses: map[int]bool{http.StatusInternalServerError: true},
		}
		attempts := 0
		transport := clientTestTransport(func(*http.Request, int) *http.Response {
			attempts++
			if attempts < 3 {
				return JSONResponse(http.StatusInternalServerError, `{"message":"boom"}`)
			}
			return JSONResponse(http.StatusOK, `{"models":[{"name":"jev-latest","description":"Fast model","release_date":"2026-08-01"}]}`)
		})
		client := clientTestNewClient(t, WithTransport(transport), WithRetryPolicy(policy))

		response, err := client.Models.List(t.Context(), WithCallHeader(HeaderRetryCount, "99"))
		if err != nil {
			t.Fatalf("Models.List() error = %v", err)
		}
		if len(response.Models) != 1 || response.Models[0].Name != "jev-latest" {
			t.Errorf("Models = %+v, want the recovered payload", response.Models)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}

		calls := transport.callsSnapshot()
		if len(calls) != 3 {
			t.Fatalf("recorded %d calls, want 3", len(calls))
		}
		for index, want := range []string{"", "1", "2"} {
			if got := calls[index].Header.Get(HeaderRetryCount); got != want {
				t.Errorf("call %d: %s = %q, want %q", index, HeaderRetryCount, got, want)
			}
		}
	})
}

func TestClientCallRetryOverridesClientPolicy(t *testing.T) {
	t.Run("per-call policy adds retries", func(t *testing.T) {
		attempts := 0
		transport := clientTestTransport(func(*http.Request, int) *http.Response {
			attempts++
			return JSONResponse(http.StatusInternalServerError, `{"message":"boom"}`)
		})
		client := clientTestNewClient(t, WithTransport(transport))

		policy := &RetryPolicy{MaxRetries: 2, HTTPStatuses: map[int]bool{http.StatusInternalServerError: true}}
		_, err := client.Models.List(t.Context(), WithCallRetry(policy))
		if err == nil {
			t.Fatal("Models.List() error = nil, want the server failure")
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
		if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != http.StatusInternalServerError {
			t.Errorf("error = %v (%T), want a 500 *APIError", err, err)
		}
	})

	t.Run("per-call policy disables retries", func(t *testing.T) {
		attempts := 0
		transport := clientTestTransport(func(*http.Request, int) *http.Response {
			attempts++
			return JSONResponse(http.StatusInternalServerError, `{"message":"boom"}`)
		})
		policy := &RetryPolicy{MaxRetries: 2, HTTPStatuses: map[int]bool{http.StatusInternalServerError: true}}
		client := clientTestNewClient(t, WithTransport(transport), WithRetryPolicy(policy))

		if _, err := client.Models.List(t.Context(), WithCallRetry(noRetryPolicy())); err == nil {
			t.Fatal("Models.List() error = nil, want the server failure")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1", attempts)
		}
	})
}

func TestClientCallTimeoutOverride(t *testing.T) {
	t.Run("per-call timeout produces a timeout error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := clientTestNewClient(t, WithTransport(clientTestDeadlineTransport{}), WithTimeout(30*time.Second))

			_, err := client.Models.List(t.Context(), WithCallTimeout(5*time.Second))
			timeoutErr, ok := AsTimeoutError(err)
			if !ok {
				t.Fatalf("Models.List() error = %v (%T), want *APITimeoutError", err, err)
			}
			if timeoutErr.Timeout != 5*time.Second {
				t.Errorf("Timeout = %v, want the per-call %v", timeoutErr.Timeout, 5*time.Second)
			}
		})
	})

	t.Run("client timeout applies without an override", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := clientTestNewClient(t, WithTransport(clientTestDeadlineTransport{}), WithTimeout(5*time.Second))

			_, err := client.Models.List(t.Context())
			timeoutErr, ok := AsTimeoutError(err)
			if !ok {
				t.Fatalf("Models.List() error = %v (%T), want *APITimeoutError", err, err)
			}
			if timeoutErr.Timeout != 5*time.Second {
				t.Errorf("Timeout = %v, want the client-level %v", timeoutErr.Timeout, 5*time.Second)
			}
		})
	})
}

func TestClientNonPositiveCallTimeoutIsRejectedBeforeSending(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t, WithTransport(transport))

	for _, timeout := range []time.Duration{0, -time.Second} {
		_, err := client.Models.List(t.Context(), WithCallTimeout(timeout))

		sdkErr, ok := errors.AsType[*SDKError](err)
		if !ok {
			t.Fatalf("WithCallTimeout(%v) error = %v (%T), want *SDKError", timeout, err, err)
		}
		if !strings.Contains(sdkErr.Message, "timeout must be a positive") {
			t.Errorf("WithCallTimeout(%v) error = %q, want it to describe a positive timeout", timeout, sdkErr.Message)
		}
	}
	if calls := transport.callsSnapshot(); len(calls) != 0 {
		t.Errorf("recorded %d requests, want none for a rejected timeout", len(calls))
	}
}

func TestClientSendsThroughSuppliedHTTPClient(t *testing.T) {
	transport := clientTestTransport(func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{"models":[]}`)
	})
	supplied := &http.Client{Transport: transport}
	client := clientTestNewClient(t, WithHTTPClient(supplied))

	if got := client.HTTPClient(); got != supplied {
		t.Fatalf("HTTPClient() = %#v, want the supplied *http.Client", got)
	}
	if _, err := client.Models.List(t.Context()); err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	call := transport.lastCall(t)
	if got := call.Header.Get(HeaderAuthorization); got != "Bearer test-key" {
		t.Errorf("%s = %q, want %q", HeaderAuthorization, got, "Bearer test-key")
	}
}

func TestClientClose(t *testing.T) {
	t.Run("supplied transport is closed once", func(t *testing.T) {
		transport := &clientTestClosingTransport{}
		client, err := NewClient(WithAPIKey("test-key"), WithTransport(transport), WithLogger(clientTestSilentLogger()))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		if err := client.Close(); err != nil {
			t.Fatalf("Close() error = %v, want nil", err)
		}
		idleCloses, closes := transport.counts()
		if closes != 1 {
			t.Errorf("transport saw %d Close calls, want 1", closes)
		}
		if idleCloses != 1 {
			t.Errorf("transport saw %d CloseIdleConnections calls, want 1", idleCloses)
		}

		// Close is idempotent: a later call settles without touching the transport again.
		if err := client.Close(); err != nil {
			t.Errorf("second Close() error = %v, want nil", err)
		}
		idleCloses, closes = transport.counts()
		if closes != 1 || idleCloses != 1 {
			t.Errorf("after a second Close the transport saw %d Close and %d CloseIdleConnections calls, want 1 and 1", closes, idleCloses)
		}
	})

	t.Run("transport inside a supplied http client is closed", func(t *testing.T) {
		transport := &clientTestClosingTransport{}
		client, err := NewClient(WithAPIKey("test-key"), WithHTTPClient(&http.Client{Transport: transport}), WithLogger(clientTestSilentLogger()))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		if err := client.Close(); err != nil {
			t.Fatalf("Close() error = %v, want nil", err)
		}
		idleCloses, closes := transport.counts()
		if closes != 1 {
			t.Errorf("transport saw %d Close calls, want 1", closes)
		}
		if idleCloses != 1 {
			t.Errorf("transport saw %d CloseIdleConnections calls, want 1", idleCloses)
		}
	})

	t.Run("transport close failure is returned", func(t *testing.T) {
		sentinel := errors.New("close failed")
		transport := &clientTestClosingTransport{closeErr: sentinel}
		client, err := NewClient(WithAPIKey("test-key"), WithTransport(transport), WithLogger(clientTestSilentLogger()))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		if err := client.Close(); !errors.Is(err, sentinel) {
			t.Errorf("Close() error = %v, want %v", err, sentinel)
		}
		if err := client.Close(); !errors.Is(err, sentinel) {
			t.Errorf("second Close() error = %v, want the first call's %v", err, sentinel)
		}
		if _, closes := transport.counts(); closes != 1 {
			t.Errorf("transport saw %d Close calls, want 1", closes)
		}
	})

	t.Run("owned transport is closed without error", func(t *testing.T) {
		client, err := NewClient(WithAPIKey("test-key"), WithLogger(clientTestSilentLogger()))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v, want nil", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("second Close() error = %v, want nil", err)
		}
	})
}

func TestClientModelsListDecodesPayloadAndMetadata(t *testing.T) {
	payload := `{"models":[{"name":"jev-latest","description":"Fast model","release_date":"2026-08-01","context_window":128000}]}`
	transport := clientTestTransport(func(*http.Request, int) *http.Response {
		return JSONResponseWithHeaders(http.StatusOK, payload, map[string]string{
			HeaderRequestID: "req_123",
			"X-Visible":     "visible",
		})
	})
	client := clientTestNewClient(t, WithTransport(transport))

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	want := ModelMetadata{Name: "jev-latest", Description: "Fast model", ReleaseDate: "2026-08-01"}
	if len(response.Models) != 1 || response.Models[0] != want {
		t.Errorf("Models = %+v, want [%+v]", response.Models, want)
	}

	requestID, err := response.RequestID()
	if err != nil {
		t.Fatalf("RequestID() error = %v", err)
	}
	if requestID != "req_123" {
		t.Errorf("RequestID() = %q, want %q", requestID, "req_123")
	}
	if got := response.RawRequestID(); got != "req_123" {
		t.Errorf("RawRequestID() = %q, want %q", got, "req_123")
	}
	raw, err := response.RawHTTPResponse() //nolint:bodyclose // synthetic Response uses http.NoBody
	if err != nil {
		t.Fatalf("RawHTTPResponse() error = %v", err)
	}
	if raw.StatusCode != http.StatusOK {
		t.Errorf("RawHTTPResponse().StatusCode = %d, want %d", raw.StatusCode, http.StatusOK)
	}
	if got := raw.Header.Get(HeaderRequestID); got != "req_123" {
		t.Errorf("RawHTTPResponse() %s = %q, want %q", HeaderRequestID, got, "req_123")
	}
	// Unmodeled fields are dropped from the card but survive in the raw body.
	if got := string(response.RawBody()); got != payload {
		t.Errorf("RawBody() = %q, want %q", got, payload)
	}
	if !strings.Contains(string(response.RawBody()), "context_window") {
		t.Error("RawBody() lost the unmodeled context_window field")
	}
}

func TestClientModelsListWithoutRequestID(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t, WithTransport(transport))

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	if got := response.RawRequestID(); got != "" {
		t.Errorf("RawRequestID() = %q, want empty", got)
	}
	if _, err := response.RequestID(); err == nil {
		t.Error("RequestID() error = nil, want a failure when the header is absent")
	}
}

func TestClientModelsListRejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		fieldPath string
	}{
		{"not json", `not json`, ""},
		{"null body", `null`, ""},
		{"missing models", `{}`, "models"},
		{"models is not a list", `{"models":"bad"}`, "models"},
		{"card is not an object", `{"models":[1]}`, "models[0]"},
		{"missing description", `{"models":[{"name":"x"}]}`, "models[0].description"},
		{"missing release date", `{"models":[{"name":"x","description":"d"}]}`, "models[0].release_date"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := clientTestAnsweringTransport(http.StatusOK, tc.body)
			client := clientTestNewClient(t, WithTransport(transport))

			_, err := client.Models.List(t.Context())
			validation, ok := AsValidationError(err)
			if !ok {
				t.Fatalf("Models.List() error = %v (%T), want *APIResponseValidationError", err, err)
			}
			if validation.FieldPath != tc.fieldPath {
				t.Errorf("FieldPath = %q, want %q", validation.FieldPath, tc.fieldPath)
			}
			if validation.Status != http.StatusOK {
				t.Errorf("Status = %d, want %d", validation.Status, http.StatusOK)
			}
			if !strings.Contains(validation.Error(), "Invalid response data at '"+tc.fieldPath+"'") {
				t.Errorf("error = %q, want it to name the field path", validation.Error())
			}
		})
	}
}

func TestClientModelsListAcceptsEmptyList(t *testing.T) {
	transport := clientTestAnsweringTransport(http.StatusOK, `{"models":[]}`)
	client := clientTestNewClient(t, WithTransport(transport))

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	if response.Models == nil {
		t.Error("Models = nil, want an empty, non-nil slice")
	}
	if len(response.Models) != 0 {
		t.Errorf("Models = %+v, want no models", response.Models)
	}
}

func TestClientRequestCompression(t *testing.T) {
	t.Parallel()

	transport := clientTestAnsweringTransport(http.StatusOK, `{"model":"jev-latest","usage":{"input_tokens":10,"output_tokens":2},"answers":{"q":{"type":"noul","noul":0.5}}}`)
	client := clientTestNewClient(t, WithTransport(transport))

	// Small body (< 1 KiB)
	_, err := client.SystemOne(t.Context(), "small state", Questions{"q": NewNoul("test")})
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call := transport.lastCall(t)
	if enc := call.Header.Get(HeaderContentEncoding); enc != "" {
		t.Errorf("small body: Content-Encoding = %q, want empty", enc)
	}

	// Large body (>= 1 KiB)
	largeState := strings.Repeat("a long text string for compression testing ", 30) // ~1.3 KiB
	_, err = client.SystemOne(t.Context(), largeState, Questions{"q": NewNoul("test")})
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call = transport.lastCall(t)
	if enc := call.Header.Get(HeaderContentEncoding); enc != EncodingGzip {
		t.Errorf("large body: Content-Encoding = %q, want gzip", enc)
	}

	// Verify gzip decompression
	gr, err := gzip.NewReader(bytes.NewReader(call.Body))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("decompression failed: %v", err)
	}
	if !strings.Contains(string(decompressed), largeState) {
		t.Errorf("decompressed body does not contain state")
	}

	// WithCallCompression(false) on large body
	_, err = client.SystemOne(t.Context(), largeState, Questions{"q": NewNoul("test")}, WithCallCompression(false))
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call = transport.lastCall(t)
	if enc := call.Header.Get(HeaderContentEncoding); enc != "" {
		t.Errorf("WithCallCompression(false): Content-Encoding = %q, want empty", enc)
	}
	if !strings.Contains(string(call.Body), largeState) {
		t.Errorf("recorded body is not raw JSON")
	}
}

func TestClientDangerZoneHeaders(t *testing.T) {
	t.Parallel()

	transport := clientTestAnsweringTransport(http.StatusOK, `{"model":"jev-latest","usage":{"input_tokens":10,"output_tokens":2},"answers":{"q":{"type":"noul","noul":0.5}}}`)

	// Client configured with both danger zone headers
	client := clientTestNewClient(t,
		WithTransport(transport),
		WithLongContext(true),
		WithImages(true),
	)

	_, err := client.SystemOne(t.Context(), "test", Questions{"q": NewNoul("test")})
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call := transport.lastCall(t)
	if call.Header.Get(HeaderDangerZoneLongContext) != "true" {
		t.Errorf("missing %s", HeaderDangerZoneLongContext)
	}
	if call.Header.Get(HeaderDangerZoneImages) != "true" {
		t.Errorf("missing %s", HeaderDangerZoneImages)
	}

	// Overriding with call options to disable them
	_, err = client.SystemOne(t.Context(), "test", Questions{"q": NewNoul("test")},
		WithCallLongContext(false),
		WithCallImages(false),
	)
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call = transport.lastCall(t)
	if call.Header.Get(HeaderDangerZoneLongContext) != "" {
		t.Errorf("%s present after WithCallLongContext(false)", HeaderDangerZoneLongContext)
	}
	if call.Header.Get(HeaderDangerZoneImages) != "" {
		t.Errorf("%s present after WithCallImages(false)", HeaderDangerZoneImages)
	}
}

func TestClientWithImagesFalseSuppressesAutoHeaderAndBlocksCallerInjection(t *testing.T) {
	t.Parallel()

	transport := clientTestAnsweringTransport(http.StatusOK, `{"model":"jev-latest","usage":{"input_tokens":10,"output_tokens":2},"answers":{"q":{"type":"noul","noul":0.5}}}`)
	client := clientTestNewClient(t, WithTransport(transport), WithImages(false))

	img, err := NewImageWithRef("ref-suppressed", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	if _, err := client.SystemOne(t.Context(), map[string]any{"photo": img}, Questions{"q": NewNoul("test")}); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	if got := transport.lastCall(t).Header.Get(HeaderDangerZoneImages); got != "" {
		t.Errorf("%s = %q when client configured WithImages(false), want empty", HeaderDangerZoneImages, got)
	}

	// Per-call WithCallImages(true) still overrides client WithImages(false).
	if _, err := client.SystemOne(t.Context(), map[string]any{"photo": img}, Questions{"q": NewNoul("test")}, WithCallImages(true)); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	if got := transport.lastCall(t).Header.Get(HeaderDangerZoneImages); got != "true" {
		t.Errorf("%s = %q when WithCallImages(true), want true", HeaderDangerZoneImages, got)
	}

	// Caller headers cannot spoof Danger-Zone or Content-Encoding headers.
	_, err = client.SystemOne(t.Context(), "small", Questions{"q": NewNoul("test")}, WithCallHeaders(map[string]string{
		HeaderDangerZoneLongContext: "true",
		HeaderDangerZoneImages:      "true",
		HeaderContentEncoding:       "br",
	}))
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call := transport.lastCall(t)
	for _, h := range []string{HeaderDangerZoneLongContext, HeaderDangerZoneImages, HeaderContentEncoding} {
		if got := call.Header.Get(h); got != "" {
			t.Errorf("caller injected protected header %s = %q", h, got)
		}
	}

	// Invalid CRLF in WithCallHeader fails fast with *SDKError and 0 retries/calls.
	beforeCalls := len(transport.callsSnapshot())
	retryPolicy := DefaultRetryPolicy()
	retryPolicy.MaxRetries = 3
	_, err = client.Models.List(t.Context(), WithCallRetry(retryPolicy), WithCallHeader("X-Correlation", "bad\r\nheader"))
	if _, ok := errors.AsType[*SDKError](err); !ok {
		t.Fatalf("WithCallHeader CRLF error = %v (%T), want *SDKError", err, err)
	}
	if got := len(transport.callsSnapshot()); got != beforeCalls {
		t.Errorf("recorded %d calls on invalid header, want %d", got, beforeCalls)
	}
}

func TestClientSuppliedHTTPClientBlocksRedirectsAndDowngrades(t *testing.T) {
	t.Parallel()

	t.Run("nil CheckRedirect on supplied client still blocks redirects", func(t *testing.T) {
		hops := 0
		transport := clientTestTransport(func(req *http.Request, _ int) *http.Response {
			hops++
			if hops == 1 {
				return JSONResponseWithHeaders(http.StatusFound, `{"message":"redirect"}`, map[string]string{
					"Location": "http://api.typesafe.ai/v1/models",
				})
			}
			return JSONResponse(http.StatusOK, `{"models":[]}`)
		})
		supplied := &http.Client{Transport: transport}
		client := clientTestNewClient(t, WithHTTPClient(supplied))

		if client.HTTPClient() != supplied {
			t.Fatal("HTTPClient() did not preserve supplied pointer")
		}
		if supplied.CheckRedirect != nil {
			t.Fatal("supplied http.Client CheckRedirect was mutated")
		}

		_, err := client.Models.List(t.Context())
		apiErr, ok := AsAPIError(err)
		if !ok || apiErr.Status != http.StatusFound {
			t.Fatalf("Models.List() error = %v (%T), want 302 *APIError", err, err)
		}
		if hops != 1 {
			t.Errorf("transport saw %d hops, want 1 (redirect blocked)", hops)
		}
	})

	t.Run("explicit CheckRedirect blocks https-to-http downgrade and strips secrets cross-origin", func(t *testing.T) {
		var redirectedAuth, redirectedAPIKey string
		transport := clientTestTransport(func(req *http.Request, _ int) *http.Response {
			switch req.URL.Host {
			case "api.typesafe.ai":
				return JSONResponseWithHeaders(http.StatusFound, `{}`, map[string]string{
					"Location": "https://other.example.test/v1/models",
				})
			default:
				redirectedAuth = req.Header.Get(HeaderAuthorization)
				redirectedAPIKey = req.Header.Get("X-Api-Key")
				return JSONResponse(http.StatusOK, `{"models":[]}`)
			}
		})
		supplied := &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		}
		client := clientTestNewClient(t, WithHTTPClient(supplied), WithHeader("X-Api-Key", "gateway-secret"))

		if _, err := client.Models.List(t.Context()); err != nil {
			t.Fatalf("Models.List() error = %v", err)
		}
		if redirectedAuth != "" || redirectedAPIKey != "" {
			t.Errorf("cross-origin redirect leaked credentials: Auth=%q X-Api-Key=%q", redirectedAuth, redirectedAPIKey)
		}

		// HTTPS -> HTTP downgrade must be refused even with custom CheckRedirect.
		downgradeTransport := clientTestTransport(func(req *http.Request, _ int) *http.Response {
			if req.URL.Scheme == "https" {
				return JSONResponseWithHeaders(http.StatusFound, `{}`, map[string]string{
					"Location": "http://api.typesafe.ai/v1/models",
				})
			}
			t.Error("followed https-to-http downgrade redirect")
			return JSONResponse(http.StatusOK, `{"models":[]}`)
		})
		downgradeClient := clientTestNewClient(t, WithHTTPClient(&http.Client{
			Transport:     downgradeTransport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		}))
		if _, err := downgradeClient.Models.List(t.Context()); err == nil {
			t.Fatal("Models.List() succeeded across https-to-http downgrade, want error")
		}
	})
}

func TestClientNilGuardsAndNilResponseBody(t *testing.T) {
	t.Parallel()

	var nilClient *Client
	if err := nilClient.Close(); err != nil {
		t.Errorf("(*Client)(nil).Close() = %v, want nil", err)
	}
	if _, err := nilClient.SystemOne(t.Context(), "x", fixedQuestions()); err == nil {
		t.Error("(*Client)(nil).SystemOne() = nil, want *SDKError")
	}
	if _, err := SystemOneAs[map[string]any](t.Context(), nilClient, "x", fixedQuestions()); err == nil {
		t.Error("SystemOneAs(nil client) = nil, want *SDKError")
	}
	var nilModels *ModelsService
	if _, err := nilModels.List(t.Context()); err == nil {
		t.Error("(*ModelsService)(nil).List() = nil, want *SDKError")
	}

	// Custom RoundTripper returning a nil Response.Body must not panic.
	transport := clientTestTransport(func(*http.Request, int) *http.Response {
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: nil}
	})
	client := clientTestNewClient(t, WithTransport(transport))
	_, err := client.Models.List(t.Context())
	if apiErr, ok := AsAPIError(err); !ok || apiErr.Status != http.StatusInternalServerError {
		t.Errorf("nil Body response error = %v (%T), want 500 *APIError", err, err)
	}
}

func TestClientConcurrentSafetyAndWithAssetsIsolation(t *testing.T) {
	t.Parallel()

	transport := clientTestAnsweringTransport(http.StatusOK, `{"model":"jev-latest","usage":{"input_tokens":5,"output_tokens":2},"answers":{"q":{"type":"noul","noul":0.75}}}`)
	client := clientTestNewClient(t, WithTransport(transport))

	sharedLogo, err := NewImageWithRef("ref-logo", []byte("logo-bytes"), "png")
	if err != nil {
		t.Fatal(err)
	}
	sharedAssets := map[string]any{
		"ref-logo": sharedLogo.Asset(),
	}

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			perReqImg, err := NewImage([]byte("tenant-secret-image"), "jpg")
			if err != nil {
				t.Errorf("goroutine %d NewImage: %v", i, err)
				return
			}
			resp, err := client.SystemOne(
				t.Context(),
				map[string]any{"scan": perReqImg, "pad": strings.Repeat("p", 1200)},
				Questions{"q": NewNoul("Is this valid?")},
				WithAssets(sharedAssets),
			)
			if err != nil {
				t.Errorf("goroutine %d SystemOne: %v", i, err)
				return
			}
			// Mutating RawBody() slice must not corrupt internal response bytes.
			raw := resp.RawBody()
			if len(raw) > 0 {
				raw[0] = '!'
			}
			if len(resp.RawBody()) > 0 && resp.RawBody()[0] == '!' {
				t.Errorf("goroutine %d RawBody() was not defensively cloned", i)
			}
		})
	}
	wg.Wait()

	// sharedAssets must still contain only ref-logo (no cross-request contamination).
	if len(sharedAssets) != 1 {
		t.Errorf("sharedAssets mutated across requests: got %d keys, want 1", len(sharedAssets))
	}
}
