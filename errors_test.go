package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// errorsTestHeader builds an http.Header from alternating name and value pairs. Repeating a name
// appends an additional value.
func errorsTestHeader(pairs ...string) http.Header {
	header := make(http.Header, len(pairs)/2+1)
	for index := 0; index+1 < len(pairs); index += 2 {
		header.Add(pairs[index], pairs[index+1])
	}
	return header
}

// errorsTestMessageBody is the decoded JSON body a failed request carries in these tests.
func errorsTestMessageBody(message string) map[string]any {
	return map[string]any{"message": message}
}

func TestErrorStatusMappingToType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		want   any
	}{
		{http.StatusBadRequest, (*BadRequestError)(nil)},
		{http.StatusUnauthorized, (*AuthenticationError)(nil)},
		{http.StatusForbidden, (*PermissionDeniedError)(nil)},
		{http.StatusNotFound, (*NotFoundError)(nil)},
		{http.StatusUnprocessableEntity, (*UnprocessableEntityError)(nil)},
		{http.StatusTooManyRequests, (*RateLimitError)(nil)},
		{StatusOverloaded, (*OverloadedError)(nil)},
		{http.StatusInternalServerError, (*InternalServerError)(nil)},
		{http.StatusServiceUnavailable, (*InternalServerError)(nil)},
		{599, (*InternalServerError)(nil)},
		{http.StatusConflict, (*APIError)(nil)},
		{http.StatusTeapot, (*APIError)(nil)},
		{http.StatusFound, (*APIError)(nil)},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			raw := []byte(`{"message":"failed"}`)
			err := newAPIError(tc.status, decodeErrorBody(raw), raw,
				errorsTestHeader(HeaderRequestID, "req-1"), "GET https://api.typesafe.ai/v1/models")

			if got, want := reflect.TypeOf(err), reflect.TypeOf(tc.want); got != want {
				t.Fatalf("newAPIError(%d) type = %v, want %v", tc.status, got, want)
			}
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("newAPIError(%d) is not extractable as *APIError", tc.status)
			}
			if apiErr.Status != tc.status {
				t.Errorf("Status = %d, want %d", apiErr.Status, tc.status)
			}
			if apiErr.Endpoint != "GET https://api.typesafe.ai/v1/models" {
				t.Errorf("Endpoint = %q, want %q", apiErr.Endpoint, "GET https://api.typesafe.ai/v1/models")
			}
			if apiErr.RequestID != "req-1" {
				t.Errorf("RequestID = %q, want %q", apiErr.RequestID, "req-1")
			}
			if !reflect.DeepEqual(apiErr.Body, errorsTestMessageBody("failed")) {
				t.Errorf("Body = %#v, want %#v", apiErr.Body, errorsTestMessageBody("failed"))
			}
			if got := string(apiErr.RawBody); got != `{"message":"failed"}` {
				t.Errorf("RawBody = %q, want %q", got, `{"message":"failed"}`)
			}
		})
	}
}

