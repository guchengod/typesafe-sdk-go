package typesafe

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseLogLevelNames(t *testing.T) {
	cases := []struct {
		value string
		level slog.Level
		ok    bool
	}{
		{"debug", slog.LevelDebug, true},
		{"DEBUG", slog.LevelDebug, true},
		{"  debug  ", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"Info", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"WARNING", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"off", offLevel, true},
		{"OFF", offLevel, true},
		{"trace", slog.LevelInfo, false},
		{"critical", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"   ", slog.LevelInfo, false},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			level, ok := ParseLogLevel(tc.value)
			if ok != tc.ok {
				t.Fatalf("ParseLogLevel(%q) ok = %v, want %v", tc.value, ok, tc.ok)
			}
			if tc.ok && level != tc.level {
				t.Errorf("ParseLogLevel(%q) = %v, want %v", tc.value, level, tc.level)
			}
		})
	}
}

func TestIsSecretHeaderClassification(t *testing.T) {
	cases := []struct {
		name   string
		secret bool
	}{
		{"Authorization", true},
		{"authorization", true},
		{"AUTHORIZATION", true},
		{"Authorization ", true},
		{"Proxy-Authorization", true},
		{"proxy-authorization", true},
		{"X-API-Key", true},
		{"x-api-key", true},
		{"API-Key", true},
		{"Cookie", true},
		{"set-cookie", true},
		{"Set-Cookie", true},
		{"X-Access-Token", true},
		{"X-Client-Secret", true},
		{"x-MiXeD-ToKeN", true},
		{"X-Refresh-Secret", true},
		{"Accept", false},
		{"content-type", false},
		{"User-Agent", false},
		{"X-Visible", false},
		{"x-typesafe-request-id", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSecretHeader(tc.name); got != tc.secret {
				t.Errorf("isSecretHeader(%q) = %v, want %v", tc.name, got, tc.secret)
			}
		})
	}
}

func TestRedactHeadersReplacesSecretValues(t *testing.T) {
	headers := http.Header{
		HeaderAuthorization: {"Bearer auth-credential"},
		"X-Api-Key":         {"key-credential"},
		"Set-Cookie":        {"session=response-credential"},
		"X-Visible":         {"visible", "also-visible"},
	}

	redacted := redactHeaders(headers)
	for _, name := range []string{HeaderAuthorization, "X-Api-Key", "Set-Cookie"} {
		if got := redacted[name]; got != "***" {
			t.Errorf("redacted[%s] = %q, want %q", name, got, "***")
		}
	}
	if got := redacted["X-Visible"]; got != "visible, also-visible" {
		t.Errorf("redacted[X-Visible] = %q, want the values preserved", got)
	}
	if got := headers.Get(HeaderAuthorization); got != "Bearer auth-credential" {
		t.Errorf("input headers were mutated: %s = %q", HeaderAuthorization, got)
	}
	if got := redactHeaders(nil); got != nil {
		t.Errorf("redactHeaders(nil) = %v, want nil", got)
	}
}

