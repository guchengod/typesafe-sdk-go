package typesafe

import (
	"context"
	"errors"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"
)

// DefaultRetryPolicy returns a fresh copy of the retry policy the SDK uses when none is supplied.
func DefaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{
		MaxRetries:         2,
		BackoffInitial:     500 * time.Millisecond,
		BackoffMax:         5 * time.Second,
		BackoffJitter:      0.25,
		HTTPStatuses:       DefaultRetryStatuses(),
		RespectRetryAfter:  true,
		APIConnectionError: true,
		APITimeoutError:    true,
		Timeout:            new(DefaultRetryBudget),
	}
}

// DefaultRetryStatuses returns the set of HTTP status codes the SDK retries by default:
// 408, 429, and every 5xx.
func DefaultRetryStatuses() map[int]bool {
	statuses := make(map[int]bool, 101)
	statuses[http.StatusRequestTimeout] = true
	statuses[http.StatusTooManyRequests] = true
	for status := http.StatusInternalServerError; status < 600; status++ {
		statuses[status] = true
	}
	return statuses
}

// RetryPolicy configures how the client retries failed requests.
//
// The zero value is not usable; start from [DefaultRetryPolicy] and adjust, or set the fields
// you care about and leave the rest zero:
//
//	policy := typesafe.DefaultRetryPolicy()
//	policy.MaxRetries = 3
//	policy.Timeout = nil // no overall budget
//
// A non-nil policy passed to [WithRetryPolicy] or [WithCallRetry] is validated when it is used.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the initial attempt. Zero disables retries.
	MaxRetries int

	// BackoffInitial is the first backoff delay; it doubles per attempt up to BackoffMax.
	// Zero disables backoff.
	BackoffInitial time.Duration

	// BackoffMax caps the backoff delay. Zero disables backoff.
	BackoffMax time.Duration

	// BackoffJitter is the fraction of each backoff delay that is randomly subtracted,
	// between zero and one.
	BackoffJitter float64

	// HTTPStatuses holds the response status codes that are retried.
	HTTPStatuses map[int]bool

	// RespectRetryAfter honors the retry-after-ms and Retry-After response headers.
	RespectRetryAfter bool

	// APIConnectionError retries [APIConnectionError] failures.
	APIConnectionError bool

	// APITimeoutError retries [APITimeoutError] failures.
	APITimeoutError bool

	// RetryErrors holds additional error values that trigger a retry. A failure is retried when
	// errors.Is reports a match against any entry.
	RetryErrors []error

	// Predicate is consulted for every failure. Returning true triggers a retry, in addition to
	// the rules above.
	Predicate func(error) bool

	// Timeout is the total budget for one SDK call, covering the initial attempt, retries, and
	// delays. A retry whose delay would reach or exceed the remaining budget is abandoned and the
	// last error is returned. Nil disables the budget.
	Timeout *time.Duration

	// Sleep waits between attempts. It is a seam for tests; nil uses a context-aware timer.
	Sleep func(ctx context.Context, delay time.Duration) error

	// RandFloat returns a value in [0, 1) used for backoff jitter. It is a seam for tests;
	// nil uses the global math/rand/v2 source.
	RandFloat func() float64
}

// Validate reports whether the policy is well formed.
func (p *RetryPolicy) Validate() error {
	switch {
	case p.MaxRetries < 0:
		return &SDKError{Message: "max_retries must be a non-negative integer."}
	case p.BackoffInitial < 0:
		return &SDKError{Message: "backoff_initial must be a non-negative, finite number of seconds."}
	case p.BackoffMax < 0:
		return &SDKError{Message: "backoff_max must be a non-negative, finite number of seconds."}
	case !isFinite(p.BackoffJitter) || p.BackoffJitter < 0 || p.BackoffJitter > 1:
		return &SDKError{Message: "backoff_jitter must be between zero and one."}
	case p.Timeout != nil && *p.Timeout <= 0:
		return &SDKError{Message: "timeout must be a positive, finite number of seconds."}
	}
	return nil
}

