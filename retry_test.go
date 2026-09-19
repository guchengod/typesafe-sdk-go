package typesafe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// retryTestPolicy returns a default policy with deterministic jitter and a sleep seam that
// records every delay instead of waiting.
func retryTestPolicy(delays *[]time.Duration) *RetryPolicy {
	policy := DefaultRetryPolicy()
	policy.RandFloat = func() float64 { return 0 }
	policy.Sleep = func(_ context.Context, delay time.Duration) error {
		*delays = append(*delays, delay)
		return nil
	}
	return policy
}

// retryTestBackoffPolicy returns a policy that computes backoff from the given bounds with a
// pinned jitter sample.
func retryTestBackoffPolicy(initial, maximum time.Duration, jitter, sample float64) *RetryPolicy {
	return &RetryPolicy{
		BackoffInitial: initial,
		BackoffMax:     maximum,
		BackoffJitter:  jitter,
		RandFloat:      func() float64 { return sample },
	}
}

// retryTestFailure returns a function that fails with failure until the attempt index reaches
// succeedAt, and a counter of the attempts it observed. A negative succeedAt never succeeds.
func retryTestFailure(failure error, succeedAt int) (*int, func(int) error) {
	attempts := new(int)
	return attempts, func(attempt int) error {
		*attempts++
		if succeedAt >= 0 && attempt >= succeedAt {
			return nil
		}
		return failure
	}
}

// retryTestStatusError builds the API error a failed response produces.
func retryTestStatusError(status int, headers http.Header) *APIError {
	return &APIError{
		Status:   status,
		Body:     map[string]any{"message": "failed"},
		RawBody:  []byte(`{"message":"failed"}`),
		Headers:  headers,
		Endpoint: "GET https://api.typesafe.ai/v1/models",
	}
}

// retryTestRateLimit builds the rate-limit error a 429 response produces.
func retryTestRateLimit(headers http.Header) *RateLimitError {
	return &RateLimitError{APIError: *retryTestStatusError(http.StatusTooManyRequests, headers)}
}

// retryTestFailTransport is a transport that always fails with the same error.
type retryTestFailTransport struct {
	err   error
	calls int
}

func (t *retryTestFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, t.err
}

// retryTestClient builds a client over the transport, with retries disabled unless overridden.
func retryTestClient(t *testing.T, transport http.RoundTripper, options ...Option) *Client {
	t.Helper()
	base := []Option{
		WithAPIKey("test-key"),
		WithTransport(transport),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithRetryPolicy(noRetryPolicy()),
	}
	client, err := NewClient(append(base, options...)...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRetryPolicyValidateRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*RetryPolicy)
		want   string
	}{
		{"valid defaults", func(*RetryPolicy) {}, ""},
		{"negative max retries", func(p *RetryPolicy) { p.MaxRetries = -1 }, "max_retries must be a non-negative integer."},
		{"negative backoff initial", func(p *RetryPolicy) { p.BackoffInitial = -time.Millisecond }, "backoff_initial must be a non-negative, finite number of seconds."},
		{"negative backoff max", func(p *RetryPolicy) { p.BackoffMax = -time.Second }, "backoff_max must be a non-negative, finite number of seconds."},
		{"jitter below zero", func(p *RetryPolicy) { p.BackoffJitter = -0.1 }, "backoff_jitter must be between zero and one."},
		{"jitter above one", func(p *RetryPolicy) { p.BackoffJitter = 1.1 }, "backoff_jitter must be between zero and one."},
		{"jitter not a number", func(p *RetryPolicy) { p.BackoffJitter = math.NaN() }, "backoff_jitter must be between zero and one."},
		{"jitter positive infinity", func(p *RetryPolicy) { p.BackoffJitter = math.Inf(1) }, "backoff_jitter must be between zero and one."},
		{"jitter negative infinity", func(p *RetryPolicy) { p.BackoffJitter = math.Inf(-1) }, "backoff_jitter must be between zero and one."},
		{"zero timeout", func(p *RetryPolicy) { p.Timeout = new(time.Duration) }, "timeout must be a positive, finite number of seconds."},
		{"negative timeout", func(p *RetryPolicy) { p.Timeout = new(-time.Second) }, "timeout must be a positive, finite number of seconds."},
		{"nil timeout is allowed", func(p *RetryPolicy) { p.Timeout = nil }, ""},
		{"zero retries is allowed", func(p *RetryPolicy) { p.MaxRetries = 0 }, ""},
		{"zero backoff is allowed", func(p *RetryPolicy) { p.BackoffInitial, p.BackoffMax = 0, 0 }, ""},
		{"boundary jitter values are allowed", func(p *RetryPolicy) { p.BackoffJitter = 1 }, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultRetryPolicy()
			tc.mutate(policy)
			err := policy.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Errorf("Validate() = %q, want %q", err.Error(), tc.want)
			}
			sdkErr, ok := errors.AsType[*SDKError](err)
			if !ok {
				t.Fatalf("Validate() error %T is not an *SDKError", err)
			}
			if sdkErr.Cause != nil {
				t.Errorf("Cause = %v, want nil", sdkErr.Cause)
			}
			var target TypeSafeError
			if !errors.As(err, &target) {
				t.Errorf("validation error does not match TypeSafeError")
			}
		})
	}
}

func TestRetryPolicyValidateAcceptsZeroValuePolicy(t *testing.T) {
	t.Parallel()
	policy := &RetryPolicy{}
	if err := policy.Validate(); err != nil {
		t.Errorf("(&RetryPolicy{}).Validate() = %v, want nil", err)
	}
	boundary := &RetryPolicy{BackoffJitter: 1, Timeout: new(time.Nanosecond)}
	if err := boundary.Validate(); err != nil {
		t.Errorf("boundary Validate() = %v, want nil", err)
	}
}

