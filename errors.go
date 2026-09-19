package typesafe

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TypeSafeError is the marker interface implemented by every error this SDK returns.
//
// Match it with [errors.As]:
//
//	if err != nil {
//		var sdkErr typesafe.TypeSafeError
//		if errors.As(err, &sdkErr) {
//			// the failure came from the SDK
//		}
//	}
type TypeSafeError interface {
	error
	isTypeSafeError()
}

// SDKError is a client-side failure: invalid configuration, an unencodable body, or a
// response that could not be read. It never represents an unsuccessful HTTP status.
type SDKError struct {
	// Message describes the failure.
	Message string

	// Cause is the wrapped error, when one exists.
	Cause error
}

func (e *SDKError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

// Unwrap returns the wrapped cause so [errors.Is] and [errors.As] traverse it.
func (e *SDKError) Unwrap() error { return e.Cause }

func (e *SDKError) isTypeSafeError() {}

// APIError is an unsuccessful HTTP response, carrying the full response metadata.
//
// Typed subclasses such as [RateLimitError] embed APIError and unwrap to it, so a single
//
//	var apiErr *typesafe.APIError
//	errors.As(err, &apiErr)
//
// matches every HTTP failure the SDK returns.
type APIError struct {
	// Status is the HTTP response status code.
	Status int

	// Body is the decoded JSON error body, the raw text for a non-JSON body, or nil for an empty body.
	Body any

	// RawBody is the exact response body as received.
	RawBody []byte

	// Headers holds the response headers.
	Headers http.Header

	// Endpoint is "METHOD URL" with credentials, query parameters, and fragments removed.
	// Empty when the response has no originating request.
	Endpoint string

	// RequestID is the x-typesafe-request-id response header, or empty when absent.
	RequestID string

	// Message is the human-readable detail shown by Error. The SDK fills it from Body when the
	// error is built; a caller constructing an APIError may leave it empty to derive it lazily or
	// set it to override the derived text.
	Message string
}

func (e *APIError) isTypeSafeError() {}

// Error returns the status and message with the available request context.
func (e *APIError) Error() string { return e.format(e.detail()) }

// detail returns the effective message: the explicit override, else the body-derived message.
func (e *APIError) detail() string {
	if e.Message != "" {
		return e.Message
	}
	if message := extractMessage(e.Body); message != "" {
		return message
	}
	if e.Body == nil {
		return "status code (no body)"
	}
	raw := rawBodyString(e.Body, e.RawBody)
	if len(raw) > MaxErrorBodyLength {
		return raw[:MaxErrorBodyLength] + "…"
	}
	return raw
}

// format renders "<endpoint>: <status> <detail> (request_id=<id>)" with the optional parts omitted.
func (e *APIError) format(detail string) string {
	if detail == "" {
		detail = strconv.Itoa(e.Status)
	} else {
		detail = strconv.Itoa(e.Status) + " " + detail
	}
	if e.Endpoint != "" {
		detail = e.Endpoint + ": " + detail
	}
	if e.RequestID != "" {
		detail += " (request_id=" + e.RequestID + ")"
	}
	return detail
}

// rawBodyString renders a decoded body as the raw text used in error messages.
func rawBodyString(body any, raw []byte) string {
	if text, ok := body.(string); ok {
		return text
	}
	if len(raw) > 0 {
		return string(raw)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("%v", body)
	}
	return string(encoded)
}

// BadRequestError is an HTTP 400 response.
type BadRequestError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *BadRequestError) Unwrap() error { return &e.APIError }

// AuthenticationError is an HTTP 401 response.
type AuthenticationError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *AuthenticationError) Unwrap() error { return &e.APIError }

// PermissionDeniedError is an HTTP 403 response.
type PermissionDeniedError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *PermissionDeniedError) Unwrap() error { return &e.APIError }

// NotFoundError is an HTTP 404 response.
type NotFoundError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *NotFoundError) Unwrap() error { return &e.APIError }

// UnprocessableEntityError is an HTTP 422 response.
type UnprocessableEntityError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *UnprocessableEntityError) Unwrap() error { return &e.APIError }