// Clone returns a deep copy of the policy, so a per-call override can be adjusted without
// mutating the client's policy.
func (p *RetryPolicy) Clone() *RetryPolicy {
	if p == nil {
		return nil
	}
	clone := *p
	clone.HTTPStatuses = maps.Clone(p.HTTPStatuses)
	clone.RetryErrors = slices.Clone(p.RetryErrors)
	if p.Timeout != nil {
		clone.Timeout = new(*p.Timeout)
	}
	return &clone
}

// isRetryable reports whether a failure should be retried under this policy.
func (p *RetryPolicy) isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var timeoutErr *APITimeoutError
	if errors.As(err, &timeoutErr) {
		if p.APITimeoutError {
			return true
		}
	} else {
		var connectionErr *APIConnectionError
		if errors.As(err, &connectionErr) && p.APIConnectionError {
			return true
		}
	}
	if apiErr, ok := AsAPIError(err); ok && p.HTTPStatuses[apiErr.Status] {
		return true
	}
	for _, target := range p.RetryErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return p.Predicate != nil && p.Predicate(err)
}

// backoff returns the jittered exponential delay for a retry attempt, where attempt 1 is the
// first retry.
func (p *RetryPolicy) backoff(attempt int) time.Duration {
	if p.BackoffInitial <= 0 || p.BackoffMax <= 0 {
		return 0
	}
	initial := p.BackoffInitial.Seconds()
	maximum := p.BackoffMax.Seconds()
	exponent := float64(attempt - 1)

	exponential := maximum
	if initial < maximum {
		if exponent < 62 {
			exponential = min(math.Ldexp(initial, int(exponent)), maximum)
		}
	}

	sample := rand.Float64()
	if p.RandFloat != nil {
		sample = p.RandFloat()
	}
	jitter := min(max(p.BackoffJitter, 0), 1)

	delay := exponential * (1 - sample*jitter)
	// Milliseconds of resolution match the SDK's documented backoff granularity.
	delay = math.Round(delay*1000) / 1000
	if delay < 0 {
		delay = 0
	}
	if delay > exponential {
		delay = exponential
	}
	return time.Duration(delay * float64(time.Second))
}

// delay returns the wait before the next attempt: the server's Retry-After hint when one is
// present and honored, else the computed backoff.
func (p *RetryPolicy) delay(attempt int, lastErr error) time.Duration {
	if p.RespectRetryAfter {
		var apiErr *APIError
		if errors.As(lastErr, &apiErr) {
			if serverDelay, ok := parseRetryAfter(apiErr.Headers); ok {
				return serverDelay
			}
		}
	}
	return p.backoff(attempt)
}

// wait sleeps for delay unless the context ends first.
func (p *RetryPolicy) wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	if p.Sleep != nil {
		return p.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Execute runs fn under this policy, retrying per the policy's rules.
//
// fn receives the zero-based attempt index. Execute stops early, returning the last failure,
// when the retry budget would be exceeded, when the failure is not retryable, or when the
// caller's context ends. A cancelled or expired context is never retried.
func (p *RetryPolicy) Execute(ctx context.Context, fn func(attempt int) error) error {
	policy := p
	if policy == nil {
		policy = DefaultRetryPolicy()
	}
	if err := policy.Validate(); err != nil {
		return err
	}

	start := time.Now()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(attempt)
		if err == nil {
			return nil
		}
		if attempt >= policy.MaxRetries {
			return err
		}
		// The caller's context is authoritative: a cancelled call is never extended.
		if ctx.Err() != nil {
			return err
		}
		if !policy.isRetryable(err) {
			return err
		}
		delay := policy.delay(attempt+1, err)
		if policy.Timeout != nil && time.Since(start)+delay >= *policy.Timeout {
			return err
		}
		if waitErr := policy.wait(ctx, delay); waitErr != nil {
			return waitErr
		}
	}
}
