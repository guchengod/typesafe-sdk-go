package typesafe

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/tls"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Pre-canonicalized MIME header keys avoid per-request allocations inside textproto.CanonicalMIMEHeaderKey
// for mixed-case SDK header constants such as "X-TypeSafe-SDK" and "TypeSafe-Danger-Zone-Images".
var (
	canonAuthorization         = textproto.CanonicalMIMEHeaderKey(HeaderAuthorization)
	canonAccept                = textproto.CanonicalMIMEHeaderKey(HeaderAccept)
	canonContentType           = textproto.CanonicalMIMEHeaderKey(HeaderContentType)
	canonUserAgent             = textproto.CanonicalMIMEHeaderKey(HeaderUserAgent)
	canonSDK                   = textproto.CanonicalMIMEHeaderKey(HeaderSDK)
	canonRuntime               = textproto.CanonicalMIMEHeaderKey(HeaderRuntime)
	canonRetryCount            = textproto.CanonicalMIMEHeaderKey(HeaderRetryCount)
	canonRequestID             = textproto.CanonicalMIMEHeaderKey(HeaderRequestID)
	canonDangerZoneImages      = textproto.CanonicalMIMEHeaderKey(HeaderDangerZoneImages)
	canonDangerZoneLongContext = textproto.CanonicalMIMEHeaderKey(HeaderDangerZoneLongContext)
	canonContentEncoding       = textproto.CanonicalMIMEHeaderKey(HeaderContentEncoding)
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
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    100,
		MaxConnsPerHost:        100,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		MaxResponseHeaderBytes: 256 << 10,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
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
	compression *bool
	longContext *bool
	images      *bool
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
	policy := cmp.Or(req.retryPolicy, config.RetryPolicy)

	targetURL := config.BaseURL + req.path
	safeURL := cleanURL(targetURL)
	var endpoint string
	if safeURL != "" {
		endpoint = req.method + " " + safeURL
	}

	var body []byte
	var logBody []byte
	var isCompressed bool
	if req.body != nil {
		encoded, err := jsonv2.Marshal(req.body, marshalOptions)
		if err != nil {
			return nil, &SDKError{Message: "The request body could not be encoded as JSON", Cause: err}
		}
		if len(encoded) > MaxRequestBodyLimit {
			return nil, &SDKError{
				Message: fmt.Sprintf("The request body exceeded the %d-byte limit.", MaxRequestBodyLimit),
			}
		}
		if config.Logger.Enabled(ctx, slog.LevelDebug) {
			logBody = encoded
		}
		shouldCompress := config.Compression
		if req.compression != nil {
			shouldCompress = *req.compression
		}
		if shouldCompress && len(encoded) >= MinCompressionBytes {
			compressed, err := compressGzip(encoded)
			if err != nil {
				return nil, &SDKError{Message: "Failed to compress request body", Cause: err}
			}
			body = compressed
			isCompressed = true
		} else {
			body = encoded
		}
	}

	header := buildHeaders(config, req, body != nil, isCompressed)
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
		if policy.MaxRetries > 0 {
			httpReq.Header = header.Clone()
		} else {
			httpReq.Header = header
		}
		if attempt > 0 {
			httpReq.Header[canonRetryCount] = []string{strconv.Itoa(attempt)}
			if logger.Enabled(attemptCtx, slog.LevelInfo) {
				logger.LogAttrs(attemptCtx, slog.LevelInfo, "request.retry",
					slog.String("method", req.method), slog.String("url", safeURL), slog.Int("attempt", attempt))
			}
		}
		logRequest(attemptCtx, logger, req.method, safeURL, httpReq.Header, logBody)

		started := time.Now()
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			// A transport may return a response alongside a redirect or protocol failure.
			if httpResp != nil && httpResp.Body != nil {
				closeBody(httpResp.Body)
			}
			failure := classifyTransportError(err, req.timeout)
			logFailure(attemptCtx, logger, req.method, safeURL, failure)
			return failure
		}
		if httpResp.Body == nil {
			httpResp.Body = http.NoBody
		}
		defer closeBody(httpResp.Body)

		payload, err := readResponseBody(httpResp)
		if err != nil {
			failure := classifyTransportError(err, req.timeout)
			logFailure(attemptCtx, logger, req.method, safeURL, failure)
			return failure
		}

		requestID := httpResp.Header.Get(canonRequestID)
		logResponse(attemptCtx, logger, req.method, safeURL, httpResp.StatusCode, time.Since(started), requestID, httpResp.Header, payload)

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
func buildHeaders(config *Config, req request, hasBody, isCompressed bool) http.Header {
	hasCallerHeaders := len(config.DefaultHeaders) > 0 || len(req.headers) > 0
	header := make(http.Header, len(config.DefaultHeaders)+len(req.headers)+7)
	for name, values := range config.DefaultHeaders {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	for name, value := range req.headers {
		header.Set(name, value)
	}
	if hasCallerHeaders {
		delete(header, canonDangerZoneLongContext)
		delete(header, canonDangerZoneImages)
		delete(header, canonContentEncoding)
		// A caller-supplied retry count would misreport the attempt number to the server.
		delete(header, canonRetryCount)
	}

	effectiveLongContext := config.LongContext
	if req.longContext != nil {
		effectiveLongContext = *req.longContext
	}
	if effectiveLongContext {
		header[canonDangerZoneLongContext] = []string{"true"}
	}

	effectiveImages := config.Images
	if req.images != nil {
		effectiveImages = *req.images
	}
	if effectiveImages {
		header[canonDangerZoneImages] = []string{"true"}
	}

	header[canonAuthorization] = []string{"Bearer " + config.APIKey}
	header[canonAccept] = []string{ContentTypeJSON}
	ua := sdkUserAgent()
	header[canonUserAgent] = []string{ua}
	header[canonSDK] = []string{ua}
	header[canonRuntime] = []string{RuntimeString()}
	if hasBody {
		header[canonContentType] = []string{ContentTypeJSON}
	}
	if isCompressed {
		header[canonContentEncoding] = []string{EncodingGzip}
	}
	return header
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

// compressGzip compresses data with standard gzip, reusing pooled writers to reduce GC churn.
func compressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, ok := gzipWriterPool.Get().(*gzip.Writer)
	if !ok || zw == nil {
		zw = gzip.NewWriter(&buf)
	} else {
		zw.Reset(&buf)
	}
	defer func() {
		zw.Reset(io.Discard)
		gzipWriterPool.Put(zw)
	}()
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

// readResponseBody bounds 2xx bodies to [MaxResponseBodyLimit] (failing fast on Content-Length)
// and caps non-2xx error bodies at [MaxErrorResponseBodyLimit] to prevent gzip/JSON memory amplification.
func readResponseBody(httpResp *http.Response) ([]byte, error) {
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		payload, err := io.ReadAll(io.LimitReader(httpResp.Body, MaxErrorResponseBodyLimit+1))
		if err != nil {
			return nil, err
		}
		payload = payload[:min(len(payload), MaxErrorResponseBodyLimit)]
		return payload, nil
	}
	if httpResp.ContentLength > MaxResponseBodyLimit {
		return nil, &SDKError{Message: "The response body exceeded the " + strconv.Itoa(MaxResponseBodyLimit) + "-byte limit."}
	}
	return readBody(httpResp.Body)
}

// classifyTransportError maps a transport failure onto the SDK's timeout and connection errors.
func classifyTransportError(err error, timeout time.Duration) error {
	if timeoutErr, ok := errors.AsType[*APITimeoutError](err); ok {
		return timeoutErr
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if sdkErr, ok := errors.AsType[*SDKError](err); ok {
		return sdkErr
	}
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		sanitized := &url.Error{
			Op:  urlErr.Op,
			URL: cleanURL(urlErr.URL),
			Err: urlErr.Err,
		}
		if urlErr.Timeout() {
			return &APITimeoutError{
				Cause:   sanitized,
				Timeout: timeout,
			}
		}
		err = sanitized
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &APITimeoutError{
			Cause:   err,
			Timeout: timeout,
		}
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
		slog.String("body", truncateUTF8(string(body), MaxLoggedBodyBytes)),
	)
}

func logResponse(ctx context.Context, logger *slog.Logger, method, url string, status int, elapsed time.Duration, requestID string, header http.Header, body []byte) {
	requestID = cmp.Or(requestID, "-")
	if logger.Enabled(ctx, slog.LevelInfo) {
		logger.LogAttrs(ctx, slog.LevelInfo, "response",
			slog.String("method", method),
			slog.String("url", url),
			slog.Int("status", status),
			slog.Int64("elapsed_ms", elapsed.Milliseconds()),
			slog.String("request_id", requestID),
		)
	}
	if logger.Enabled(ctx, slog.LevelDebug) {
		logger.LogAttrs(ctx, slog.LevelDebug, "response.wire",
			slog.String("method", method),
			slog.String("url", url),
			slog.Int("status", status),
			slog.String("request_id", requestID),
			slog.Any("headers", redactHeaders(header)),
			slog.String("body", truncateUTF8(string(body), MaxLoggedBodyBytes)),
		)
	}
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