func TestRedactingHandler(t *testing.T) {
	t.Run("secret attribute names are redacted", func(t *testing.T) {
		buffer := &bytes.Buffer{}
		logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(buffer, nil)))

		logger.InfoContext(t.Context(), "record",
			slog.String("authorization", "auth-credential"),
			slog.String("x-visible", "visible-value"),
			slog.String("x-api-key", "key-credential"),
		)

		output := buffer.String()
		for _, want := range []string{
			`"msg":"record"`,
			`"level":"INFO"`,
			`"authorization":"***"`,
			`"x-api-key":"***"`,
			// Attributes that need no redaction still reach the wrapped handler.
			`"x-visible":"visible-value"`,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("output is missing %q: %s", want, output)
			}
		}
		for _, secret := range []string{"auth-credential", "key-credential"} {
			if strings.Contains(output, secret) {
				t.Errorf("output leaked %q: %s", secret, output)
			}
		}
	})

	t.Run("secret keys inside a map attribute are redacted", func(t *testing.T) {
		buffer := &bytes.Buffer{}
		logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(buffer, nil)))

		logger.InfoContext(t.Context(), "record", slog.Any("headers", map[string]string{
			"X-Api-Key": "map-credential",
			"x-visible": "map-visible",
		}))

		output := buffer.String()
		if !strings.Contains(output, `"x-visible":"map-visible"`) {
			t.Errorf("output dropped the visible map entry: %s", output)
		}
		if !strings.Contains(output, `"***"`) {
			t.Errorf("output has no redaction marker: %s", output)
		}
		if strings.Contains(output, "map-credential") {
			t.Errorf("output leaked the map credential: %s", output)
		}
	})

	t.Run("clean records pass through unchanged", func(t *testing.T) {
		var redacting, plain bytes.Buffer
		redacted := NewRedactingHandler(slog.NewJSONHandler(&redacting, nil))
		untouched := slog.NewJSONHandler(&plain, nil)

		record := slog.NewRecord(time.Now(), slog.LevelWarn, "clean", 0)
		record.AddAttrs(
			slog.String("x-visible", "visible"),
			slog.Int("count", 2),
			slog.Any("tags", map[string]string{"a": "b"}),
		)
		if err := redacted.Handle(t.Context(), record); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if err := untouched.Handle(t.Context(), record); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
		if redacting.String() != plain.String() {
			t.Errorf("clean record was rewritten:\nredacting: %s\nplain:     %s", redacting.String(), plain.String())
		}
	})

	t.Run("attributes added with WithAttrs are redacted", func(t *testing.T) {
		buffer := &bytes.Buffer{}
		logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(buffer, nil))).
			With(slog.String("proxy-authorization", "attr-credential"))

		logger.InfoContext(t.Context(), "record", slog.String("x-visible", "visible"))

		output := buffer.String()
		if !strings.Contains(output, `"proxy-authorization":"***"`) {
			t.Errorf("output did not redact the bound attribute: %s", output)
		}
		if strings.Contains(output, "attr-credential") {
			t.Errorf("output leaked the bound attribute: %s", output)
		}
	})

	t.Run("attributes inside a group are redacted", func(t *testing.T) {
		buffer := &bytes.Buffer{}
		logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(buffer, nil))).WithGroup("request")

		logger.InfoContext(t.Context(), "record", slog.String("cookie", "group-credential"), slog.String("x-visible", "visible"))

		output := buffer.String()
		if !strings.Contains(output, `"request":{"cookie":"***"`) {
			t.Errorf("output did not redact the grouped attribute: %s", output)
		}
		if strings.Contains(output, "group-credential") {
			t.Errorf("output leaked the grouped attribute: %s", output)
		}
	})

	t.Run("enabled follows the wrapped handler", func(t *testing.T) {
		handler := NewRedactingHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))

		if handler.Enabled(t.Context(), slog.LevelInfo) {
			t.Error("Enabled(info) = true, want false")
		}
		if !handler.Enabled(t.Context(), slog.LevelWarn) {
			t.Error("Enabled(warn) = false, want true")
		}
	})
}

// loggingTestProbe identifies the records this file writes when it captures the default logger's
// output.
const loggingTestProbe = "logging-test-probe"

// loggingTestLogLevels are the levels the default logger tests exercise.
var loggingTestLogLevels = []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}

// loggingTestCaptureStderr runs fn with os.Stderr replaced by a pipe and returns what fn wrote to
// it, so the default logger's output is observed rather than inferred.
func loggingTestCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = original }()

	collected := make(chan string, 1)
	go func() {
		var buffer bytes.Buffer
		_, _ = io.Copy(&buffer, reader)
		collected <- buffer.String()
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Errorf("closing the captured stderr: %v", err)
	}
	output := <-collected
	_ = reader.Close()
	return output
}

func TestDefaultLoggerIsSilentWithoutConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"unset", ""},
		{"blank", "  "},
		{"unknown", "bogus"},
		{"off", "off"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(LogLevelEnv, tc.value)
			output := loggingTestCaptureStderr(t, func() {
				logger := defaultLogger()
				for _, level := range loggingTestLogLevels {
					logger.LogAttrs(t.Context(), level, loggingTestProbe)
				}
			})
			if strings.Contains(output, loggingTestProbe) {
				t.Errorf("defaultLogger() wrote to stderr with %s=%q, want no output:\n%s", LogLevelEnv, tc.value, output)
			}
		})
	}
}

func TestDefaultLoggerLevelFromEnvironment(t *testing.T) {
	cases := []struct {
		value      string
		suppressed []slog.Level
	}{
		{"debug", nil},
		{"info", []slog.Level{slog.LevelDebug}},
		{"warning", []slog.Level{slog.LevelDebug, slog.LevelInfo}},
		{"error", []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn}},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(LogLevelEnv, tc.value)
			output := loggingTestCaptureStderr(t, func() {
				logger := defaultLogger()
				for _, level := range loggingTestLogLevels {
					logger.LogAttrs(t.Context(), level, loggingTestProbe+" "+level.String())
				}
			})

			for _, level := range loggingTestLogLevels {
				logged := strings.Contains(output, loggingTestProbe+" "+level.String())
				if want := !slices.Contains(tc.suppressed, level); logged != want {
					t.Errorf("with %s=%q, %v logged = %v, want %v:\n%s", LogLevelEnv, tc.value, level, logged, want, output)
				}
			}
		})
	}
}

// loggingTestFailingTransport fails every request with cause, exercising the SDK's failure log.
type loggingTestFailingTransport struct{ cause error }

func (t loggingTestFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.cause
}