// RateLimitError is an HTTP 429 response.
type RateLimitError struct {
	APIError

	// RetryAfterMS is the wait the server requested in milliseconds, or nil when the
	// response carried no usable Retry-After or retry-after-ms header.
	RetryAfterMS *float64
}

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *RateLimitError) Unwrap() error { return &e.APIError }

// RetryAfter returns the server-requested wait and whether the response provided one.
func (e *RateLimitError) RetryAfter() (time.Duration, bool) {
	if e.RetryAfterMS == nil {
		return 0, false
	}
	return time.Duration(*e.RetryAfterMS * float64(time.Millisecond)), true
}

// InternalServerError is an HTTP 5xx response.
type InternalServerError struct{ APIError }

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *InternalServerError) Unwrap() error { return &e.APIError }

// APIConnectionError is returned when the request never produced an HTTP response.
type APIConnectionError struct {
	// Message describes the transport failure.
	Message string

	// Cause is the underlying transport error.
	Cause error
}

func (e *APIConnectionError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

// Unwrap returns the underlying transport error.
func (e *APIConnectionError) Unwrap() error { return e.Cause }

func (e *APIConnectionError) isTypeSafeError() {}

// APITimeoutError is returned when a request exceeds its configured timeout.
type APITimeoutError struct {
	APIConnectionError

	// Timeout is the timeout that was in effect for the request.
	Timeout time.Duration
}

// Error returns the formatted timeout message.
func (e *APITimeoutError) Error() string {
	return fmt.Sprintf("Request timed out (timeout=%v).", e.Timeout)
}

// APIResponseValidationError is returned for a successful HTTP status whose body does not
// match the expected schema.
type APIResponseValidationError struct {
	APIError

	// FieldPath is the dotted path to the offending field, such as "answers.tone.confidence".
	FieldPath string
}

// Error returns the formatted validation message.
func (e *APIResponseValidationError) Error() string {
	return e.format(fmt.Sprintf("Invalid response data at '%s'.", e.FieldPath))
}

// Unwrap exposes the embedded [APIError] to [errors.As].
func (e *APIResponseValidationError) Unwrap() error { return &e.APIError }

// AsAPIError extracts the [APIError] carried by any HTTP failure this SDK returns.
// It reports false for connection, timeout, validation-of-input, and configuration errors.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// AsRateLimitError extracts the [RateLimitError] when the failure was an HTTP 429.
func AsRateLimitError(err error) (*RateLimitError, bool) {
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		return rateLimit, true
	}
	return nil, false
}

// AsTimeoutError extracts the [APITimeoutError] when the request exceeded its timeout.
func AsTimeoutError(err error) (*APITimeoutError, bool) {
	var timeout *APITimeoutError
	if errors.As(err, &timeout) {
		return timeout, true
	}
	return nil, false
}

// AsValidationError extracts the [APIResponseValidationError] when a successful response
// could not be decoded.
func AsValidationError(err error) (*APIResponseValidationError, bool) {
	var validation *APIResponseValidationError
	if errors.As(err, &validation) {
		return validation, true
	}
	return nil, false
}

// cleanEndpoint returns "METHOD URL" with userinfo, query parameters, and fragments removed,
// so credentials embedded in a URL never reach an error message or a log line.
func cleanEndpoint(method, rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return method + " " + rawURL
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return method + " " + parsed.String()
}

// parseRetryAfter reads the retry-after-ms and Retry-After response headers into a delay.
//
// retry-after-ms is interpreted as milliseconds; Retry-After is interpreted as seconds, or as an
// HTTP date. A header that is present but empty means "retry now" and yields a zero delay. A
// negative Retry-After disables the hint, while a negative retry-after-ms falls through to
// Retry-After. Malformed values are ignored.
func parseRetryAfter(headers http.Header) (time.Duration, bool) {
	if raw, present := headerValue(headers, HeaderRetryAfterMs); present {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(cmp.Or(raw, "0")), 64); err == nil && isFinite(seconds) && seconds >= 0 {
			return scaleDuration(seconds, float64(time.Millisecond))
		}
	}
	raw, present := headerValue(headers, HeaderRetryAfter)
	if !present {
		return 0, false
	}
	raw = strings.TrimSpace(cmp.Or(raw, "0"))
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && isFinite(seconds) {
		if seconds < 0 {
			return 0, false
		}
		return scaleDuration(seconds, float64(time.Second))
	}
	if when, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay, true
		}
		return 0, true
	}
	return 0, false
}