func TestAPIErrorErrorStringOptionalParts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *APIError
		want string
	}{
		{
			name: "every optional part present",
			err: &APIError{
				Status:    500,
				Body:      errorsTestMessageBody("server exploded"),
				Endpoint:  "POST https://api.typesafe.ai/v1/systemone",
				RequestID: "req-1",
			},
			want: "POST https://api.typesafe.ai/v1/systemone: 500 server exploded (request_id=req-1)",
		},
		{
			name: "no request id",
			err: &APIError{
				Status:   400,
				Body:     errorsTestMessageBody("Bad request"),
				Endpoint: "GET https://api.typesafe.ai/v1/models",
			},
			want: "GET https://api.typesafe.ai/v1/models: 400 Bad request",
		},
		{
			name: "no endpoint",
			err:  &APIError{Status: 429, Body: errorsTestMessageBody("Too many requests")},
			want: "429 Too many requests",
		},
		{
			name: "request id only",
			err:  &APIError{Status: 503, Body: errorsTestMessageBody("unavailable"), RequestID: "req-7"},
			want: "503 unavailable (request_id=req-7)",
		},
		{
			name: "no body",
			err:  &APIError{Status: 500, RequestID: "req-9"},
			want: "500 status code (no body) (request_id=req-9)",
		},
		{
			name: "no body and no request context",
			err:  &APIError{Status: 400},
			want: "400 status code (no body)",
		},
		{
			name: "message override wins over the body",
			err: &APIError{
				Status:  429,
				Body:    errorsTestMessageBody("Server explanation"),
				Message: "A custom explanation",
			},
			want: "429 A custom explanation",
		},
		{
			name: "empty override falls back to the body",
			err: &APIError{
				Status:  429,
				Body:    errorsTestMessageBody("Server explanation"),
				Message: "",
			},
			want: "429 Server explanation",
		},
		{
			name: "override is rendered even with a request context",
			err: &APIError{
				Status:    400,
				Body:      errorsTestMessageBody("Bad request"),
				Endpoint:  "GET https://api.typesafe.ai/v1/models",
				RequestID: "req-2",
				Message:   "Bad request",
			},
			want: "GET https://api.typesafe.ai/v1/models: 400 Bad request (request_id=req-2)",
		},
		{
			name: "status only when the body renders nothing",
			err:  &APIError{Status: 400, Body: "", RawBody: []byte(`""`)},
			want: "400",
		},
		{
			name: "string body is used verbatim",
			err:  &APIError{Status: 400, Body: "bad request", RawBody: []byte(`"bad request"`)},
			want: "400 bad request",
		},
		{
			name: "array body renders raw",
			err:  &APIError{Status: 400, Body: []any{1.0, 2.0}, RawBody: []byte("[1,2]")},
			want: "400 [1,2]",
		},
		{
			name: "number body renders raw",
			err:  &APIError{Status: 400, Body: 42.0, RawBody: []byte("42")},
			want: "400 42",
		},
		{
			name: "raw body is ignored when the decoded body is nil",
			err:  &APIError{Status: 500, RawBody: []byte("ignored")},
			want: "500 status code (no body)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCleanEndpointStripsCredentialsQueryAndFragment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		method string
		rawURL string
		want   string
	}{
		{"plain url", "GET", "https://api.typesafe.ai/v1/models", "GET https://api.typesafe.ai/v1/models"},
		{"userinfo", "GET", "https://user:password@example.test/v1/models", "GET https://example.test/v1/models"},
		{"username only", "POST", "https://user@example.test/v1/systemone", "POST https://example.test/v1/systemone"},
		{"query", "GET", "https://example.test/v1/models?token=secret", "GET https://example.test/v1/models"},
		{"fragment", "GET", "https://example.test/v1/models#fragment", "GET https://example.test/v1/models"},
		{
			"credentials query and fragment",
			"GET",
			"https://user:password@example.test/v1/models?token=secret#fragment",
			"GET https://example.test/v1/models",
		},
		{"port and path prefix", "POST", "https://api.example.test:8443/prefix/v1/systemone", "POST https://api.example.test:8443/prefix/v1/systemone"},
		{"empty url", "POST", "", ""},
		{"unparseable url", "GET", "://bad", "GET ://bad"},
		{"unparseable host", "GET", "http://[::1", "GET http://[::1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cleanEndpoint(tc.method, tc.rawURL); got != tc.want {
				t.Errorf("cleanEndpoint(%q, %q) = %q, want %q", tc.method, tc.rawURL, got, tc.want)
			}
		})
	}
}

func TestAPIErrorEndpointOmitsURLCredentials(t *testing.T) {
	t.Parallel()
	endpoint := cleanEndpoint("GET", "https://user:password@example.test/v1/models?token=secret#fragment")
	err := &APIError{
		Status:    400,
		Body:      errorsTestMessageBody("Bad request"),
		Endpoint:  endpoint,
		RequestID: "",
	}
	const want = "GET https://example.test/v1/models: 400 Bad request"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if err.Endpoint != "GET https://example.test/v1/models" {
		t.Errorf("Endpoint = %q, want %q", err.Endpoint, "GET https://example.test/v1/models")
	}
	for _, secret := range []string{"password", "token=secret", "fragment", "user:"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("Error() leaks %q: %q", secret, err.Error())
		}
	}
}

func TestDecodeErrorBodyShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  []byte
		want any
	}{
		{"nil", nil, nil},
		{"empty", []byte{}, nil},
		{"json object", []byte(`{"message":"failed"}`), map[string]any{"message": "failed"}},
		{"json string", []byte(`"failed"`), "failed"},
		{"json array", []byte(`[1,2]`), []any{1.0, 2.0}},
		{"json number", []byte(`42`), 42.0},
		{"json true", []byte(`true`), true},
		{"json null", []byte(`null`), nil},
		{"non-json text", []byte("not JSON"), "not JSON"},
		{"truncated json", []byte(`{"broken":`), `{"broken":`},
		{"trailing garbage", []byte(`{"a":1} trailing`), `{"a":1} trailing`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decodeErrorBody(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decodeErrorBody(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestErrorMessageExtractionPrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body any
		want string
	}{
		{"nil body", nil, ""},
		{"string body", "boom", "boom"},
		{"empty string body", "", ""},
		{"error wins over message and detail", map[string]any{"error": "e1", "message": "m1", "detail": "d1"}, "e1"},
		{"non-string error falls through to message", map[string]any{"error": 123.0, "message": "m1"}, "m1"},
		{"nested error message", map[string]any{"error": map[string]any{"message": "nested"}}, "nested"},
		{"nested error without message falls through", map[string]any{"error": map[string]any{"code": "x"}, "message": "m1"}, "m1"},
		{"empty error yields nothing", map[string]any{"error": ""}, ""},
		{"empty error does not fall through to message", map[string]any{"error": "", "message": "m1"}, ""},
		{"message wins over detail", map[string]any{"message": "m1", "detail": "d1"}, "m1"},
		{"string detail", map[string]any{"detail": "d1"}, "d1"},
		{"object detail message", map[string]any{"detail": map[string]any{"message": "dm"}}, "dm"},
		{"object detail without message", map[string]any{"detail": map[string]any{"code": "x"}}, ""},
		{
			"validation entry drops the body segment",
			map[string]any{"detail": []any{map[string]any{"loc": []any{"body", "answers", "tone"}, "msg": "bad"}}},
			"answers.tone: bad",
		},
		{
			"validation entries join with a semicolon",
			map[string]any{"detail": []any{
				map[string]any{"loc": []any{"q"}, "msg": "a"},
				map[string]any{"loc": []any{"body", "r"}, "msg": "b"},
			}},
			"q: a; r: b",
		},
		{"validation entry without a location", map[string]any{"detail": []any{map[string]any{"msg": "no-loc"}}}, "no-loc"},
		{"validation entry with only the body segment", map[string]any{"detail": []any{map[string]any{"loc": []any{"body"}, "msg": "x"}}}, "x"},
		{"empty validation list", map[string]any{"detail": []any{}}, ""},
		{"validation list without usable entries", map[string]any{"detail": []any{nil, 42.0, map[string]any{"msg": 4.0}}}, ""},
		{"non-object detail", map[string]any{"detail": 5.0}, ""},
		{"array body", []any{"anything"}, ""},
		{"number body", 42.0, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractMessage(tc.body); got != tc.want {
				t.Errorf("extractMessage(%#v) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestAPIErrorTruncationOnlyAppliesToRawBodyFallback(t *testing.T) {
	t.Parallel()

	t.Run("extracted message is never truncated", func(t *testing.T) {
		t.Parallel()
		message := strings.Repeat("m", 500)
		raw, err := json.Marshal(errorsTestMessageBody(message))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		apiErr := &APIError{Status: 400, Body: errorsTestMessageBody(message), RawBody: raw}
		if got := apiErr.Error(); got != "400 "+message {
			t.Errorf("Error() = %q, want the full 500-character message", got)
		}
	})

	t.Run("string body is never truncated", func(t *testing.T) {
		t.Parallel()
		body := strings.Repeat("x", MaxErrorBodyLength+1)
		apiErr := &APIError{Status: 400, Body: body, RawBody: []byte(body)}
		if got, want := apiErr.Error(), "400 "+body; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("raw body fallback at the limit", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"unknown":"` + strings.Repeat("x", 186) + `"}`)
		if len(raw) != MaxErrorBodyLength {
			t.Fatalf("test body length = %d, want exactly %d", len(raw), MaxErrorBodyLength)
		}
		apiErr := &APIError{Status: 400, Body: map[string]any{"unknown": strings.Repeat("x", 186)}, RawBody: raw}
		if got, want := apiErr.Error(), "400 "+string(raw); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("raw body fallback beyond the limit", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"unknown":"` + strings.Repeat("x", 187) + `"}`)
		if len(raw) != MaxErrorBodyLength+1 {
			t.Fatalf("test body length = %d, want exactly %d", len(raw), MaxErrorBodyLength+1)
		}
		apiErr := &APIError{Status: 400, Body: map[string]any{"unknown": strings.Repeat("x", 187)}, RawBody: raw}
		want := "400 " + string(raw[:MaxErrorBodyLength]) + "…"
		if got := apiErr.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestParseRetryAfterTruthTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers http.Header
		want    time.Duration
		ok      bool
	}{
		{"absent headers", nil, 0, false},
		{"empty header set", http.Header{}, 0, false},
		{"seconds", errorsTestHeader(HeaderRetryAfter, "2"), 2 * time.Second, true},
		{"fractional seconds", errorsTestHeader(HeaderRetryAfter, "2.5"), 2500 * time.Millisecond, true},
		{"zero seconds means retry now", errorsTestHeader(HeaderRetryAfter, "0"), 0, true},
		{"empty seconds means retry now", errorsTestHeader(HeaderRetryAfter, ""), 0, true},
		{"padded seconds", errorsTestHeader(HeaderRetryAfter, "  3  "), 3 * time.Second, true},
		{"negative seconds disable the hint", errorsTestHeader(HeaderRetryAfter, "-1"), 0, false},
		{"malformed seconds", errorsTestHeader(HeaderRetryAfter, "bad"), 0, false},
		{"nan seconds are ignored", errorsTestHeader(HeaderRetryAfter, "NaN"), 0, false},
		{"infinite seconds are ignored", errorsTestHeader(HeaderRetryAfter, "inf"), 0, false},
		{"negative infinite seconds are ignored", errorsTestHeader(HeaderRetryAfter, "-inf"), 0, false},
		{"milliseconds", errorsTestHeader(HeaderRetryAfterMs, "125"), 125 * time.Millisecond, true},
		{"fractional milliseconds", errorsTestHeader(HeaderRetryAfterMs, "0.5"), 500 * time.Microsecond, true},
		{"zero milliseconds means retry now", errorsTestHeader(HeaderRetryAfterMs, "0"), 0, true},
		{"empty milliseconds means retry now", errorsTestHeader(HeaderRetryAfterMs, ""), 0, true},
		{"padded milliseconds", errorsTestHeader(HeaderRetryAfterMs, " 125 "), 125 * time.Millisecond, true},
		{"negative milliseconds with no fallback", errorsTestHeader(HeaderRetryAfterMs, "-1"), 0, false},
		{"negative milliseconds falls through to seconds", errorsTestHeader(HeaderRetryAfterMs, "-1", HeaderRetryAfter, "2"), 2 * time.Second, true},
		{"nan milliseconds falls through to seconds", errorsTestHeader(HeaderRetryAfterMs, "NaN", HeaderRetryAfter, "1.5"), 1500 * time.Millisecond, true},
		{"infinite milliseconds falls through to nothing", errorsTestHeader(HeaderRetryAfterMs, "inf"), 0, false},
		{"infinite milliseconds falls through to seconds", errorsTestHeader(HeaderRetryAfterMs, "inf", HeaderRetryAfter, "1.5"), 1500 * time.Millisecond, true},
		{"malformed milliseconds falls through to seconds", errorsTestHeader(HeaderRetryAfterMs, "bad", HeaderRetryAfter, "2"), 2 * time.Second, true},
		{"milliseconds win over seconds", errorsTestHeader(HeaderRetryAfterMs, "0", HeaderRetryAfter, "50"), 0, true},
		{"header names are case-insensitive", errorsTestHeader("RETRY-AFTER-MS", "250"), 250 * time.Millisecond, true},
		{"first header value wins", errorsTestHeader(HeaderRetryAfter, "3", HeaderRetryAfter, "9"), 3 * time.Second, true},
		{"overflowing seconds are ignored", errorsTestHeader(HeaderRetryAfter, "1e308"), 0, false},
		{"overflowing milliseconds are ignored", errorsTestHeader(HeaderRetryAfterMs, "1e308"), 0, false},
		{"seconds beyond the Duration range are ignored", errorsTestHeader(HeaderRetryAfter, "9223372036854775807"), 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseRetryAfter(tc.headers)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseRetryAfter(%v) = (%v, %v), want (%v, %v)", tc.headers, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	future := now.Add(10 * time.Second).UTC().Format(http.TimeFormat)
	past := now.Add(-10 * time.Second).UTC().Format(http.TimeFormat)

	delay, ok := parseRetryAfter(errorsTestHeader(HeaderRetryAfter, future))
	if !ok {
		t.Fatalf("parseRetryAfter(future date) ok = false, want true")
	}
	if delay < 8*time.Second || delay > 11*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want roughly 10s", delay)
	}

	pastDelay, ok := parseRetryAfter(errorsTestHeader(HeaderRetryAfter, past))
	if !ok {
		t.Fatalf("parseRetryAfter(past date) ok = false, want true")
	}
	if pastDelay != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", pastDelay)
	}

	fallback, ok := parseRetryAfter(errorsTestHeader(HeaderRetryAfterMs, "bad", HeaderRetryAfter, future))
	if !ok || fallback < 8*time.Second || fallback > 11*time.Second {
		t.Errorf("parseRetryAfter(malformed ms, future date) = (%v, %v), want roughly 10s", fallback, ok)
	}
}

func TestRateLimitErrorRetryAfterMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		headers    map[string]string
		wantMS     *float64
		wantDelay  time.Duration
		wantHasDel bool
	}{
		{"milliseconds", map[string]string{HeaderRetryAfterMs: "125"}, new(125.0), 125 * time.Millisecond, true},
		{"seconds", map[string]string{HeaderRetryAfter: "2"}, new(2000.0), 2 * time.Second, true},
		{"fractional seconds", map[string]string{HeaderRetryAfter: "1.5"}, new(1500.0), 1500 * time.Millisecond, true},
		{"milliseconds win", map[string]string{HeaderRetryAfterMs: "250", HeaderRetryAfter: "9"}, new(250.0), 250 * time.Millisecond, true},
		{"negative milliseconds fall through", map[string]string{HeaderRetryAfterMs: "-1", HeaderRetryAfter: "2"}, new(2000.0), 2 * time.Second, true},
		{"zero means retry now", map[string]string{HeaderRetryAfterMs: "0"}, new(0.0), 0, true},
		{"absent headers", nil, nil, 0, false},
		{"malformed header", map[string]string{HeaderRetryAfter: "bad"}, nil, 0, false},
		{"negative seconds", map[string]string{HeaderRetryAfter: "-1"}, nil, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := newMockClient(t, func(*http.Request, int) *http.Response {
				return JSONResponseWithHeaders(http.StatusTooManyRequests, `{"message":"Too many requests"}`, tc.headers)
			})
			_, err := client.Models.List(t.Context())
			if err == nil {
				t.Fatal("Models.List() error = nil, want a 429 failure")
			}
			rateLimit, ok := AsRateLimitError(err)
			if !ok {
				t.Fatalf("AsRateLimitError(%v) = false, want true", err)
			}
			if rateLimit.Status != http.StatusTooManyRequests {
				t.Errorf("Status = %d, want %d", rateLimit.Status, http.StatusTooManyRequests)
			}
			switch {
			case tc.wantMS == nil && rateLimit.RetryAfterMS != nil:
				t.Errorf("RetryAfterMS = %v, want nil", *rateLimit.RetryAfterMS)
			case tc.wantMS != nil && rateLimit.RetryAfterMS == nil:
				t.Errorf("RetryAfterMS = nil, want %v", *tc.wantMS)
			case tc.wantMS != nil && *rateLimit.RetryAfterMS != *tc.wantMS:
				t.Errorf("RetryAfterMS = %v, want %v", *rateLimit.RetryAfterMS, *tc.wantMS)
			}
			delay, hasDelay := rateLimit.RetryAfter()
			if hasDelay != tc.wantHasDel || delay != tc.wantDelay {
				t.Errorf("RetryAfter() = (%v, %v), want (%v, %v)", delay, hasDelay, tc.wantDelay, tc.wantHasDel)
			}
		})
	}
}

func TestRateLimitErrorRetryAfterFromHTTPDate(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	client, _ := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponseWithHeaders(http.StatusTooManyRequests, `{}`, map[string]string{HeaderRetryAfter: future})
	})
	_, err := client.Models.List(t.Context())
	rateLimit, ok := AsRateLimitError(err)
	if !ok {
		t.Fatalf("AsRateLimitError(%v) = false, want true", err)
	}
	delay, hasDelay := rateLimit.RetryAfter()
	if !hasDelay || delay < 25*time.Second || delay > 31*time.Second {
		t.Errorf("RetryAfter() = (%v, %v), want roughly 30s", delay, hasDelay)
	}
}

func TestErrorTraversalToAPIError(t *testing.T) {
	t.Parallel()
	base := APIError{
		Status:    403,
		Body:      errorsTestMessageBody("forbidden"),
		Endpoint:  "GET https://api.typesafe.ai/v1/models",
		RequestID: "req-3",
	}
	cases := []struct {
		name string
		err  error
	}{
		{"APIError", &base},
		{"BadRequestError", &BadRequestError{APIError: base}},
		{"AuthenticationError", &AuthenticationError{APIError: base}},
		{"PermissionDeniedError", &PermissionDeniedError{APIError: base}},
		{"NotFoundError", &NotFoundError{APIError: base}},
		{"UnprocessableEntityError", &UnprocessableEntityError{APIError: base}},
		{"InternalServerError", &InternalServerError{APIError: base}},
		{"RateLimitError", &RateLimitError{APIError: base}},
		{"APIResponseValidationError", &APIResponseValidationError{APIError: base, FieldPath: "answers.q"}},
		{"wrapped", fmt.Errorf("call failed: %w", &BadRequestError{APIError: base})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			apiErr, ok := AsAPIError(tc.err)
			if !ok {
				t.Fatalf("AsAPIError(%v) = false, want true", tc.err)
			}
			if apiErr.Status != base.Status {
				t.Errorf("Status = %d, want %d", apiErr.Status, base.Status)
			}
			if apiErr.RequestID != base.RequestID {
				t.Errorf("RequestID = %q, want %q", apiErr.RequestID, base.RequestID)
			}

			var extracted *APIError
			if !errors.As(tc.err, &extracted) {
				t.Errorf("errors.As(%v, *APIError) = false, want true", tc.err)
			}

			var sdkErr TypeSafeError
			if !errors.As(tc.err, &sdkErr) {
				t.Errorf("errors.As(%v, TypeSafeError) = false, want true", tc.err)
			}
		})
	}
}

