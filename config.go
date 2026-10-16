package typesafe

import (
	"cmp"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config is the resolved client configuration.
type Config struct {
	// APIKey is the credential sent as a bearer token.
	APIKey string

	// BaseURL is the API root, without a trailing slash.
	BaseURL string

	// DefaultModel is the model used when a call does not name one.
	DefaultModel string

	// Timeout is the HTTP timeout for one attempt, in effect for every call.
	Timeout time.Duration

	// DefaultHeaders holds headers sent with every request. Client identification and credentials
	// are applied after these, so a default header cannot displace them.
	DefaultHeaders http.Header

	// Logger receives the SDK's structured log records.
	Logger *slog.Logger

	// RetryPolicy is the retry policy in effect for every call.
	RetryPolicy *RetryPolicy

	// HTTPClient is the caller-supplied HTTP client, or nil when the SDK owns its client.
	HTTPClient *http.Client

	// Transport is the caller-supplied transport, or nil when the SDK owns its transport.
	Transport http.RoundTripper

	// Compression enables automatic request body compression for bodies >= 1 KiB.
	// Defaults to true.
	Compression bool

	// LongContext enables the TypeSafe-Danger-Zone-Long-Context header by default (262k token context).
	LongContext bool

	// Images enables the TypeSafe-Danger-Zone-Images header by default.
	Images bool
}

// Option configures a [Client] at construction time.
type Option func(*configOptions)

type configOptions struct {
	apiKey         string
	baseURL        string
	defaultModel   string
	timeout        *time.Duration
	defaultHeaders http.Header
	logger         *slog.Logger
	retryPolicy    *RetryPolicy
	httpClient     *http.Client
	transport      http.RoundTripper
	compression    *bool
	longContext    *bool
	images         *bool
}

// WithCompression sets whether request bodies >= 1 KiB are compressed (gzip).
// Default is true. Pass false to send uncompressed bodies.
func WithCompression(enabled bool) Option {
	return func(o *configOptions) { o.compression = new(enabled) }
}

// WithLongContext sets whether to send TypeSafe-Danger-Zone-Long-Context: true with every request.
func WithLongContext(enabled bool) Option {
	return func(o *configOptions) { o.longContext = new(enabled) }
}

// WithImages sets whether to send TypeSafe-Danger-Zone-Images: true with every request.
func WithImages(enabled bool) Option {
	return func(o *configOptions) { o.images = new(enabled) }
}

// WithAPIKey sets the API key, overriding the TYPESAFE_API_KEY environment variable.
func WithAPIKey(apiKey string) Option {
	return func(o *configOptions) { o.apiKey = apiKey }
}

// WithBaseURL sets the API root, overriding the TYPESAFE_BASE_URL environment variable.
func WithBaseURL(baseURL string) Option {
	return func(o *configOptions) { o.baseURL = baseURL }
}

// WithDefaultModel sets the model used when a call does not name one, overriding the
// TYPESAFE_DEFAULT_MODEL environment variable.
func WithDefaultModel(model string) Option {
	return func(o *configOptions) { o.defaultModel = model }
}

// WithTimeout sets the HTTP timeout for one attempt. It must be positive.
//
// The timeout has no environment variable. When [WithHTTPClient] supplies both a client and no
// explicit timeout, the client's own Timeout is adopted instead.
func WithTimeout(timeout time.Duration) Option {
	return func(o *configOptions) { o.timeout = new(timeout) }
}

// WithRetryPolicy sets the retry policy for every call.
func WithRetryPolicy(policy *RetryPolicy) Option {
	return func(o *configOptions) { o.retryPolicy = policy }
}

// WithHeader sets a default header sent with every request. Client identification and
// credentials are applied afterwards and cannot be displaced.
func WithHeader(key, value string) Option {
	return func(o *configOptions) {
		if o.defaultHeaders == nil {
			o.defaultHeaders = make(http.Header)
		}
		o.defaultHeaders.Set(key, value)
	}
}

// WithHeaders sets default headers sent with every request, merging with any set by [WithHeader].
func WithHeaders(headers map[string]string) Option {
	return func(o *configOptions) {
		if o.defaultHeaders == nil {
			o.defaultHeaders = make(http.Header, len(headers))
		}
		for key, value := range headers {
			o.defaultHeaders.Set(key, value)
		}
	}
}

// WithHTTPClient supplies the http.Client the SDK sends through, together with its transport.
// Mutually exclusive with [WithTransport].
func WithHTTPClient(client *http.Client) Option {
	return func(o *configOptions) { o.httpClient = client }
}

// WithTransport supplies the http.RoundTripper the SDK sends through. Mutually exclusive with
// [WithHTTPClient].
func WithTransport(transport http.RoundTripper) Option {
	return func(o *configOptions) { o.transport = transport }
}

// WithLogger sets the structured logger. The SDK logs requests, responses, and retries at info
// level, and headers and bodies at debug level, with credential headers redacted.
func WithLogger(logger *slog.Logger) Option {
	return func(o *configOptions) { o.logger = logger }
}

// RequestOption overrides client configuration for a single call.
type RequestOption func(*requestOptions)

type requestOptions struct {
	timeout      *time.Duration
	retryPolicy  *RetryPolicy
	extraHeaders map[string]string
	extraBody    map[string]any
	model        string
	compression  *bool
	longContext  *bool
	images       *bool
	assets       map[string]any
}

// WithCallTimeout overrides the HTTP timeout for a single call. It must be positive.
func WithCallTimeout(timeout time.Duration) RequestOption {
	return func(o *requestOptions) { o.timeout = new(timeout) }
}

// WithCallRetry overrides the retry policy for a single call.
func WithCallRetry(policy *RetryPolicy) RequestOption {
	return func(o *requestOptions) { o.retryPolicy = policy }
}

// WithCallHeader sets or overrides a request header for a single call.
func WithCallHeader(key, value string) RequestOption {
	return func(o *requestOptions) {
		if o.extraHeaders == nil {
			o.extraHeaders = make(map[string]string)
		}
		o.extraHeaders[key] = value
	}
}

// WithCallHeaders sets or overrides request headers for a single call, merging with any set by
// [WithCallHeader].
func WithCallHeaders(headers map[string]string) RequestOption {
	return func(o *requestOptions) {
		if o.extraHeaders == nil {
			o.extraHeaders = make(map[string]string, len(headers))
		}
		for key, value := range headers {
			o.extraHeaders[key] = value
		}
	}
}

// WithModel overrides the model for a single call.
func WithModel(model string) RequestOption {
	return func(o *requestOptions) { o.model = model }
}

// WithCallCompression overrides body compression for a single call.
func WithCallCompression(enabled bool) RequestOption {
	return func(o *requestOptions) { o.compression = new(enabled) }
}

// WithCallLongContext sets TypeSafe-Danger-Zone-Long-Context: true for a single call.
func WithCallLongContext(enabled bool) RequestOption {
	return func(o *requestOptions) { o.longContext = new(enabled) }
}

// WithCallImages sets TypeSafe-Danger-Zone-Images: true for a single call.
func WithCallImages(enabled bool) RequestOption {
	return func(o *requestOptions) { o.images = new(enabled) }
}

// WithAssets attaches an assets map to the request, containing image assets.
// It accepts [Assets], map[string]Asset, or map[string]any.
func WithAssets(assets any) RequestOption {
	return func(o *requestOptions) {
		switch typed := assets.(type) {
		case nil:
			o.assets = nil
		case Assets:
			m := make(map[string]any, len(typed))
			for k, v := range typed {
				m[k] = v
			}
			o.assets = m
		case map[string]Asset:
			m := make(map[string]any, len(typed))
			for k, v := range typed {
				m[k] = v
			}
			o.assets = m
		case map[string]any:
			o.assets = typed
		}
	}
}

// WithExtraBody adds top-level fields to a request body, shallow-merged over "state", "model",
// and "questions" after they are set. Merging is last-write-wins: a colliding key replaces the
// SDK's value, and object values are replaced rather than deep-merged.
func WithExtraBody(extra map[string]any) RequestOption {
	return func(o *requestOptions) { o.extraBody = extra }
}

// resolveEnv returns the first non-blank value among the explicit value, the named environment
// variable, and the fallback. Blank and whitespace-only values are treated as unset.
func resolveEnv(value, name, fallback string) string {
	return cmp.Or(strings.TrimSpace(value), strings.TrimSpace(os.Getenv(name)), fallback)
}

// resolveConfig applies the options and the environment to produce a validated [Config].
func resolveConfig(opts []Option) (*Config, error) {
	var options configOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	if options.transport != nil && options.httpClient != nil {
		return nil, &SDKError{Message: "transport and http_client are mutually exclusive."}
	}

	apiKey := resolveEnv(options.apiKey, APIKeyEnv, "")
	if apiKey == "" {
		return nil, &SDKError{
			Message: "No API key was provided. Pass WithAPIKey or set the " + APIKeyEnv + " environment variable.",
		}
	}

	timeout := DefaultTimeout
	if options.timeout != nil {
		timeout = *options.timeout
	} else if options.httpClient != nil && options.httpClient.Timeout > 0 {
		timeout = options.httpClient.Timeout
	}
	if timeout <= 0 {
		return nil, &SDKError{Message: "timeout must be a positive, finite number of seconds."}
	}

	policy := options.retryPolicy
	if policy == nil {
		policy = DefaultRetryPolicy()
	} else if err := policy.Validate(); err != nil {
		return nil, err
	}

	logger := options.logger
	if logger == nil {
		logger = defaultLogger()
	}

	compression := true
	if options.compression != nil {
		compression = *options.compression
	}
	longContext := false
	if options.longContext != nil {
		longContext = *options.longContext
	}
	images := false
	if options.images != nil {
		images = *options.images
	}

	headers := make(http.Header, len(options.defaultHeaders)+2)
	for name, values := range options.defaultHeaders {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	if longContext {
		headers.Set(HeaderDangerZoneLongContext, "true")
	}
	if images {
		headers.Set(HeaderDangerZoneImages, "true")
	}

	return &Config{
		APIKey:         apiKey,
		BaseURL:        strings.TrimRight(resolveEnv(options.baseURL, BaseURLEnv, DefaultBaseURL), "/"),
		DefaultModel:   resolveEnv(options.defaultModel, DefaultModelEnv, DefaultModel),
		Timeout:        timeout,
		DefaultHeaders: headers,
		Logger:         logger,
		RetryPolicy:    policy,
		HTTPClient:     options.httpClient,
		Transport:      options.transport,
		Compression:    compression,
		LongContext:    longContext,
		Images:         images,
	}, nil
}
