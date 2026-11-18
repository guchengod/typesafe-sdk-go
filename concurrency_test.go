package typesafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if err := verifyNoGoroutineLeaks(); err != nil {
			fmt.Fprintf(os.Stderr, "goroutine leak detected:\n%v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func verifyNoGoroutineLeaks() error {
	for range 4 {
		runtime.Gosched()
	}
	if leakProf := pprof.Lookup("goroutineleak"); leakProf != nil {
		var leakBuf bytes.Buffer
		if err := leakProf.WriteTo(&leakBuf, 1); err != nil {
			return fmt.Errorf("writing goroutineleak profile: %w", err)
		}
		if leakProf.Count() > 0 {
			return fmt.Errorf("goroutineleak count=%d:\n%s", leakProf.Count(), leakBuf.String())
		}
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		var stackBuf bytes.Buffer
		if prof := pprof.Lookup("goroutine"); prof != nil {
			_ = prof.WriteTo(&stackBuf, 2)
		}
		unexpected := findUnexpectedGoroutines(stackBuf.String())
		if len(unexpected) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lingering goroutines after test suite:\n%s", strings.Join(unexpected, "\n\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func findUnexpectedGoroutines(stacks string) []string {
	var bad []string
	for block := range strings.SplitSeq(strings.TrimSpace(stacks), "\n\n") {
		if block == "" ||
			strings.Contains(block, "runtime/pprof.writeGoroutine") ||
			strings.Contains(block, "testing.(*M).Run") ||
			strings.Contains(block, "testing.(*T).Parallel") {
			continue
		}
		if strings.Contains(block, "github.com/guchengod/typesafe-sdk-go") ||
			strings.Contains(block, "net/http.(*persistConn).") ||
			strings.Contains(block, "os/signal.NotifyContext") {
			bad = append(bad, block)
		}
	}
	return bad
}

func TestConcurrencyNilAndCancelledContextFastFail(t *testing.T) {
	t.Parallel()

	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, fixedAnswerResponse)
	})

	// Unmarshalable state proves SystemOne checks context before marshaling/compressing.
	unmarshalableState := map[string]any{"bad": make(chan int)}
	var nilCtx context.Context

	t.Run("nil context returns SDKError without panicking or marshaling", func(t *testing.T) {
		if _, err := client.SystemOne(nilCtx, unmarshalableState, fixedQuestions()); err == nil {
			t.Fatal("SystemOne(nil) error = nil, want *SDKError")
		} else if sdkErr, ok := errors.AsType[*SDKError](err); !ok || !strings.Contains(sdkErr.Message, "context") {
			t.Fatalf("SystemOne(nil) error = %T (%v), want *SDKError mentioning context", err, err)
		}

		if _, err := client.SystemOneAs[responseTestEnvelope](nilCtx, unmarshalableState, fixedQuestions()); err == nil {
			t.Fatal("SystemOneAs(nil) error = nil, want *SDKError")
		} else if _, ok := errors.AsType[*SDKError](err); !ok {
			t.Fatalf("SystemOneAs(nil) error = %T (%v), want *SDKError", err, err)
		}

		if _, err := client.Models.List(nilCtx); err == nil {
			t.Fatal("Models.List(nil) error = nil, want *SDKError")
		} else if _, ok := errors.AsType[*SDKError](err); !ok {
			t.Fatalf("Models.List(nil) error = %T (%v), want *SDKError", err, err)
		}

		if err := DefaultRetryPolicy().Execute(nilCtx, func(int) error { return nil }); err == nil {
			t.Fatal("RetryPolicy.Execute(nil) error = nil, want *SDKError")
		} else if _, ok := errors.AsType[*SDKError](err); !ok {
			t.Fatalf("RetryPolicy.Execute(nil) error = %T (%v), want *SDKError", err, err)
		}
	})

	t.Run("already-cancelled context fails fast before marshaling", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := client.SystemOne(ctx, unmarshalableState, fixedQuestions())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SystemOne(cancelled) error = %T (%v), want context.Canceled", err, err)
		}
		if len(transport.callsSnapshot()) != 0 {
			t.Fatalf("recorded %d transport calls, want 0", len(transport.callsSnapshot()))
		}
	})
}

type cancelRaceBody struct {
	done <-chan struct{}
}

func (b *cancelRaceBody) Read([]byte) (int, error) {
	<-b.done
	// Simulate socket/HTTP2 stream teardown surfacing ErrUnexpectedEOF before context.Canceled.
	return 0, io.ErrUnexpectedEOF
}

func (b *cancelRaceBody) Close() error { return nil }

type concurrencyTestTransport struct {
	mu         sync.Mutex
	roundTrip  func(*http.Request) (*http.Response, error)
	idleCloses int
	closes     int
}

func (t *concurrencyTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	return t.roundTrip(req)
}