func TestErrorExtractorsReportNoMatch(t *testing.T) {
	t.Parallel()
	sdkErr := &SDKError{Message: "bad configuration"}
	connectionErr := &APIConnectionError{Message: "Connection error"}
	timeoutErr := &APITimeoutError{Timeout: 2 * time.Second}
	plain := errors.New("plain failure")
	validationErr := &APIResponseValidationError{APIError: APIError{Status: 200}, FieldPath: "answers.q"}
	rateLimitErr := &RateLimitError{APIError: APIError{Status: 429}}
	internalErr := &InternalServerError{APIError: APIError{Status: 500}}

	apiErrorFalse := []error{sdkErr, connectionErr, timeoutErr, plain, fmt.Errorf("call failed: %w", sdkErr)}
	for _, err := range apiErrorFalse {
		if apiErr, ok := AsAPIError(err); ok {
			t.Errorf("AsAPIError(%v) = (%v, true), want false", err, apiErr)
		}
	}
	if apiErr, ok := AsAPIError(validationErr); !ok || apiErr.Status != 200 {
		t.Errorf("AsAPIError(validation) = (%v, %v), want the embedded *APIError", apiErr, ok)
	}
	if _, ok := AsAPIError(rateLimitErr); !ok {
		t.Errorf("AsAPIError(rate limit) = false, want true")
	}

	for _, err := range []error{sdkErr, connectionErr, timeoutErr, internalErr, plain} {
		if rateLimit, ok := AsRateLimitError(err); ok {
			t.Errorf("AsRateLimitError(%v) = (%v, true), want false", err, rateLimit)
		}
	}
	if rateLimit, ok := AsRateLimitError(rateLimitErr); !ok || rateLimit.Status != 429 {
		t.Errorf("AsRateLimitError(rate limit) = (%v, %v), want the *RateLimitError", rateLimit, ok)
	}

	for _, err := range []error{sdkErr, connectionErr, internalErr, plain} {
		if timeout, ok := AsTimeoutError(err); ok {
			t.Errorf("AsTimeoutError(%v) = (%v, true), want false", err, timeout)
		}
	}
	if timeout, ok := AsTimeoutError(timeoutErr); !ok || timeout.Timeout != 2*time.Second {
		t.Errorf("AsTimeoutError(timeout) = (%v, %v), want the *APITimeoutError", timeout, ok)
	}

	for _, err := range []error{sdkErr, connectionErr, timeoutErr, plain, internalErr} {
		if validation, ok := AsValidationError(err); ok {
			t.Errorf("AsValidationError(%v) = (%v, true), want false", err, validation)
		}
	}
	if validation, ok := AsValidationError(validationErr); !ok || validation.FieldPath != "answers.q" {
		t.Errorf("AsValidationError(validation) = (%v, %v), want the *APIResponseValidationError", validation, ok)
	}
}

func TestTypeSafeErrorInterfaceMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   error
		match bool
	}{
		{"SDKError", &SDKError{Message: "boom"}, true},
		{"APIConnectionError", &APIConnectionError{Message: "Connection error"}, true},
		{"APITimeoutError", &APITimeoutError{Timeout: time.Second}, true},
		{"APIError", &APIError{Status: 500}, true},
		{"BadRequestError", &BadRequestError{APIError: APIError{Status: 400}}, true},
		{"RateLimitError", &RateLimitError{APIError: APIError{Status: 429}}, true},
		{"APIResponseValidationError", &APIResponseValidationError{APIError: APIError{Status: 200}}, true},
		{"wrapped SDK error", fmt.Errorf("wrapped: %w", &SDKError{Message: "boom"}), true},
		{"plain error", errors.New("plain"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var target TypeSafeError
			if got := errors.As(tc.err, &target); got != tc.match {
				t.Errorf("errors.As(%v, TypeSafeError) = %v, want %v", tc.err, got, tc.match)
			}
		})
	}
}

func TestSDKErrorMessageAndCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("underlying failure")

	t.Run("without a cause", func(t *testing.T) {
		t.Parallel()
		err := &SDKError{Message: "bad configuration"}
		if got := err.Error(); got != "bad configuration" {
			t.Errorf("Error() = %q, want %q", got, "bad configuration")
		}
		if err.Unwrap() != nil {
			t.Errorf("Unwrap() = %v, want nil", err.Unwrap())
		}
	})

	t.Run("with a cause", func(t *testing.T) {
		t.Parallel()
		err := &SDKError{Message: "The response body could not be decoded as JSON", Cause: cause}
		const want = "The response body could not be decoded as JSON: underlying failure"
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if !errors.Is(err, cause) {
			t.Errorf("errors.Is(err, cause) = false, want true")
		}
		if got := err.Unwrap(); got != cause {
			t.Errorf("Unwrap() = %v, want %v", got, cause)
		}
	})

	t.Run("wrapped SDK error is findable", func(t *testing.T) {
		t.Parallel()
		wrapped := fmt.Errorf("client call failed: %w", &SDKError{Message: "bad", Cause: cause})
		sdkErr, ok := errors.AsType[*SDKError](wrapped)
		if !ok {
			t.Fatalf("errors.AsType[*SDKError](%v) = false, want true", wrapped)
		}
		if sdkErr.Message != "bad" {
			t.Errorf("Message = %q, want %q", sdkErr.Message, "bad")
		}
		if !errors.Is(wrapped, cause) {
			t.Errorf("errors.Is(wrapped, cause) = false, want true")
		}
	})
}

func TestAPIConnectionErrorMessageAndCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection refused")

	withoutCause := &APIConnectionError{Message: "Connection error"}
	if got := withoutCause.Error(); got != "Connection error" {
		t.Errorf("Error() = %q, want %q", got, "Connection error")
	}
	if withoutCause.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", withoutCause.Unwrap())
	}

	withCause := &APIConnectionError{Message: "Connection error", Cause: cause}
	if got, want := withCause.Error(), "Connection error: connection refused"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(withCause, cause) {
		t.Errorf("errors.Is(err, cause) = false, want true")
	}
}

func TestAPITimeoutErrorMessageText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		timeout time.Duration
		want    string
	}{
		{"seconds", 2 * time.Second, "Request timed out (timeout=2s)."},
		{"sub-second", 1500 * time.Millisecond, "Request timed out (timeout=1.5s)."},
		{"milliseconds", 250 * time.Millisecond, "Request timed out (timeout=250ms)."},
		{"zero", 0, "Request timed out (timeout=0s)."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := &APITimeoutError{Timeout: tc.timeout}
			if got := err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if !errors.As(err, new(TypeSafeError)) {
				t.Errorf("timeout error does not match TypeSafeError")
			}
		})
	}

	t.Run("timeout text ignores the cause", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("i/o timeout")
		err := &APITimeoutError{
			APIConnectionError: APIConnectionError{Cause: cause},
			Timeout:            5 * time.Second,
		}
		if got, want := err.Error(), "Request timed out (timeout=5s)."; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if !errors.Is(err, cause) {
			t.Errorf("errors.Is(err, cause) = false, want true")
		}
		if got := err.Unwrap(); got != cause {
			t.Errorf("Unwrap() = %v, want %v", got, cause)
		}
	})
}