func TestSDKLoggingRedactsCredentials(t *testing.T) {
	newClient := func(t *testing.T, logger *slog.Logger, handler func(*http.Request, int) *http.Response) *Client {
		t.Helper()
		client, err := NewClient(
			WithAPIKey("auth-credential"),
			WithTransport(&mockTransport{handler: handler}),
			WithLogger(logger),
			WithRetryPolicy(noRetryPolicy()),
			WithHeader("X-API-Key", "request-credential"),
			WithHeader("X-Visible", "request-visible"),
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}

	t.Run("successful response", func(t *testing.T) {
		logger, buffer := capturedLogs()
		client := newClient(t, logger, func(*http.Request, int) *http.Response {
			return JSONResponseWithHeaders(http.StatusOK, fixedAnswerResponse, map[string]string{
				HeaderRequestID: "req_log",
				"Set-Cookie":    "response-credential",
				"X-Visible":     "response-visible",
			})
		})

		if _, err := client.SystemOne(t.Context(), "hello", fixedQuestions()); err != nil {
			t.Fatalf("SystemOne() error = %v", err)
		}

		output := buffer.String()
		for _, want := range []string{"request-visible", "response-visible", "***", "req_log", "hello"} {
			if !strings.Contains(output, want) {
				t.Errorf("log output is missing %q:\n%s", want, output)
			}
		}
		for _, secret := range []string{"auth-credential", "request-credential", "response-credential"} {
			if strings.Contains(output, secret) {
				t.Errorf("log output leaked %q:\n%s", secret, output)
			}
		}
	})

	t.Run("failed response", func(t *testing.T) {
		logger, buffer := capturedLogs()
		client := newClient(t, logger, func(*http.Request, int) *http.Response {
			return JSONResponseWithHeaders(http.StatusUnauthorized, `{"message":"failure"}`, map[string]string{
				"X-Access-Token": "response-credential",
				"X-Visible":      "response-visible",
			})
		})

		_, err := client.Models.List(t.Context())
		authErr, ok := errors.AsType[*AuthenticationError](err)
		if !ok {
			t.Fatalf("Models.List() error = %v (%T), want *AuthenticationError", err, err)
		}
		if authErr.Status != http.StatusUnauthorized {
			t.Errorf("Status = %d, want %d", authErr.Status, http.StatusUnauthorized)
		}

		output := buffer.String()
		for _, want := range []string{"msg=response", "status=401", "request-visible", "response-visible", "***"} {
			if !strings.Contains(output, want) {
				t.Errorf("log output is missing %q:\n%s", want, output)
			}
		}
		for _, secret := range []string{"auth-credential", "request-credential", "response-credential"} {
			if strings.Contains(output, secret) {
				t.Errorf("log output leaked %q:\n%s", secret, output)
			}
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		logger, buffer := capturedLogs()
		cause := errors.New("connection reset by peer")
		client, err := NewClient(
			WithAPIKey("auth-credential"),
			WithTransport(loggingTestFailingTransport{cause: cause}),
			WithLogger(logger),
			WithRetryPolicy(noRetryPolicy()),
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		_, err = client.Models.List(t.Context())
		if _, ok := errors.AsType[*APIConnectionError](err); !ok {
			t.Fatalf("Models.List() error = %v (%T), want *APIConnectionError", err, err)
		}

		output := buffer.String()
		for _, want := range []string{"msg=request.failed", "connection reset by peer", "***"} {
			if !strings.Contains(output, want) {
				t.Errorf("log output is missing %q:\n%s", want, output)
			}
		}
		if strings.Contains(output, "auth-credential") {
			t.Errorf("log output leaked the API key:\n%s", output)
		}
	})
}

func TestSDKLoggingLevelControlsOutput(t *testing.T) {
	cases := []struct {
		level        slog.Level
		wantResponse bool
		wantWire     bool
	}{
		{slog.LevelDebug, true, true},
		{slog.LevelInfo, true, false},
		{slog.LevelWarn, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.level.String(), func(t *testing.T) {
			buffer := &bytes.Buffer{}
			logger := slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: tc.level}))
			client, err := NewClient(
				WithAPIKey("test-key"),
				WithTransport(&mockTransport{handler: func(*http.Request, int) *http.Response {
					return JSONResponse(http.StatusOK, `{"models":[]}`)
				}}),
				WithLogger(logger),
				WithRetryPolicy(noRetryPolicy()),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			if _, err := client.Models.List(t.Context()); err != nil {
				t.Fatalf("Models.List() error = %v", err)
			}

			output := buffer.String()
			if got := strings.Contains(output, "msg=response"); got != tc.wantResponse {
				t.Errorf("response summary logged = %v, want %v:\n%s", got, tc.wantResponse, output)
			}
			if got := strings.Contains(output, "msg=request.wire"); got != tc.wantWire {
				t.Errorf("wire request logged = %v, want %v:\n%s", got, tc.wantWire, output)
			}
			if !tc.wantWire && strings.Contains(output, "headers=") {
				t.Errorf("headers were logged below debug level:\n%s", output)
			}
		})
	}
}