func (t *concurrencyTestTransport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.idleCloses++
}

func (t *concurrencyTestTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closes++
	return nil
}

func (t *concurrencyTestTransport) counts() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.idleCloses, t.closes
}

func TestConcurrencyCallerDeadlineAndCancellationClassification(t *testing.T) {
	t.Parallel()

	t.Run("tighter caller deadline returns context.DeadlineExceeded instead of 10s APITimeoutError", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client, err := NewClient(
				WithAPIKey("test-key"),
				WithTransport(clientTestDeadlineTransport{}),
				WithTimeout(10*time.Second),
				WithRetryPolicy(noRetryPolicy()),
				WithLogger(slog.New(slog.DiscardHandler)),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			_, err = client.Models.List(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %T (%v), want context.DeadlineExceeded", err, err)
			}
			if timeoutErr, ok := errors.AsType[*APITimeoutError](err); ok {
				t.Fatalf("caller deadline misclassified as *APITimeoutError (Timeout=%v)", timeoutErr.Timeout)
			}
		})
	})

	t.Run("mid-body cancellation racing with EOF returns context.Canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		client, _ := newMockClient(t, func(req *http.Request, _ int) *http.Response {
			cancel()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {ContentTypeJSON}},
				Body:       &cancelRaceBody{done: req.Context().Done()},
			}
		})

		_, err := client.Models.List(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %T (%v), want context.Canceled", err, err)
		}
		if _, ok := errors.AsType[*APIConnectionError](err); ok {
			t.Fatalf("cancellation misclassified as *APIConnectionError: %v", err)
		}
	})

	t.Run("url.Error with context.Canceled scrubs URL credentials and query params", func(t *testing.T) {
		client, err := NewClient(
			WithAPIKey("test-key"),
			WithTransport(&concurrencyTestTransport{
				roundTrip: func(*http.Request) (*http.Response, error) {
					// #nosec G101 -- synthetic URL credentials used to verify cleanURL redaction on cancellation.
					return nil, &url.Error{
						Op:  "Get",
						URL: "https://svc:secret-pass@example.test/v1/models?api_token=secret-tok",
						Err: context.Canceled,
					}
				},
			}),
			WithRetryPolicy(noRetryPolicy()),
			WithLogger(slog.New(slog.DiscardHandler)),
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		_, classified := client.Models.List(t.Context())
		if !errors.Is(classified, context.Canceled) {
			t.Fatalf("classified = %T (%v), want context.Canceled", classified, classified)
		}
		if strings.Contains(classified.Error(), "secret-pass") || strings.Contains(classified.Error(), "secret-tok") {
			t.Fatalf("classified error leaked URL secret: %q", classified.Error())
		}
	})
}

func TestConcurrencyRetryPolicyTotalTimeoutBoundsAttempts(t *testing.T) {
	t.Parallel()

	t.Run("initial attempt is bounded by RetryPolicy.Timeout when smaller than client Timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			policy := DefaultRetryPolicy()
			policy.MaxRetries = 0
			policy.Timeout = new(2 * time.Second)

			client, err := NewClient(
				WithAPIKey("test-key"),
				WithTransport(clientTestDeadlineTransport{}),
				WithTimeout(10*time.Second),
				WithRetryPolicy(policy),
				WithLogger(slog.New(slog.DiscardHandler)),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			start := time.Now()
			_, err = client.Models.List(t.Context())
			elapsed := time.Since(start)
			if elapsed != 2*time.Second {
				t.Fatalf("elapsed = %v, want 2s", elapsed)
			}
			timeoutErr, ok := errors.AsType[*APITimeoutError](err)
			if !ok || timeoutErr.Timeout != 2*time.Second {
				t.Fatalf("err = %T (%v), want *APITimeoutError with Timeout=2s", err, err)
			}
		})
	})

	t.Run("retry attempt cannot overrun total RetryPolicy.Timeout budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			policy := DefaultRetryPolicy()
			policy.MaxRetries = 2
			policy.BackoffInitial = 250 * time.Millisecond
			policy.BackoffMax = 250 * time.Millisecond
			policy.BackoffJitter = 0
			policy.Timeout = new(15 * time.Second)

			var callNum int
			client, err := NewClient(
				WithAPIKey("test-key"),
				WithTransport(&concurrencyTestTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
					callNum++
					if callNum == 1 {
						synctest.Sleep(14 * time.Second)
						return JSONResponse(http.StatusServiceUnavailable, `{"message":"busy"}`), nil
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				}}),
				WithTimeout(15*time.Second),
				WithRetryPolicy(policy),
				WithLogger(slog.New(slog.DiscardHandler)),
			)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			start := time.Now()
			_, err = client.Models.List(t.Context())
			elapsed := time.Since(start)
			if elapsed > 15*time.Second {
				t.Fatalf("elapsed = %v, exceeded total budget of 15s", elapsed)
			}
			if _, ok := errors.AsType[*APITimeoutError](err); !ok {
				t.Fatalf("err = %T (%v), want *APITimeoutError", err, err)
			}
		})
	})
}