func TestAPIResponseValidationErrorMessage(t *testing.T) {
	t.Parallel()
	t.Run("without request context", func(t *testing.T) {
		t.Parallel()
		err := &APIResponseValidationError{
			APIError:  APIError{Status: 200},
			FieldPath: "answers.tone.confidence",
		}
		const want = "200 Invalid response data at 'answers.tone.confidence'."
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("with request context", func(t *testing.T) {
		t.Parallel()
		err := &APIResponseValidationError{
			APIError: APIError{
				Status:    200,
				Endpoint:  "POST https://api.typesafe.ai/v1/systemone",
				RequestID: "req-v",
			},
			FieldPath: "answers.q.noul",
		}
		const want = "POST https://api.typesafe.ai/v1/systemone: 200 Invalid response data at 'answers.q.noul'. (request_id=req-v)"
		if got := err.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		validation, ok := AsValidationError(err)
		if !ok {
			t.Fatalf("AsValidationError(%v) = false, want true", err)
		}
		if validation.FieldPath != "answers.q.noul" {
			t.Errorf("FieldPath = %q, want %q", validation.FieldPath, "answers.q.noul")
		}
		apiErr, ok := AsAPIError(err)
		if !ok || apiErr.RequestID != "req-v" {
			t.Errorf("AsAPIError(%v) = (%v, %v), want the embedded *APIError", err, apiErr, ok)
		}
	})
}

func TestAPIErrorMessageOverridePreservesBodyAndHeaders(t *testing.T) {
	t.Parallel()
	body := errorsTestMessageBody("Server explanation")
	headers := errorsTestHeader(HeaderRetryAfterMs, "125")
	raw := []byte(`{"message":"Server explanation"}`)
	err, ok := newAPIError(http.StatusTooManyRequests, body, raw, headers, "").(*RateLimitError)
	if !ok {
		t.Fatalf("newAPIError(429) type = %T, want *RateLimitError", err)
	}
	err.Message = "A custom explanation"

	if got := err.Error(); got != "429 A custom explanation" {
		t.Errorf("Error() = %q, want %q", got, "429 A custom explanation")
	}
	if err.Status != 429 {
		t.Errorf("Status = %d, want 429", err.Status)
	}
	if !reflect.DeepEqual(err.Body, body) {
		t.Errorf("Body = %#v, want %#v", err.Body, body)
	}
	if !reflect.DeepEqual(err.Headers, headers) {
		t.Errorf("Headers = %v, want %v", err.Headers, headers)
	}
	if err.RequestID != "" {
		t.Errorf("RequestID = %q, want empty", err.RequestID)
	}
	if err.RetryAfterMS == nil || *err.RetryAfterMS != 125 {
		t.Errorf("RetryAfterMS = %v, want 125", err.RetryAfterMS)
	}
}

func TestAPIErrorNoBodyFallbackRawRendering(t *testing.T) {
	t.Parallel()
	t.Run("raw body is re-encoded when absent", func(t *testing.T) {
		t.Parallel()
		err := &APIError{Status: 400, Body: map[string]any{"unknown": "value"}}
		if got, want := err.Error(), `400 {"unknown":"value"}`; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("unencodable body falls back to a Go rendering", func(t *testing.T) {
		t.Parallel()
		err := &APIError{Status: 400, Body: map[string]any{"channel": make(chan int)}}
		if got := err.Error(); !strings.HasPrefix(got, "400 map[channel:") {
			t.Errorf("Error() = %q, want a Go-formatted rendering of the body", got)
		}
	})
}

func TestOverloadedError(t *testing.T) {
	t.Parallel()
	headers := errorsTestHeader(HeaderRetryAfterMs, "350", HeaderRequestID, "req-overloaded-1")
	raw := []byte(`{"message":"TypeSafe is temporarily overloaded"}`)
	err := newAPIError(StatusOverloaded, decodeErrorBody(raw), raw, headers, "POST https://api.typesafe.ai/v1/systemone")

	overloaded, ok := AsOverloadedError(err)
	if !ok {
		t.Fatalf("AsOverloadedError(%v) = false, want true", err)
	}
	if overloaded.Status != StatusOverloaded {
		t.Errorf("Status = %d, want %d", overloaded.Status, StatusOverloaded)
	}
	if overloaded.RequestID != "req-overloaded-1" {
		t.Errorf("RequestID = %q, want %q", overloaded.RequestID, "req-overloaded-1")
	}
	if wait, hasWait := overloaded.RetryAfter(); !hasWait || wait != 350*time.Millisecond {
		t.Errorf("RetryAfter() = (%v, %v), want (350ms, true)", wait, hasWait)
	}
	const wantMsg = "POST https://api.typesafe.ai/v1/systemone: 529 TypeSafe is temporarily overloaded (request_id=req-overloaded-1)"
	if got := err.Error(); got != wantMsg {
		t.Errorf("Error() = %q, want %q", got, wantMsg)
	}

	// Verify AsRateLimitError compatibility with OverloadedError
	rateLimit, ok := AsRateLimitError(err)
	if !ok {
		t.Fatalf("AsRateLimitError(%v) = false, want true for 529", err)
	}
	if rateLimit.Status != StatusOverloaded {
		t.Errorf("rateLimit.Status = %d, want %d", rateLimit.Status, StatusOverloaded)
	}
	if wait, hasWait := rateLimit.RetryAfter(); !hasWait || wait != 350*time.Millisecond {
		t.Errorf("rateLimit.RetryAfter() = (%v, %v), want (350ms, true)", wait, hasWait)
	}
}
