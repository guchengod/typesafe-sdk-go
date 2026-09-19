package typesafe

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// Client is the TypeSafe AI API client.
//
// A Client is safe for concurrent use by multiple goroutines: it holds no per-request state, and
// the underlying http.Client pools connections across calls. Create one Client and share it. All
// request methods take a context.Context, which is the only cancellation and concurrency control
// the SDK needs.
type Client struct {
	config     *Config
	httpClient *http.Client

	// Models provides access to the Models API resource.
	Models *ModelsService

	closeOnce sync.Once
	closeErr  error
}

// NewClient creates a client for the TypeSafe AI API.
//
// Configuration is resolved from the functional options first, then the environment, then the
// SDK defaults:
//
//	client, err := typesafe.NewClient(typesafe.WithTimeout(30*time.Second))
//
// TYPESAFE_API_KEY is required unless [WithAPIKey] supplies the key.
func NewClient(opts ...Option) (*Client, error) {
	config, err := resolveConfig(opts)
	if err != nil {
		return nil, err
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		transport := config.Transport
		if transport == nil {
			transport = newSecureHTTPTransport()
		}
		httpClient = &http.Client{
			Transport: transport,
			// The SDK does not follow redirects: a redirected request would carry the API key to
			// another host and could replay a POST body there. A 3xx response is surfaced as an
			// APIError instead. Supply WithHTTPClient to opt into redirect handling.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}

	client := &Client{config: config, httpClient: httpClient}
	client.Models = &ModelsService{client: client}
	return client, nil
}

// Config returns the resolved client configuration.
func (c *Client) Config() *Config { return c.config }

// HTTPClient returns the underlying *http.Client. A client constructed with [WithHTTPClient]
// returns that value; otherwise it returns the SDK's own client.
func (c *Client) HTTPClient() *http.Client { return c.httpClient }

// Close releases the connection pool held by the client's transport.
//
// A transport supplied through [WithTransport] or [WithHTTPClient] is closed when it implements
// io.Closer, so the client owns the resources it was given. Close is idempotent: later calls
// return the first call's error without touching the transport again.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.httpClient == nil || c.httpClient.Transport == nil {
			return
		}
		if idle, ok := c.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
			idle.CloseIdleConnections()
		}
		if closer, ok := c.httpClient.Transport.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// newRequest folds the per-call options into an executable request against the client's config.
func (c *Client) newRequest(method, path string, body any, options requestOptions) (request, error) {
	timeout, err := resolveCallTimeout(c.config.Timeout, options.timeout)
	if err != nil {
		return request{}, err
	}
	return request{
		method:      method,
		path:        path,
		body:        body,
		timeout:     timeout,
		headers:     options.extraHeaders,
		retryPolicy: options.retryPolicy,
	}, nil
}

// resolveRequestOptions applies the per-call options, ignoring nil entries.
func resolveRequestOptions(opts []RequestOption) requestOptions {
	var options requestOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

// resolveCallTimeout returns the timeout in effect for a call, validating a per-call override.
func resolveCallTimeout(base time.Duration, override *time.Duration) (time.Duration, error) {
	if override == nil {
		return base, nil
	}
	if *override <= 0 {
		return 0, &SDKError{Message: "timeout must be a positive, finite number of seconds."}
	}
	return *override, nil
}