// headerValue returns a header's first value along with whether the header was present at all,
// so an empty value can be told apart from an absent header.
func headerValue(headers http.Header, name string) (string, bool) {
	if headers == nil {
		return "", false
	}
	if values, ok := headers[textproto.CanonicalMIMEHeaderKey(name)]; ok {
		if len(values) == 0 {
			return "", true
		}
		return values[0], true
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			if len(values) == 0 {
				return "", true
			}
			return values[0], true
		}
	}
	return "", false
}

// scaleDuration converts a value in unit-sized seconds to a Duration, rejecting overflow.
func scaleDuration(value, unit float64) (time.Duration, bool) {
	nanos := value * unit
	if !isFinite(nanos) || nanos > float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(nanos), true
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// extractMessage pulls a concise, human-readable message out of a decoded error body.
//
// It understands the shapes the API and common proxies produce: a bare string, {"error": ...},
// {"message": ...}, {"detail": ...}, and FastAPI-style validation lists, which are rendered as
// "path: message; path: message" with the leading "body" location segment removed.
//
// A field that is present but empty yields the empty string, leaving the caller to fall back to
// rendering the body; only a validation list with no usable entries yields nothing.
func extractMessage(body any) string {
	if body == nil {
		return ""
	}
	if text, ok := body.(string); ok {
		return text
	}
	object, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	if value, ok := object["error"]; ok {
		if text, ok := value.(string); ok {
			return text
		}
		if nested, ok := value.(map[string]any); ok {
			if text, ok := nested["message"].(string); ok {
				return text
			}
		}
	}
	if text, ok := object["message"].(string); ok {
		return text
	}
	switch detail := object["detail"].(type) {
	case string:
		return detail
	case map[string]any:
		if text, ok := detail["message"].(string); ok {
			return text
		}
	case []any:
		parts := make([]string, 0, len(detail))
		for _, item := range detail {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			message, ok := entry["msg"].(string)
			if !ok {
				continue
			}
			if path := locationPath(entry["loc"]); path != "" {
				parts = append(parts, path+": "+message)
			} else {
				parts = append(parts, message)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return ""
}

// locationPath renders a validation error's location array as a dotted path, dropping the
// "body" segment the server uses to name the request location.
func locationPath(location any) string {
	segments, ok := location.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		text := fmt.Sprint(segment)
		if text != "body" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, ".")
}

// newAPIError maps an unsuccessful status code onto the matching error type.
func newAPIError(status int, body any, rawBody []byte, headers http.Header, endpoint string) error {
	base := APIError{
		Status:    status,
		Body:      body,
		RawBody:   rawBody,
		Headers:   headers,
		Endpoint:  endpoint,
		RequestID: headers.Get(HeaderRequestID),
	}
	// Materialize the derived message once, so repeated Error() calls never re-extract it.
	base.Message = base.detail()
	switch status {
	case http.StatusBadRequest:
		return &BadRequestError{APIError: base}
	case http.StatusUnauthorized:
		return &AuthenticationError{APIError: base}
	case http.StatusForbidden:
		return &PermissionDeniedError{APIError: base}
	case http.StatusNotFound:
		return &NotFoundError{APIError: base}
	case http.StatusUnprocessableEntity:
		return &UnprocessableEntityError{APIError: base}
	case http.StatusTooManyRequests:
		rateLimit := &RateLimitError{APIError: base}
		if delay, ok := parseRetryAfter(headers); ok {
			rateLimit.RetryAfterMS = new(float64(delay) / float64(time.Millisecond))
		}
		return rateLimit
	default:
		if status >= http.StatusInternalServerError {
			return &InternalServerError{APIError: base}
		}
		return &base
	}
}

// decodeErrorBody converts response bytes into the value carried by an [APIError]: the decoded
// JSON for a JSON body, the raw text otherwise, and nil for an empty body.
func decodeErrorBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	return decoded
}