func TestConcurrencyClientCloseFenceAndDefaultTransportSafety(t *testing.T) {
	t.Parallel()

	t.Run("calls after Close fail fast with SDKError without hitting transport", func(t *testing.T) {
		client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
			return JSONResponse(http.StatusOK, fixedAnswerResponse)
		})
		if err := client.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}

		for _, call := range []func() error{
			func() error { _, err := client.SystemOne(t.Context(), "s", fixedQuestions()); return err },
			func() error {
				_, err := client.SystemOneAs[responseTestEnvelope](t.Context(), "s", fixedQuestions())
				return err
			},
			func() error { _, err := client.Models.List(t.Context()); return err },
		} {
			err := call()
			if sdkErr, ok := errors.AsType[*SDKError](err); !ok || !strings.Contains(sdkErr.Message, "closed") {
				t.Fatalf("post-Close call error = %T (%v), want *SDKError mentioning closed", err, err)
			}
		}
		if got := len(transport.callsSnapshot()); got != 0 {
			t.Fatalf("transport saw %d calls after Close, want 0", got)
		}
	})

	t.Run("Close during in-flight request evicts idle pool on completion and blocks retries", func(t *testing.T) {
		inFlight := make(chan struct{})
		notifyInFlight := sync.OnceFunc(func() { close(inFlight) })
		release := make(chan struct{})
		var attempts int
		transport := &concurrencyTestTransport{
			roundTrip: func(*http.Request) (*http.Response, error) {
				attempts++
				notifyInFlight()
				<-release
				return JSONResponse(http.StatusServiceUnavailable, `{"message":"busy"}`), nil
			},
		}
		policy := DefaultRetryPolicy()
		policy.MaxRetries = 3
		policy.BackoffInitial = time.Millisecond
		policy.BackoffMax = time.Millisecond

		client, err := NewClient(
			WithAPIKey("test-key"),
			WithTransport(transport),
			WithRetryPolicy(policy),
			WithLogger(slog.New(slog.DiscardHandler)),
		)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}

		var callErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, callErr = client.Models.List(t.Context())
		})

		<-inFlight
		if err := client.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		close(release)
		wg.Wait()

		if callErr == nil {
			t.Fatal("in-flight call succeeded, want error")
		}
		if attempts != 1 {
			t.Errorf("transport saw %d attempts, want 1 (post-Close retries must not fire)", attempts)
		}
		idleCloses, closes := transport.counts()
		if closes != 1 {
			t.Errorf("transport Close count = %d, want 1", closes)
		}
		if idleCloses < 2 {
			t.Errorf("transport CloseIdleConnections count = %d, want >= 2 (initial Close + post-in-flight eviction)", idleCloses)
		}
	})

	t.Run("Close does not touch http.DefaultTransport", func(t *testing.T) {
		client, err := NewClient(WithAPIKey("test-key"), WithTransport(http.DefaultTransport))
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
}

func TestConcurrencyDefensiveCloningAndConfigIsolation(t *testing.T) {
	t.Parallel()

	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, fixedAnswerResponse)
	})

	extra := map[string]any{"custom_tag": "v1"}
	extraOpt := WithExtraBody(extra)
	extra["custom_tag"] = "mutated"

	callHeaders := map[string]string{"X-Trace": "t1"}
	hdrOpt := WithCallHeaders(callHeaders)
	callHeaders["X-Trace"] = "mutated"

	choiceMap := map[string]any{"calm": nil, "angry": "hostile"}
	choiceQ := NewChoice(choiceMap)
	choiceMap["injected"] = "bad"

	scoreSlice := []any{"low", "mid", "high"}
	scoreQ := NewScore(scoreSlice)
	scoreSlice[0] = "mutated"

	rawSource := map[string]any{"billing": map[string]any{"type": "noul", "instructions": "billing?"}}
	norm, err := NormalizeQuestions(rawSource)
	if err != nil {
		t.Fatalf("NormalizeQuestions() error = %v", err)
	}
	norm["extra_q"] = NewNoul("extra?")
	if _, leaked := rawSource["extra_q"]; leaked {
		t.Fatal("NormalizeQuestions(map[string]any) aliased caller map")
	}

	// Mutating client.Config() is isolated from the internal execution snapshot.
	exposedCfg := client.Config()
	exposedCfg.DefaultHeaders.Set("X-Mutated", "bad")
	exposedCfg.RetryPolicy = nil

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			clonedCfg := client.Config().Clone()
			clonedCfg.DefaultHeaders.Set("X-Race", fmt.Sprintf("val-%d", i))

			_, err := client.SystemOne(t.Context(), "state", Questions{
				"billing": NewNoul("billing?"),
				"tone":    choiceQ,
				"urgency": scoreQ,
			}, extraOpt, hdrOpt)
			if err != nil {
				t.Errorf("SystemOne() error = %v", err)
			}
		})
	}
	wg.Wait()

	call := transport.lastCall(t)
	if got := call.Header.Get("X-Mutated"); got != "" {
		t.Errorf("X-Mutated = %q, want empty (internal config isolated from Config() mutation)", got)
	}
	if got := call.Header.Get("X-Trace"); got != "t1" {
		t.Errorf("X-Trace = %q, want t1 (isolated from post-option map mutation)", got)
	}
	decoded := decodingBody(t, call)
	if got := decoded["custom_tag"]; got != "v1" {
		t.Errorf("custom_tag = %#v, want v1 (isolated from post-WithExtraBody map mutation)", got)
	}

	// Also verify execute handles nil Config.RetryPolicy without panicking.
	rawCfg := *client.config
	rawCfg.RetryPolicy = nil
	if _, err := execute(t.Context(), &rawCfg, client.execClient, request{
		method:  http.MethodGet,
		path:    ModelsPath,
		timeout: time.Second,
	}); err != nil {
		t.Errorf("execute with nil Config.RetryPolicy error = %v", err)
	}
}

