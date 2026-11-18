package typesafe

import (
	"cmp"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	execClient *http.Client

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

	httpClient, execClient := buildExecHTTPClient(config)
	client := &Client{
		config:     config,
		httpClient: httpClient,
		execClient: execClient,
	}
	client.Models = &ModelsService{client: client}
	return client, nil
}

func buildExecHTTPClient(config *Config) (*http.Client, *http.Client) {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if config.HTTPClient == nil {
		transport := config.Transport
		if transport == nil {
			transport = newSecureHTTPTransport()
		}
		owned := &http.Client{
			Transport:     transport,
			CheckRedirect: noRedirect,
		}
		return owned, owned
	}

	// Shallow-clone the caller's http.Client so HTTPClient() still returns the original pointer
	// untouched while requests execute with hardened redirect and per-attempt timeout rules.
	cloned := *config.HTTPClient
	cloned.Timeout = 0
	if cloned.Transport == nil {
		cloned.Transport = newSecureHTTPTransport()
	}
	if cloned.CheckRedirect == nil {
		cloned.CheckRedirect = noRedirect
	} else {
		callerRedirect := cloned.CheckRedirect
		cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 {
				origin := via[0].URL
				prev := via[len(via)-1].URL
				if (strings.EqualFold(origin.Scheme, "https") || strings.EqualFold(prev.Scheme, "https")) &&
					!strings.EqualFold(req.URL.Scheme, "https") {
					return &SDKError{Message: "Refusing to follow HTTPS-to-HTTP redirect."}
				}
				if !sameOrigin(origin, req.URL) {
					if req.Body != nil && req.Body != http.NoBody {
						return &SDKError{Message: "Refusing to replay request body across cross-origin redirect."}
					}
					for name := range req.Header {
						if isSecretHeader(name) {
							req.Header.Del(name)
						}
					}
				}
			}
			return callerRedirect(req, via)
		}
	}
	return config.HTTPClient, &cloned
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// Config returns the resolved client configuration. Treat the returned Config as read-only once
// the Client is shared across goroutines; use per-call [RequestOption] values for overrides.
func (c *Client) Config() *Config {
	if c == nil {
		return nil
	}
	return c.config
}

// HTTPClient returns the underlying *http.Client. A client constructed with [WithHTTPClient]
// returns that value; otherwise it returns the SDK's own client.
func (c *Client) HTTPClient() *http.Client {
	if c == nil {
		return nil
	}
	return c.httpClient
}

// Close releases the connection pool held by the client's transport.
//
// A transport supplied through [WithTransport] or [WithHTTPClient] is closed when it implements
// io.Closer, so the client owns the resources it was given. Close is idempotent: later calls
// return the first call's error without touching the transport again.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		active := cmp.Or(c.execClient, c.httpClient)
		if active == nil || active.Transport == nil {
			return
		}
		if idle, ok := active.Transport.(interface{ CloseIdleConnections() }); ok {
			idle.CloseIdleConnections()
		}
		if closer, ok := active.Transport.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// newRequest folds the per-call options into an executable request against the client's config.
func (c *Client) newRequest(method, path string, body any, options requestOptions) (request, error) {
	if options.assetsErr != nil {
		return request{}, options.assetsErr
	}
	timeout, err := resolveCallTimeout(c.config.Timeout, options.timeout)
	if err != nil {
		return request{}, err
	}
	for name, value := range options.extraHeaders {
		if err := validateHeaderPair(name, value); err != nil {
			return request{}, err
		}
	}
	return request{
		method:      method,
		path:        path,
		body:        body,
		timeout:     timeout,
		headers:     options.extraHeaders,
		retryPolicy: options.retryPolicy,
		compression: options.compression,
		longContext: options.longContext,
		images:      options.images,
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