func TestRetryPolicyCloneDeepCopies(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sentinel")
	predicate := func(error) bool { return true }
	sleep := func(context.Context, time.Duration) error { return nil }
	original := DefaultRetryPolicy()
	original.RetryErrors = []error{sentinel}
	original.Predicate = predicate
	original.Sleep = sleep

	clone := original.Clone()
	if clone == original {
		t.Fatal("Clone() returned the same pointer")
	}
	if clone.MaxRetries != original.MaxRetries || clone.BackoffInitial != original.BackoffInitial ||
		clone.BackoffMax != original.BackoffMax || clone.BackoffJitter != original.BackoffJitter ||
		clone.RespectRetryAfter != original.RespectRetryAfter || clone.APIConnectionError != original.APIConnectionError ||
		clone.APITimeoutError != original.APITimeoutError {
		t.Errorf("Clone() scalars differ: %+v vs %+v", clone, original)
	}

	clone.HTTPStatuses[http.StatusInternalServerError] = false
	if !original.HTTPStatuses[http.StatusInternalServerError] {
		t.Errorf("mutating the clone changed the original HTTPStatuses")
	}
	clone.RetryErrors = append(clone.RetryErrors, errors.New("extra"))
	if len(original.RetryErrors) != 1 {
		t.Errorf("mutating the clone changed the original RetryErrors: %v", original.RetryErrors)
	}
	if clone.Timeout == original.Timeout {
		t.Errorf("Clone() shares the Timeout pointer")
	}
	*clone.Timeout = time.Second
	if *original.Timeout != DefaultRetryBudget {
		t.Errorf("mutating the clone changed the original Timeout: %v", *original.Timeout)
	}
	if clone.Predicate == nil || !clone.Predicate(errors.New("x")) {
		t.Errorf("Clone() dropped the Predicate")
	}
	if clone.Sleep == nil {
		t.Errorf("Clone() dropped the Sleep seam")
	}

	var nilPolicy *RetryPolicy
	if nilPolicy.Clone() != nil {
		t.Errorf("(*RetryPolicy)(nil).Clone() != nil")
	}

	sparse := (&RetryPolicy{MaxRetries: 3}).Clone()
	if sparse.HTTPStatuses != nil || sparse.RetryErrors != nil || sparse.Timeout != nil ||
		sparse.Predicate != nil || sparse.Sleep != nil || sparse.RandFloat != nil {
		t.Errorf("Clone() of a sparse policy = %+v, want zero optional fields", sparse)
	}
}

func TestRetryDefaultPolicyAndStatuses(t *testing.T) {
	t.Parallel()

	t.Run("default policy values", func(t *testing.T) {
		t.Parallel()
		policy := DefaultRetryPolicy()
		if policy.MaxRetries != 2 {
			t.Errorf("MaxRetries = %d, want 2", policy.MaxRetries)
		}
		if policy.BackoffInitial != 500*time.Millisecond {
			t.Errorf("BackoffInitial = %v, want 500ms", policy.BackoffInitial)
		}
		if policy.BackoffMax != 5*time.Second {
			t.Errorf("BackoffMax = %v, want 5s", policy.BackoffMax)
		}
		if policy.BackoffJitter != 0.25 {
			t.Errorf("BackoffJitter = %v, want 0.25", policy.BackoffJitter)
		}
		if !policy.RespectRetryAfter || !policy.APIConnectionError || !policy.APITimeoutError {
			t.Errorf("default policy flags = %+v, want all enabled", policy)
		}
		if policy.Timeout == nil || *policy.Timeout != DefaultRetryBudget {
			t.Errorf("Timeout = %v, want %v", policy.Timeout, DefaultRetryBudget)
		}
		if policy.Sleep != nil || policy.RandFloat != nil || policy.Predicate != nil || policy.RetryErrors != nil {
			t.Errorf("default policy seams = %+v, want nil", policy)
		}
		if policy.Validate() != nil {
			t.Errorf("Validate() = %v, want nil", policy.Validate())
		}
	})

	t.Run("default statuses", func(t *testing.T) {
		t.Parallel()
		statuses := DefaultRetryStatuses()
		for _, status := range []int{408, 429, 500, 501, 503, 550, 599} {
			if !statuses[status] {
				t.Errorf("DefaultRetryStatuses()[%d] = false, want true", status)
			}
		}
		for _, status := range []int{0, 200, 301, 302, 400, 401, 403, 404, 409, 422, 499, 600, 700} {
			if statuses[status] {
				t.Errorf("DefaultRetryStatuses()[%d] = true, want false", status)
			}
		}
		if got, want := len(statuses), 102; got != want {
			t.Errorf("len(DefaultRetryStatuses()) = %d, want %d", got, want)
		}
	})

	t.Run("defaults are freshly allocated", func(t *testing.T) {
		t.Parallel()
		first := DefaultRetryPolicy()
		first.MaxRetries = 9
		first.HTTPStatuses[http.StatusInternalServerError] = false
		second := DefaultRetryPolicy()
		if second.MaxRetries != 2 {
			t.Errorf("MaxRetries = %d, want 2", second.MaxRetries)
		}
		if !second.HTTPStatuses[http.StatusInternalServerError] {
			t.Errorf("mutating one policy's statuses changed another's")
		}
		if got := DefaultRetryStatuses(); !got[http.StatusInternalServerError] {
			t.Errorf("mutating a policy changed DefaultRetryStatuses()")
		}
	})
}

func TestRetryPolicyExecuteAttemptCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		maxRetries int
		attempts   int
	}{
		{0, 1},
		{1, 2},
		{2, 3},
		{4, 5},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("max retries %d", tc.maxRetries), func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = tc.maxRetries
			failure := retryTestRateLimit(nil)

			var indices []int
			attempts := 0
			err := policy.Execute(t.Context(), func(attempt int) error {
				attempts++
				indices = append(indices, attempt)
				return failure
			})

			if attempts != tc.attempts {
				t.Errorf("attempts = %d, want %d", attempts, tc.attempts)
			}
			if want := retryTestRange(tc.attempts); !slices.Equal(indices, want) {
				t.Errorf("attempt indices = %v, want %v", indices, want)
			}
			if len(delays) != tc.attempts-1 {
				t.Errorf("delays = %v, want %d entries", delays, tc.attempts-1)
			}
			if err != failure {
				t.Errorf("Execute() = %v, want the last failure %v", err, failure)
			}
		})
	}
}