func TestConcurrencyCaptureStderrPanicDoesNotLeak(t *testing.T) {
	func() {
		defer func() { _ = recover() }()
		_ = loggingTestCaptureStderr(t, func() {
			panic("boom")
		})
	}()
	if err := verifyNoGoroutineLeaks(); err != nil {
		t.Fatalf("loggingTestCaptureStderr leaked after panic: %v", err)
	}
}

func TestConcurrencyWaitDeterministicContextPrecedence(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	policy := DefaultRetryPolicy()
	if err := policy.wait(ctx, 50*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait(cancelled, 50ms) = %v, want context.Canceled", err)
	}
}

type ctxProbeKey struct{}

type ctxProbeHandler struct {
	mu     sync.Mutex
	values []string
}

func (h *ctxProbeHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *ctxProbeHandler) Handle(ctx context.Context, _ slog.Record) error {
	if v, ok := ctx.Value(ctxProbeKey{}).(string); ok {
		h.mu.Lock()
		h.values = append(h.values, v)
		h.mu.Unlock()
	}
	return nil
}
func (h *ctxProbeHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *ctxProbeHandler) WithGroup(string) slog.Handler      { return h }

func TestConcurrencyNilResponseAccessorsAndContextLogs(t *testing.T) {
	t.Parallel()

	var nilSys *SystemOneResponse
	if nilSys.Nouls() != nil || nilSys.Choices() != nil || nilSys.Scores() != nil || nilSys.Boxes() != nil {
		t.Error("nil SystemOneResponse grouped accessors should return nil")
	}
	if nilSys.RawRequestID() != "" || nilSys.String() == "" {
		t.Error("nil SystemOneResponse RawRequestID/String unexpected")
	}
	if _, err := nilSys.RequestID(); err == nil {
		t.Error("nil SystemOneResponse.RequestID() error = nil, want error")
	}
	if _, err := nilSys.RawHTTPResponse(); err == nil { //nolint:bodyclose // nil receiver error path
		t.Error("nil SystemOneResponse.RawHTTPResponse() error = nil, want error")
	}

	var nilModels *ListModelsResponse
	if nilModels.RawRequestID() != "" {
		t.Error("nil ListModelsResponse.RawRequestID() != empty")
	}
	if _, err := nilModels.RequestID(); err == nil {
		t.Error("nil ListModelsResponse.RequestID() error = nil, want error")
	}
	if _, err := nilModels.RawHTTPResponse(); err == nil { //nolint:bodyclose // nil receiver error path
		t.Error("nil ListModelsResponse.RawHTTPResponse() error = nil, want error")
	}

	probe := &ctxProbeHandler{}
	client, _ := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{
			"model": "jev-latest",
			"usage": {"input_tokens": 1, "output_tokens": 1},
			"answers": {
				"q": {"type": "noul", "noul": 0.5},
				"unknown": {"type": "future_type"}
			}
		}`)
	}, WithLogger(slog.New(probe)))

	ctx := context.WithValue(t.Context(), ctxProbeKey{}, "trace-abc")
	if _, err := client.SystemOne(ctx, "s", Questions{"q": NewNoul("?")}); err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	probe.mu.Lock()
	got := slices.Clone(probe.values)
	probe.mu.Unlock()
	if len(got) < 3 {
		t.Fatalf("recorded %d context-propagated logs, want >= 3 (request, response, and warning): %v", len(got), got)
	}
}
