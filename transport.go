package typesafe

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// newSecureHTTPTransport returns the transport the SDK uses when the caller supplies neither an
// http.Client nor an http.RoundTripper. It pools connections aggressively and refuses TLS
// versions below 1.2.
func newSecureHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// request is the SDK's internal description of one HTTP call. Endpoint methods build it from a
// resolved [Config] plus the caller's [RequestOption] values.
type request struct {
	method      string
	path        string
	body        any
	timeout     time.Duration
	headers     map[string]string
	retryPolicy *RetryPolicy
}

// response is a successful HTTP response together with the metadata the SDK surfaces on its
// typed response objects.
type response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	RequestID  string
	Endpoint   string
	Method     string
	URL        *url.URL
}

// execute performs the request, applying retries, per-attempt timeouts, credential redaction,
// and status-to-error mapping.
//
// A non-2xx status is returned as a typed [APIError] so the retry policy can classify it. Read
// failures and timeouts are returned as [APIConnectionError] or [APITimeoutError].
func execute(ctx context.Context, config *Config, httpClient *http.Client, req request) (*response, error) {
	policy := config.RetryPolicy
	if req.retryPolicy != nil {
		policy = req.retryPolicy
	}

	targetURL := config.BaseURL + req.path
	endpoint := cleanEndpoint(req.method, targetURL)

	var body []byte
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return nil, &SDKError{Message: "The request body could not be encoded as JSON", Cause: err}
		}
		body = encoded
	}

	header := buildHeaders(config, req, body != nil)
	logger := config.Logger

	var result *response
	attemptErr := policy.Execute(ctx, func(attempt int) error {
		attemptCtx, cancel := context.WithTimeout(ctx, req.timeout)
		defer cancel()

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		httpReq, err := http.NewRequestWithContext(attemptCtx, req.method, targetURL, reader)
		if err != nil {
			return &SDKError{Message: "Failed to create HTTP request", Cause: err}
		}
		httpReq.Header = header.Clone()
		if attempt > 0 {
			httpReq.Header.Set(HeaderRetryCount, strconv.Itoa(attempt))
			if logger.Enabled(attemptCtx, slog.LevelInfo) {
				logger.LogAttrs(attemptCtx, slog.LevelInfo, "request.retry",
					slog.String("method", req.method), slog.String("url", targetURL), slog.Int("attempt", attempt))
			}
		}
		logRequest(attemptCtx, logger, req.method, targetURL, httpReq.Header, body)

		started := time.Now()
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			// A transport may return a response alongside a redirect or protocol failure.
			if httpResp != nil && httpResp.Body != nil {
				closeBody(httpResp.Body)
			}
			failure := classifyTransportError(err, req.timeout)
			logFailure(attemptCtx, logger, req.method, targetURL, failure)
			return failure
		}
		defer closeBody(httpResp.Body)

		payload, err := readBody(httpResp.Body)
		if err != nil {
			failure := classifyTransportError(err, req.timeout)
			logFailure(attemptCtx, logger, req.method, targetURL, failure)
			return failure
		}

		requestID := httpResp.Header.Get(HeaderRequestID)
		logResponse(attemptCtx, logger, req.method, targetURL, httpResp.StatusCode, time.Since(started), requestID, httpResp.Header, payload)

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			return newAPIError(httpResp.StatusCode, decodeErrorBody(payload), payload, httpResp.Header, endpoint)
		}
		// A custom RoundTripper is not required to populate Response.Request; fall back to the
		// URL this attempt was built with.
		requestURL := httpReq.URL
		if httpResp.Request != nil {
			requestURL = httpResp.Request.URL
		}
		result = &response{
			StatusCode: httpResp.StatusCode,
			Header:     httpResp.Header,
			Body:       payload,
			RequestID:  requestID,
			Endpoint:   endpoint,
			Method:     req.method,
			URL:        requestURL,
		}
		return nil
	})
	if attemptErr != nil {
		return nil, attemptErr
	}
	return result, nil
}

// buildHeaders merges the client's default headers with the per-call headers and then applies the
// SDK's own headers. Client identification and credentials are applied last, so a caller can
// never displace them, whatever the case they spell a header name in.
func buildHeaders(config *Config, req request, hasBody bool) http.Header {
	header := make(http.Header, len(config.DefaultHeaders)+len(req.headers)+5)
	for name, values := range config.DefaultHeaders {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	for name, value := range req.headers {
		header.Set(name, value)
	}
	// A caller-supplied retry count would misreport the attempt number to the server.
	header.Del(HeaderRetryCount)
	header.Set(HeaderAuthorization, "Bearer "+config.APIKey)
	header.Set(HeaderAccept, ContentTypeJSON)
	header.Set(HeaderUserAgent, sdkUserAgent())
	header.Set(HeaderSDK, sdkUserAgent())
	header.Set(HeaderRuntime, RuntimeString())
	if hasBody {
		header.Set(HeaderContentType, ContentTypeJSON)
	}
	return header
}

// closeBody releases a response body, discarding the error: the SDK has already read the payload,
// and a close failure cannot change the outcome of the call.
func closeBody(body io.Closer) {
	_ = body.Close()
}

// readBody reads a response body, refusing payloads beyond [MaxResponseBodyLimit] rather than
// silently truncating them into a confusing decode failure.
func readBody(source io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(source, MaxResponseBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > MaxResponseBodyLimit {
		return nil, &SDKError{Message: "The response body exceeded the " + strconv.Itoa(MaxResponseBodyLimit) + "-byte limit."}
	}
	return payload, nil
}

// classifyTransportError maps a transport failure onto the SDK's timeout and connection errors.
func classifyTransportError(err error, timeout time.Duration) error {
	var timeoutErr *APITimeoutError
	if errors.As(err, &timeoutErr) {
		return timeoutErr
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &APITimeoutError{
			APIConnectionError: APIConnectionError{Cause: err},
			Timeout:            timeout,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &APITimeoutError{
			APIConnectionError: APIConnectionError{Cause: err},
			Timeout:            timeout,
		}
	}
	if sdkErr, ok := errors.AsType[*SDKError](err); ok {
		return sdkErr
	}
	return &APIConnectionError{Message: "Connection error", Cause: err}
}

func logRequest(ctx context.Context, logger *slog.Logger, method, url string, header http.Header, body []byte) {
	if !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	logger.LogAttrs(ctx, slog.LevelDebug, "request.wire",
		slog.String("method", method),
		slog.String("url", url),
		slog.Any("headers", redactHeaders(header)),
		slog.String("body", string(body)),
	)
}

func logResponse(ctx context.Context, logger *slog.Logger, method, url string, status int, elapsed time.Duration, requestID string, header http.Header, body []byte) {
	if !logger.Enabled(ctx, slog.LevelInfo) {
		return
	}
	if requestID == "" {
		requestID = "-"
	}
	attrs := []slog.Attr{
		slog.String("method", method),
		slog.String("url", url),
		slog.Int("status", status),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.String("request_id", requestID),
	}
	if logger.Enabled(ctx, slog.LevelDebug) {
		attrs = append(attrs,
			slog.Any("headers", redactHeaders(header)),
			slog.String("body", string(body)),
		)
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "response", attrs...)
}

func logFailure(ctx context.Context, logger *slog.Logger, method, url string, err error) {
	if !logger.Enabled(ctx, slog.LevelInfo) {
		return
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "request.failed",
		slog.String("method", method),
		slog.String("url", url),
		slog.String("error", err.Error()),
	)
}