func TestRetryPolicyExecuteStopsOnSuccess(t *testing.T) {
	t.Parallel()
	var delays []time.Duration
	policy := retryTestPolicy(&delays)
	attempts, fn := retryTestFailure(nil, 0)

	if err := policy.Execute(t.Context(), fn); err != nil {
		t.Errorf("Execute() = %v, want nil", err)
	}
	if *attempts != 1 {
		t.Errorf("attempts = %d, want 1", *attempts)
	}
	if len(delays) != 0 {
		t.Errorf("delays = %v, want none", delays)
	}
}

func TestRetryPolicyExecuteStopsOnNonRetryableFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		failure error
	}{
		{"bad request", retryTestStatusError(http.StatusBadRequest, nil)},
		{"unauthorized", retryTestStatusError(http.StatusUnauthorized, nil)},
		{"forbidden", retryTestStatusError(http.StatusForbidden, nil)},
		{"not found", retryTestStatusError(http.StatusNotFound, nil)},
		{"conflict", retryTestStatusError(http.StatusConflict, nil)},
		{"unprocessable entity", retryTestStatusError(http.StatusUnprocessableEntity, nil)},
		{"redirect", retryTestStatusError(http.StatusFound, nil)},
		{"teapot", retryTestStatusError(http.StatusTeapot, nil)},
		{"plain error", errors.New("not retryable")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = 3
			attempts, fn := retryTestFailure(tc.failure, -1)

			err := policy.Execute(t.Context(), fn)
			if *attempts != 1 {
				t.Errorf("attempts = %d, want 1", *attempts)
			}
			if len(delays) != 0 {
				t.Errorf("delays = %v, want none", delays)
			}
			if err != tc.failure {
				t.Errorf("Execute() = %v, want %v", err, tc.failure)
			}
		})
	}
}

func TestRetryPolicyExecuteRetriesDefaultStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []int{408, 429, 500, 501, 503, 550, 599} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = 1
			attempts, fn := retryTestFailure(retryTestStatusError(status, nil), -1)

			err := policy.Execute(t.Context(), fn)
			if *attempts != 2 {
				t.Errorf("attempts = %d, want 2", *attempts)
			}
			if apiErr, ok := AsAPIError(err); !ok || apiErr.Status != status {
				t.Errorf("Execute() = %v, want a %d failure", err, status)
			}
		})
	}
}

func TestRetryPolicyBackoffValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		initial time.Duration
		maximum time.Duration
		jitter  float64
		sample  float64
		attempt int
		want    time.Duration
	}{
		{"first retry", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 1, 500 * time.Millisecond},
		{"doubling", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 2, time.Second},
		{"doubling again", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 3, 2 * time.Second},
		{"fourth retry", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 4, 4 * time.Second},
		{"capped at the maximum", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 5, 5 * time.Second},
		{"still capped later", 500 * time.Millisecond, 5 * time.Second, 0.25, 0, 20, 5 * time.Second},
		{"jitter subtracted", 500 * time.Millisecond, 5 * time.Second, 0.25, 1, 1, 375 * time.Millisecond},
		{"jitter subtracted while doubling", 500 * time.Millisecond, 5 * time.Second, 0.25, 1, 2, 750 * time.Millisecond},
		{"jitter subtracted at the cap", 500 * time.Millisecond, 5 * time.Second, 0.25, 1, 5, 3750 * time.Millisecond},
		{"no jitter takes the full delay", 500 * time.Millisecond, 5 * time.Second, 0, 0.99, 3, 2 * time.Second},
		{"full jitter can halve", 500 * time.Millisecond, 5 * time.Second, 1, 0.5, 1, 250 * time.Millisecond},
		{"full jitter while doubling", 500 * time.Millisecond, 5 * time.Second, 1, 0.5, 2, 500 * time.Millisecond},
		{"zero initial disables backoff", 0, 5 * time.Second, 0.25, 0, 1, 0},
		{"zero maximum disables backoff", 500 * time.Millisecond, 0, 0.25, 0, 1, 0},
		{"both bounds zero disable backoff", 0, 0, 0.25, 0, 3, 0},
		{"initial above the maximum is capped", time.Second, 600 * time.Microsecond, 0, 0, 1, 600 * time.Microsecond},
		{"cap holds for later attempts", time.Second, 600 * time.Microsecond, 0, 0, 10, 600 * time.Microsecond},
		{"sub-millisecond delays round to zero", time.Nanosecond, 5 * time.Second, 0, 0, 1, 0},
		{"delays round down to the millisecond", 1400 * time.Microsecond, 5 * time.Second, 0, 0, 1, time.Millisecond},
		{"jittered delays round up to the millisecond", 2 * time.Millisecond, 5 * time.Second, 0.5, 0.01, 1, 2 * time.Millisecond},
		{"rounding never exceeds the un-jittered delay", 1600 * time.Microsecond, 5 * time.Second, 0, 0, 1, 1600 * time.Microsecond},
		{"jittered delay rounds to the millisecond", 333 * time.Millisecond, 5 * time.Second, 0.5, 0.333, 1, 278 * time.Millisecond},
		{"doubling stops at the cap", 3 * time.Second, 5 * time.Second, 0, 0, 1, 3 * time.Second},
		{"doubling beyond the cap is capped", 3 * time.Second, 5 * time.Second, 0, 0, 2, 5 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := retryTestBackoffPolicy(tc.initial, tc.maximum, tc.jitter, tc.sample)
			if got := policy.backoff(tc.attempt); got != tc.want {
				t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyDelayPrefersServerHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		respect bool
		failure error
		want    time.Duration
	}{
		{
			"seconds hint wins over backoff",
			true,
			retryTestRateLimit(errorsTestHeader(HeaderRetryAfter, "61")),
			61 * time.Second,
		},
		{
			"milliseconds hint",
			true,
			retryTestRateLimit(errorsTestHeader(HeaderRetryAfterMs, "60001")),
			60*time.Second + time.Millisecond,
		},
		{
			"milliseconds hint is honored when it is zero",
			true,
			retryTestRateLimit(errorsTestHeader(HeaderRetryAfterMs, "0", HeaderRetryAfter, "50")),
			0,
		},
		{
			"internal server error honors the hint",
			true,
			retryTestStatusError(http.StatusInternalServerError, errorsTestHeader(HeaderRetryAfter, "2")),
			2 * time.Second,
		},
		{
			"malformed hint falls back to backoff",
			true,
			retryTestRateLimit(errorsTestHeader(HeaderRetryAfter, "bad")),
			500 * time.Millisecond,
		},
		{
			"absent hint falls back to backoff",
			true,
			retryTestRateLimit(nil),
			500 * time.Millisecond,
		},
		{
			"non-API failure falls back to backoff",
			true,
			errors.New("transport failure"),
			500 * time.Millisecond,
		},
		{
			"disabled hint falls back to backoff",
			false,
			retryTestRateLimit(errorsTestHeader(HeaderRetryAfter, "5")),
			500 * time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.RespectRetryAfter = tc.respect
			if got := policy.delay(1, tc.failure); got != tc.want {
				t.Errorf("delay(1, %v) = %v, want %v", tc.failure, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyDelayFollowsCustomBackoffBounds(t *testing.T) {
	t.Parallel()
	var delays []time.Duration
	policy := retryTestPolicy(&delays)
	policy.BackoffInitial = 200 * time.Millisecond
	policy.Timeout = nil
	if got := policy.delay(1, errors.New("transport failure")); got != 200*time.Millisecond {
		t.Errorf("delay(1) = %v, want 200ms", got)
	}

	policy.BackoffInitial = 200 * time.Millisecond
	policy.BackoffJitter = 0
	if got := policy.delay(2, retryTestRateLimit(nil)); got != 400*time.Millisecond {
		t.Errorf("delay(2) = %v, want 400ms", got)
	}
}

func TestRetryPolicyExecuteHonorsServerDelay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		respect      bool
		headers      http.Header
		maxRetries   int
		clearTimeout bool
		wantDelays   []time.Duration
		wantAttempts int
	}{
		{
			name:         "retry-after seconds",
			respect:      true,
			headers:      errorsTestHeader(HeaderRetryAfter, "2"),
			maxRetries:   1,
			wantDelays:   []time.Duration{2 * time.Second},
			wantAttempts: 2,
		},
		{
			name:         "retry-after milliseconds",
			respect:      true,
			headers:      errorsTestHeader(HeaderRetryAfterMs, "125"),
			maxRetries:   1,
			wantDelays:   []time.Duration{125 * time.Millisecond},
			wantAttempts: 2,
		},
		{
			name:         "server delay is honored for every retry",
			respect:      true,
			headers:      errorsTestHeader(HeaderRetryAfter, "2"),
			maxRetries:   2,
			wantDelays:   []time.Duration{2 * time.Second, 2 * time.Second},
			wantAttempts: 3,
		},
		{
			name:         "server delay bypasses the backoff cap",
			respect:      true,
			headers:      errorsTestHeader(HeaderRetryAfter, "61"),
			maxRetries:   1,
			clearTimeout: true,
			wantDelays:   []time.Duration{61 * time.Second},
			wantAttempts: 2,
		},
		{
			name:         "disabled hint uses backoff",
			respect:      false,
			headers:      errorsTestHeader(HeaderRetryAfter, "5"),
			maxRetries:   1,
			wantDelays:   []time.Duration{500 * time.Millisecond},
			wantAttempts: 2,
		},
		{
			name:         "malformed hint uses backoff",
			respect:      true,
			headers:      errorsTestHeader(HeaderRetryAfter, "bad"),
			maxRetries:   1,
			wantDelays:   []time.Duration{500 * time.Millisecond},
			wantAttempts: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = tc.maxRetries
			policy.RespectRetryAfter = tc.respect
			if tc.clearTimeout {
				policy.Timeout = nil
			}
			attempts, fn := retryTestFailure(retryTestRateLimit(tc.headers), -1)

			if err := policy.Execute(t.Context(), fn); err == nil {
				t.Fatal("Execute() = nil, want the rate limit failure")
			}
			if *attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", *attempts, tc.wantAttempts)
			}
			if !slices.Equal(delays, tc.wantDelays) {
				t.Errorf("delays = %v, want %v", delays, tc.wantDelays)
			}
		})
	}
}

func TestRetryPolicyExecuteRespectsTimeoutBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		timeout      *time.Duration
		headers      http.Header
		maxRetries   int
		wantAttempts int
		wantDelays   []time.Duration
	}{
		{
			name:         "delay equal to the budget is abandoned",
			timeout:      new(500 * time.Millisecond),
			headers:      errorsTestHeader(HeaderRetryAfter, "0.5"),
			maxRetries:   5,
			wantAttempts: 1,
		},
		{
			name:         "delay beyond the budget is abandoned",
			timeout:      new(time.Second),
			headers:      errorsTestHeader(HeaderRetryAfter, "60"),
			maxRetries:   5,
			wantAttempts: 1,
		},
		{
			name:         "elapsed time alone can exhaust the budget",
			timeout:      new(time.Nanosecond),
			headers:      errorsTestHeader(HeaderRetryAfterMs, "0"),
			maxRetries:   5,
			wantAttempts: 1,
		},
		{
			name:         "delay under the budget retries",
			timeout:      new(30 * time.Second),
			headers:      errorsTestHeader(HeaderRetryAfter, "0.5"),
			maxRetries:   2,
			wantAttempts: 3,
			wantDelays:   []time.Duration{500 * time.Millisecond, 500 * time.Millisecond},
		},
		{
			name:         "backoff under the budget retries",
			timeout:      new(time.Hour),
			maxRetries:   2,
			wantAttempts: 3,
			wantDelays:   []time.Duration{500 * time.Millisecond, time.Second},
		},
		{
			name:         "nil budget never abandons a retry",
			headers:      errorsTestHeader(HeaderRetryAfter, "60"),
			maxRetries:   2,
			wantAttempts: 3,
			wantDelays:   []time.Duration{60 * time.Second, 60 * time.Second},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = tc.maxRetries
			policy.Timeout = tc.timeout
			failure := retryTestRateLimit(tc.headers)
			attempts, fn := retryTestFailure(failure, -1)

			err := policy.Execute(t.Context(), fn)
			if err != failure {
				t.Errorf("Execute() = %v, want the last failure", err)
			}
			if *attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", *attempts, tc.wantAttempts)
			}
			if !slices.Equal(delays, tc.wantDelays) {
				t.Errorf("delays = %v, want %v", delays, tc.wantDelays)
			}
		})
	}
}

func TestRetryPolicyExecuteAbandonsRetryWhenElapsedBudgetIsSpent(t *testing.T) {
	t.Parallel()
	policy := DefaultRetryPolicy()
	policy.MaxRetries = 10
	policy.Timeout = new(10 * time.Millisecond)
	policy.RandFloat = func() float64 { return 0 }
	var delays []time.Duration
	policy.Sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		time.Sleep(delay)
		return nil
	}
	attempts, fn := retryTestFailure(retryTestRateLimit(errorsTestHeader(HeaderRetryAfterMs, "5")), -1)

	err := policy.Execute(t.Context(), fn)
	if err == nil {
		t.Fatal("Execute() = nil, want the rate limit failure")
	}
	// One 5ms retry fits inside the 10ms budget; the next delay would reach it.
	if *attempts != 2 {
		t.Errorf("attempts = %d, want 2", *attempts)
	}
	if len(delays) != 1 || delays[0] != 5*time.Millisecond {
		t.Errorf("delays = %v, want [5ms]", delays)
	}
}

func TestRetryPolicyExecuteTransportErrorFlags(t *testing.T) {
	t.Parallel()
	timeoutFailure := &APITimeoutError{Timeout: 2 * time.Second}
	connectionFailure := &APIConnectionError{Message: "Connection error"}

	cases := []struct {
		name         string
		failure      error
		configure    func(*RetryPolicy)
		wantAttempts int
		wantDelays   []time.Duration
	}{
		{
			name:         "timeouts are retried by default",
			failure:      timeoutFailure,
			wantAttempts: 3,
			wantDelays:   []time.Duration{500 * time.Millisecond, time.Second},
		},
		{
			name:         "timeouts can be disabled",
			failure:      timeoutFailure,
			configure:    func(p *RetryPolicy) { p.APITimeoutError = false },
			wantAttempts: 1,
		},
		{
			name:         "connections are retried by default",
			failure:      connectionFailure,
			wantAttempts: 3,
			wantDelays:   []time.Duration{500 * time.Millisecond, time.Second},
		},
		{
			name:         "connections can be disabled",
			failure:      connectionFailure,
			configure:    func(p *RetryPolicy) { p.APIConnectionError = false },
			wantAttempts: 1,
		},
		{
			name:    "a disabled timeout can opt in through the predicate",
			failure: timeoutFailure,
			configure: func(p *RetryPolicy) {
				p.APITimeoutError = false
				p.MaxRetries = 1
				p.Predicate = func(err error) bool {
					_, ok := AsTimeoutError(err)
					return ok
				}
			},
			wantAttempts: 2,
			wantDelays:   []time.Duration{500 * time.Millisecond},
		},
		{
			name:    "a disabled timeout can opt in through retry errors",
			failure: timeoutFailure,
			configure: func(p *RetryPolicy) {
				p.APITimeoutError = false
				p.MaxRetries = 1
				p.RetryErrors = []error{timeoutFailure}
			},
			wantAttempts: 2,
			wantDelays:   []time.Duration{500 * time.Millisecond},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			if tc.configure != nil {
				tc.configure(policy)
			}
			attempts, fn := retryTestFailure(tc.failure, -1)

			err := policy.Execute(t.Context(), fn)
			if err != tc.failure {
				t.Errorf("Execute() = %v, want the last failure", err)
			}
			if *attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", *attempts, tc.wantAttempts)
			}
			if !slices.Equal(delays, tc.wantDelays) {
				t.Errorf("delays = %v, want %v", delays, tc.wantDelays)
			}
		})
	}
}

func TestRetryPolicyExecuteTimeoutRegressionIsRetried(t *testing.T) {
	t.Parallel()
	// Regression: a timeout carries connection semantics, and must still be retried when the
	// policy enables timeout retries.
	policy := DefaultRetryPolicy()
	policy.MaxRetries = 2
	var delays []time.Duration
	policy.Sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	policy.RandFloat = func() float64 { return 0 }
	timeoutErr := &APITimeoutError{Timeout: 5 * time.Second}
	attempts, fn := retryTestFailure(timeoutErr, -1)

	err := policy.Execute(t.Context(), fn)
	if *attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (the timeout must be retried)", *attempts)
	}
	if !slices.Equal(delays, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Errorf("delays = %v, want the default backoff", delays)
	}
	extracted, ok := AsTimeoutError(err)
	if !ok || extracted.Timeout != 5*time.Second {
		t.Errorf("AsTimeoutError(%v) = (%v, %v), want the timeout error", err, extracted, ok)
	}
}

func TestRetryPolicyExecuteRetryErrorsSentinel(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("retry me")
	other := errors.New("do not retry")

	cases := []struct {
		name         string
		failure      error
		wantAttempts int
	}{
		{"wrapped sentinel is retried", fmt.Errorf("call failed: %w", sentinel), 3},
		{"sentinel itself is retried", sentinel, 3},
		{"unrelated error is not retried", other, 1},
		{"wrapped unrelated error is not retried", fmt.Errorf("call failed: %w", other), 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.HTTPStatuses = nil
			policy.RetryErrors = []error{sentinel}
			attempts, fn := retryTestFailure(tc.failure, -1)

			err := policy.Execute(t.Context(), fn)
			if *attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", *attempts, tc.wantAttempts)
			}
			if err == nil {
				t.Fatalf("Execute() = nil, want %v", tc.failure)
			}
			if !errors.Is(err, tc.failure) {
				t.Errorf("errors.Is(%v, %v) = false, want true", err, tc.failure)
			}
		})
	}
}

func TestRetryPolicyExecutePredicate(t *testing.T) {
	t.Parallel()
	t.Run("predicate opts a 404 in", func(t *testing.T) {
		t.Parallel()
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		policy.MaxRetries = 1
		policy.HTTPStatuses = nil
		policy.Predicate = func(err error) bool {
			apiErr, ok := AsAPIError(err)
			return ok && apiErr.Status == http.StatusNotFound
		}
		attempts, fn := retryTestFailure(retryTestStatusError(http.StatusNotFound, nil), -1)

		if err := policy.Execute(t.Context(), fn); err == nil {
			t.Fatal("Execute() = nil, want the not found failure")
		}
		if *attempts != 2 {
			t.Errorf("attempts = %d, want 2", *attempts)
		}
	})

	t.Run("predicate returning false stops the retry", func(t *testing.T) {
		t.Parallel()
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		policy.MaxRetries = 1
		policy.HTTPStatuses = nil
		policy.Predicate = func(error) bool { return false }
		attempts, fn := retryTestFailure(retryTestStatusError(http.StatusInternalServerError, nil), -1)

		if err := policy.Execute(t.Context(), fn); err == nil {
			t.Fatal("Execute() = nil, want the failure")
		}
		if *attempts != 1 {
			t.Errorf("attempts = %d, want 1", *attempts)
		}
	})

	t.Run("predicate applies to statuses outside the retry set", func(t *testing.T) {
		t.Parallel()
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		policy.MaxRetries = 1
		policy.HTTPStatuses = nil
		policy.Predicate = func(error) bool { return true }
		attempts, fn := retryTestFailure(retryTestStatusError(http.StatusInternalServerError, nil), -1)

		if err := policy.Execute(t.Context(), fn); err == nil {
			t.Fatal("Execute() = nil, want the failure")
		}
		if *attempts != 2 {
			t.Errorf("attempts = %d, want 2", *attempts)
		}
	})
}

func TestRetryPolicyExecuteCustomHTTPStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		statuses     map[int]bool
		status       int
		wantAttempts int
	}{
		{"configured status is retried", map[int]bool{http.StatusConflict: true}, http.StatusConflict, 2},
		{"unconfigured status is not retried", map[int]bool{http.StatusConflict: true}, http.StatusInternalServerError, 1},
		{"empty set retries nothing", map[int]bool{}, http.StatusTooManyRequests, 1},
		{"nil set retries nothing", nil, http.StatusTooManyRequests, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = 1
			policy.HTTPStatuses = tc.statuses
			attempts, fn := retryTestFailure(retryTestStatusError(tc.status, nil), -1)

			if err := policy.Execute(t.Context(), fn); err == nil {
				t.Fatal("Execute() = nil, want the failure")
			}
			if *attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", *attempts, tc.wantAttempts)
			}
		})
	}
}

func TestRetryPolicyExecuteContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("a cancelled context is never retried", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		attempts := 0

		err := policy.Execute(ctx, func(int) error {
			attempts++
			return retryTestRateLimit(nil)
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Execute() = %v, want context.Canceled", err)
		}
		if attempts != 0 {
			t.Errorf("attempts = %d, want 0", attempts)
		}
	})

	t.Run("an expired context is never retried", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		attempts := 0

		err := policy.Execute(ctx, func(int) error {
			attempts++
			return retryTestRateLimit(nil)
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Execute() = %v, want context.DeadlineExceeded", err)
		}
		if attempts != 0 {
			t.Errorf("attempts = %d, want 0", attempts)
		}
	})

	t.Run("cancelling during the attempt returns the failure", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		var delays []time.Duration
		policy := retryTestPolicy(&delays)
		failure := retryTestRateLimit(nil)
		attempts := 0

		err := policy.Execute(ctx, func(int) error {
			attempts++
			cancel()
			return failure
		})
		if err != failure {
			t.Errorf("Execute() = %v, want the failure", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1", attempts)
		}
		if len(delays) != 0 {
			t.Errorf("delays = %v, want none", delays)
		}
	})

	t.Run("cancelling during the wait returns the context error", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		policy := DefaultRetryPolicy()
		policy.RandFloat = func() float64 { return 0 }
		waited := 0
		policy.Sleep = func(ctx context.Context, _ time.Duration) error {
			waited++
			cancel()
			return ctx.Err()
		}
		attempts := 0

		err := policy.Execute(ctx, func(int) error {
			attempts++
			return retryTestRateLimit(nil)
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Execute() = %v, want context.Canceled", err)
		}
		if attempts != 1 || waited != 1 {
			t.Errorf("attempts = %d, waits = %d, want 1 and 1", attempts, waited)
		}
	})
}

func TestRetryPolicyExecuteWithNilPolicyUsesDefaults(t *testing.T) {
	t.Parallel()
	t.Run("retries follow the default policy", func(t *testing.T) {
		t.Parallel()
		var policy *RetryPolicy
		// retry-after-ms of zero keeps the default policy's real sleep at zero.
		failure := retryTestRateLimit(errorsTestHeader(HeaderRetryAfterMs, "0"))
		attempts, fn := retryTestFailure(failure, -1)

		err := policy.Execute(t.Context(), fn)
		if err != failure {
			t.Errorf("Execute() = %v, want the failure", err)
		}
		if *attempts != 3 {
			t.Errorf("attempts = %d, want 3 (the default max retries plus the initial attempt)", *attempts)
		}
	})

	t.Run("a non-retryable failure is not retried", func(t *testing.T) {
		t.Parallel()
		var policy *RetryPolicy
		attempts, fn := retryTestFailure(retryTestStatusError(http.StatusNotFound, nil), -1)

		if err := policy.Execute(t.Context(), fn); err == nil {
			t.Fatal("Execute() = nil, want the failure")
		}
		if *attempts != 1 {
			t.Errorf("attempts = %d, want 1", *attempts)
		}
	})

	t.Run("success returns nil", func(t *testing.T) {
		t.Parallel()
		var policy *RetryPolicy
		if err := policy.Execute(t.Context(), func(int) error { return nil }); err != nil {
			t.Errorf("Execute() = %v, want nil", err)
		}
	})
}

func TestRetryPolicyExecuteValidatesThePolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*RetryPolicy)
		want   string
	}{
		{
			name:   "zero timeout",
			mutate: func(p *RetryPolicy) { p.Timeout = new(time.Duration) },
			want:   "timeout must be a positive, finite number of seconds.",
		},
		{
			name:   "negative max retries",
			mutate: func(p *RetryPolicy) { p.MaxRetries = -1 },
			want:   "max_retries must be a non-negative integer.",
		},
		{
			name:   "jitter outside the unit interval",
			mutate: func(p *RetryPolicy) { p.BackoffJitter = 2 },
			want:   "backoff_jitter must be between zero and one.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := DefaultRetryPolicy()
			tc.mutate(policy)
			attempts := 0

			err := policy.Execute(t.Context(), func(int) error {
				attempts++
				return nil
			})
			if err == nil {
				t.Fatalf("Execute() = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Errorf("Execute() = %q, want %q", err.Error(), tc.want)
			}
			if attempts != 0 {
				t.Errorf("attempts = %d, want 0", attempts)
			}
		})
	}
}

func TestRetryPolicyClientDefaultRetryStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status   int
		attempts int
	}{
		{http.StatusRequestTimeout, 3},
		{http.StatusTooManyRequests, 3},
		{http.StatusInternalServerError, 3},
		{http.StatusServiceUnavailable, 3},
		{599, 3},
		{http.StatusBadRequest, 1},
		{http.StatusUnauthorized, 1},
		{http.StatusForbidden, 1},
		{http.StatusNotFound, 1},
		{http.StatusConflict, 1},
		{http.StatusUnprocessableEntity, 1},
		{http.StatusFound, 1},
	}

	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
				return JSONResponseWithHeaders(tc.status, `{"message":"failed"}`, map[string]string{HeaderRetryAfterMs: "0"})
			}, WithRetryPolicy(policy))

			_, err := client.Models.List(t.Context())
			if err == nil {
				t.Fatalf("Models.List() error = nil, want a %d failure", tc.status)
			}
			apiErr, ok := AsAPIError(err)
			if !ok || apiErr.Status != tc.status {
				t.Errorf("error = %v, want a %d failure", err, tc.status)
			}
			calls := transport.callsSnapshot()
			if len(calls) != tc.attempts {
				t.Fatalf("requests = %d, want %d", len(calls), tc.attempts)
			}
			for index, call := range calls {
				wantCount := ""
				if index > 0 {
					wantCount = strconv.Itoa(index)
				}
				if got := call.Header.Get(HeaderRetryCount); got != wantCount {
					t.Errorf("request %d %s = %q, want %q", index, HeaderRetryCount, got, wantCount)
				}
			}
		})
	}
}

func TestRetryPolicyClientRetryCountHeaderAndRecovery(t *testing.T) {
	t.Parallel()
	var delays []time.Duration
	policy := retryTestPolicy(&delays)
	policy.MaxRetries = 1
	client, transport := newMockClient(t, func(_ *http.Request, attempt int) *http.Response {
		if attempt == 0 {
			return JSONResponseWithHeaders(http.StatusServiceUnavailable, `{"message":"unavailable"}`, map[string]string{HeaderRetryAfterMs: "0"})
		}
		return JSONResponse(http.StatusOK, `{"models":[]}`)
	}, WithRetryPolicy(policy))

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v, want nil", err)
	}
	if len(response.Models) != 0 {
		t.Errorf("Models = %v, want empty", response.Models)
	}
	calls := transport.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if got := calls[0].Header.Get(HeaderRetryCount); got != "" {
		t.Errorf("first request %s = %q, want empty", HeaderRetryCount, got)
	}
	if got := calls[1].Header.Get(HeaderRetryCount); got != "1" {
		t.Errorf("second request %s = %q, want %q", HeaderRetryCount, got, "1")
	}
	// A zero server delay never reaches the sleep seam: the retry happens immediately.
	if len(delays) != 0 {
		t.Errorf("delays = %v, want none", delays)
	}
}

func TestRetryPolicyClientZeroBackoffRetries(t *testing.T) {
	t.Parallel()
	bounds := []struct {
		initial time.Duration
		maximum time.Duration
	}{
		{0, 5 * time.Second},
		{500 * time.Millisecond, 0},
		{0, 0},
	}

	for _, bound := range bounds {
		for _, recover := range []bool{false, true} {
			t.Run(fmt.Sprintf("initial %v maximum %v recover %v", bound.initial, bound.maximum, recover), func(t *testing.T) {
				t.Parallel()
				var delays []time.Duration
				policy := retryTestPolicy(&delays)
				policy.MaxRetries = 1
				policy.BackoffInitial = bound.initial
				policy.BackoffMax = bound.maximum
				client, transport := newMockClient(t, func(_ *http.Request, attempt int) *http.Response {
					if recover && attempt == 1 {
						return JSONResponse(http.StatusOK, `{"models":[]}`)
					}
					return JSONResponse(http.StatusServiceUnavailable, `{"message":"temporarily unavailable"}`)
				}, WithRetryPolicy(policy))

				response, err := client.Models.List(t.Context())
				if recover {
					if err != nil {
						t.Fatalf("Models.List() error = %v, want nil", err)
					}
					if len(response.Models) != 0 {
						t.Errorf("Models = %v, want empty", response.Models)
					}
				} else {
					if err == nil {
						t.Fatal("Models.List() error = nil, want the exhausted failure")
					}
					if !strings.Contains(err.Error(), "temporarily unavailable") {
						t.Errorf("Error() = %q, want the server message", err.Error())
					}
				}

				calls := transport.callsSnapshot()
				if len(calls) != 2 {
					t.Fatalf("requests = %d, want 2", len(calls))
				}
				if got := calls[0].Header.Get(HeaderRetryCount); got != "" {
					t.Errorf("first request %s = %q, want empty", HeaderRetryCount, got)
				}
				if got := calls[1].Header.Get(HeaderRetryCount); got != "1" {
					t.Errorf("second request %s = %q, want %q", HeaderRetryCount, got, "1")
				}
				if len(delays) != 0 {
					t.Errorf("delays = %v, want none", delays)
				}
			})
		}
	}
}

func TestRetryPolicyClientExhaustedRetryReturnsFinalError(t *testing.T) {
	t.Parallel()
	statuses := []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable}
	var delays []time.Duration
	policy := retryTestPolicy(&delays)
	client, transport := newMockClient(t, func(_ *http.Request, attempt int) *http.Response {
		return JSONResponseWithHeaders(statuses[attempt], fmt.Sprintf(`{"message":"attempt %d"}`, attempt+1), map[string]string{
			HeaderRequestID:    fmt.Sprintf("request-%d", attempt+1),
			HeaderRetryAfterMs: "0",
		})
	}, WithRetryPolicy(policy))

	_, err := client.SystemOne(t.Context(), "x", fixedQuestions())
	if err == nil {
		t.Fatal("SystemOne() error = nil, want the final failure")
	}
	const want = "POST https://api.typesafe.ai/v1/systemone: 503 attempt 3 (request_id=request-3)"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("AsAPIError(%v) = false, want true", err)
	}
	if apiErr.Status != http.StatusServiceUnavailable {
		t.Errorf("Status = %d, want 503", apiErr.Status)
	}
	if apiErr.RequestID != "request-3" {
		t.Errorf("RequestID = %q, want %q", apiErr.RequestID, "request-3")
	}
	if !reflect.DeepEqual(apiErr.Body, map[string]any{"message": "attempt 3"}) {
		t.Errorf("Body = %#v, want the final attempt message", apiErr.Body)
	}
	if calls := transport.callsSnapshot(); len(calls) != 3 {
		t.Errorf("requests = %d, want 3", len(calls))
	}
}

func TestRetryPolicyClientHonorsServerDelay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		respect   bool
		headers   map[string]string
		wantDelay time.Duration
	}{
		{"retry-after seconds", true, map[string]string{HeaderRetryAfter: "2"}, 2 * time.Second},
		{"retry-after milliseconds", true, map[string]string{HeaderRetryAfterMs: "125"}, 125 * time.Millisecond},
		{"disabled hint uses backoff", false, map[string]string{HeaderRetryAfter: "2"}, 500 * time.Millisecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			policy.MaxRetries = 1
			policy.RespectRetryAfter = tc.respect
			client, transport := newMockClient(t, func(_ *http.Request, attempt int) *http.Response {
				if attempt == 0 {
					return JSONResponseWithHeaders(http.StatusServiceUnavailable, `{"message":"unavailable"}`, tc.headers)
				}
				return JSONResponse(http.StatusOK, `{"models":[]}`)
			}, WithRetryPolicy(policy))

			if _, err := client.Models.List(t.Context()); err != nil {
				t.Fatalf("Models.List() error = %v, want nil", err)
			}
			if !slices.Equal(delays, []time.Duration{tc.wantDelay}) {
				t.Errorf("delays = %v, want [%v]", delays, tc.wantDelay)
			}
			if calls := transport.callsSnapshot(); len(calls) != 2 {
				t.Errorf("requests = %d, want 2", len(calls))
			}
		})
	}
}

func TestRetryPolicyClientTransportFailures(t *testing.T) {
	t.Parallel()
	connectionFailure := errors.New("connection refused")
	timeoutFailure := &net.DNSError{Err: "i/o timeout", Name: "api.typesafe.ai", IsTimeout: true}

	cases := []struct {
		name         string
		transportErr error
		configure    func(*RetryPolicy)
		wantAttempts int
		wantTimeout  bool
	}{
		{
			name:         "connection failures are retried",
			transportErr: connectionFailure,
			wantAttempts: 3,
		},
		{
			name:         "connection failures can be disabled",
			transportErr: connectionFailure,
			configure:    func(p *RetryPolicy) { p.APIConnectionError = false },
			wantAttempts: 1,
		},
		{
			name:         "timeouts are retried",
			transportErr: timeoutFailure,
			wantAttempts: 3,
			wantTimeout:  true,
		},
		{
			name:         "timeouts can be disabled",
			transportErr: timeoutFailure,
			configure:    func(p *RetryPolicy) { p.APITimeoutError = false },
			wantAttempts: 1,
			wantTimeout:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var delays []time.Duration
			policy := retryTestPolicy(&delays)
			if tc.configure != nil {
				tc.configure(policy)
			}
			transport := &retryTestFailTransport{err: tc.transportErr}
			client := retryTestClient(t, transport, WithTimeout(2*time.Second), WithRetryPolicy(policy))

			_, err := client.Models.List(t.Context())
			if err == nil {
				t.Fatal("Models.List() error = nil, want a transport failure")
			}
			if transport.calls != tc.wantAttempts {
				t.Errorf("transport calls = %d, want %d", transport.calls, tc.wantAttempts)
			}
			if !errors.Is(err, tc.transportErr) {
				t.Errorf("errors.Is(%v, transport error) = false, want true", err)
			}
			if tc.wantTimeout {
				timeoutErr, ok := AsTimeoutError(err)
				if !ok {
					t.Fatalf("AsTimeoutError(%v) = false, want true", err)
				}
				if timeoutErr.Timeout != 2*time.Second {
					t.Errorf("Timeout = %v, want 2s", timeoutErr.Timeout)
				}
				return
			}
			if timeoutErr, ok := AsTimeoutError(err); ok {
				t.Errorf("AsTimeoutError(%v) = true, want a connection error", timeoutErr)
			}
			connectionErr, ok := errors.AsType[*APIConnectionError](err)
			if !ok || connectionErr.Message != "Connection error" {
				t.Errorf("error = %v, want an *APIConnectionError", err)
			}
		})
	}
}

// retryTestRange returns the attempt indices observed for a run of n attempts.
func retryTestRange(n int) []int {
	indices := make([]int, n)
	for index := range n {
		indices[index] = index
	}
	return indices
}
