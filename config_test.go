package typesafe

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// configTestClient builds a client from opts and closes it when the test ends.
func configTestClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	client, err := NewClient(opts...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// configTestIdleTransport is the transport a test uses when it only inspects configuration, so
// that every request still has somewhere to go.
func configTestIdleTransport() *mockTransport {
	return &mockTransport{handler: func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{"models":[]}`)
	}}
}

// configTestConstructionError returns the error NewClient produced, failing when it succeeded.
func configTestConstructionError(t *testing.T, opts ...Option) error {
	t.Helper()
	client, err := NewClient(opts...)
	if err == nil {
		_ = client.Close()
		t.Fatal("NewClient() error = nil, want a construction failure")
	}
	return err
}

func TestConfigAPIKeyResolution(t *testing.T) {
	t.Run("option wins over the environment", func(t *testing.T) {
		t.Setenv(APIKeyEnv, "env-key")
		client := configTestClient(t, WithAPIKey("option-key"))
		if got := client.Config().APIKey; got != "option-key" {
			t.Errorf("APIKey = %q, want %q", got, "option-key")
		}
	})

	t.Run("environment supplies the key", func(t *testing.T) {
		t.Setenv(APIKeyEnv, "env-key")
		client := configTestClient(t)
		if got := client.Config().APIKey; got != "env-key" {
			t.Errorf("APIKey = %q, want %q", got, "env-key")
		}
	})

	t.Run("environment value is trimmed", func(t *testing.T) {
		t.Setenv(APIKeyEnv, "  env-key  ")
		client := configTestClient(t)
		if got := client.Config().APIKey; got != "env-key" {
			t.Errorf("APIKey = %q, want %q", got, "env-key")
		}
	})

	t.Run("missing key names both remedies", func(t *testing.T) {
		t.Setenv(APIKeyEnv, "")
		err := configTestConstructionError(t)

		var sdkErr *SDKError
		if !errors.As(err, &sdkErr) {
			t.Fatalf("error = %v (%T), want *SDKError", err, err)
		}
		for _, want := range []string{"WithAPIKey", APIKeyEnv} {
			if !strings.Contains(sdkErr.Message, want) {
				t.Errorf("error message %q does not mention %q", sdkErr.Message, want)
			}
		}
	})

	t.Run("blank environment value counts as unset", func(t *testing.T) {
		t.Setenv(APIKeyEnv, " \t\n ")
		err := configTestConstructionError(t)
		if !strings.Contains(err.Error(), APIKeyEnv) {
			t.Errorf("error = %q, want it to name %s", err, APIKeyEnv)
		}
	})
}

func TestConfigBaseURLResolution(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().BaseURL; got != DefaultBaseURL {
			t.Errorf("BaseURL = %q, want %q", got, DefaultBaseURL)
		}
	})

	t.Run("environment overrides the default", func(t *testing.T) {
		t.Setenv(BaseURLEnv, "https://env.test")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().BaseURL; got != "https://env.test" {
			t.Errorf("BaseURL = %q, want %q", got, "https://env.test")
		}
	})

	t.Run("surrounding whitespace and a trailing slash are removed", func(t *testing.T) {
		t.Setenv(BaseURLEnv, "  https://env.test/  ")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().BaseURL; got != "https://env.test" {
			t.Errorf("BaseURL = %q, want %q", got, "https://env.test")
		}
	})

	t.Run("option wins over the environment", func(t *testing.T) {
		t.Setenv(BaseURLEnv, "https://env.test")
		client := configTestClient(t, WithAPIKey("k"), WithBaseURL("https://code.test/"))
		if got := client.Config().BaseURL; got != "https://code.test" {
			t.Errorf("BaseURL = %q, want %q", got, "https://code.test")
		}
	})

	t.Run("repeated trailing slashes are stripped", func(t *testing.T) {
		t.Setenv(BaseURLEnv, "https://env.test///")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().BaseURL; got != "https://env.test" {
			t.Errorf("BaseURL = %q, want %q", got, "https://env.test")
		}
	})

	t.Run("blank environment value falls back to the default", func(t *testing.T) {
		t.Setenv(BaseURLEnv, " \t ")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().BaseURL; got != DefaultBaseURL {
			t.Errorf("BaseURL = %q, want %q", got, DefaultBaseURL)
		}
	})
}

func TestConfigDefaultModelResolution(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().DefaultModel; got != DefaultModel {
			t.Errorf("DefaultModel = %q, want %q", got, DefaultModel)
		}
	})

	t.Run("environment overrides the default and is trimmed", func(t *testing.T) {
		t.Setenv(DefaultModelEnv, "  env-model  ")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().DefaultModel; got != "env-model" {
			t.Errorf("DefaultModel = %q, want %q", got, "env-model")
		}
	})

	t.Run("option wins over the environment", func(t *testing.T) {
		t.Setenv(DefaultModelEnv, "env-model")
		client := configTestClient(t, WithAPIKey("k"), WithDefaultModel("code-model"))
		if got := client.Config().DefaultModel; got != "code-model" {
			t.Errorf("DefaultModel = %q, want %q", got, "code-model")
		}
	})

	t.Run("blank environment value falls back to the default", func(t *testing.T) {
		t.Setenv(DefaultModelEnv, " \t ")
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().DefaultModel; got != DefaultModel {
			t.Errorf("DefaultModel = %q, want %q", got, DefaultModel)
		}
	})
}

func TestConfigTimeoutPrecedence(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"))
		if got := client.Config().Timeout; got != DefaultTimeout {
			t.Errorf("Timeout = %v, want %v", got, DefaultTimeout)
		}
	})

	t.Run("option applies", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"), WithTimeout(2*time.Second))
		if got := client.Config().Timeout; got != 2*time.Second {
			t.Errorf("Timeout = %v, want %v", got, 2*time.Second)
		}
	})

	t.Run("option wins over the supplied http client timeout", func(t *testing.T) {
		httpClient := &http.Client{Timeout: 3 * time.Second}
		client := configTestClient(t, WithAPIKey("k"), WithHTTPClient(httpClient), WithTimeout(time.Second))
		if got := client.Config().Timeout; got != time.Second {
			t.Errorf("Timeout = %v, want %v", got, time.Second)
		}
		if httpClient.Timeout != 3*time.Second {
			t.Errorf("supplied http.Client Timeout = %v, want it left at %v", httpClient.Timeout, 3*time.Second)
		}
	})

	t.Run("supplied http client timeout is inherited", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"), WithHTTPClient(&http.Client{Timeout: 3 * time.Second}))
		if got := client.Config().Timeout; got != 3*time.Second {
			t.Errorf("Timeout = %v, want %v", got, 3*time.Second)
		}
	})

	t.Run("supplied http client without a timeout falls back to the default", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"), WithHTTPClient(&http.Client{}))
		if got := client.Config().Timeout; got != DefaultTimeout {
			t.Errorf("Timeout = %v, want %v", got, DefaultTimeout)
		}
	})

	t.Run("non-positive timeouts are rejected", func(t *testing.T) {
		for _, timeout := range []time.Duration{0, -time.Millisecond, -time.Second} {
			err := configTestConstructionError(t, WithAPIKey("k"), WithTimeout(timeout))

			var sdkErr *SDKError
			if !errors.As(err, &sdkErr) {
				t.Fatalf("WithTimeout(%v) error = %v (%T), want *SDKError", timeout, err, err)
			}
			if !strings.Contains(sdkErr.Message, "timeout must be a positive") {
				t.Errorf("WithTimeout(%v) error = %q, want it to describe a positive timeout", timeout, sdkErr.Message)
			}
		}
	})
}

func TestConfigTransportAndHTTPClientAreMutuallyExclusive(t *testing.T) {
	transport := configTestIdleTransport()
	httpClient := &http.Client{Transport: transport}

	err := configTestConstructionError(t, WithAPIKey("k"), WithTransport(transport), WithHTTPClient(httpClient))

	var sdkErr *SDKError
	if !errors.As(err, &sdkErr) {
		t.Fatalf("error = %v (%T), want *SDKError", err, err)
	}
	if !strings.Contains(sdkErr.Message, "transport and http_client are mutually exclusive") {
		t.Errorf("error = %q, want it to report the exclusive options", sdkErr.Message)
	}
}

func TestConfigRejectsInvalidRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.MaxRetries = -1

	err := configTestConstructionError(t, WithAPIKey("k"), WithRetryPolicy(policy))
	if !strings.Contains(err.Error(), "max_retries") {
		t.Errorf("error = %q, want it to name max_retries", err)
	}
}

func TestConfigAccessors(t *testing.T) {
	t.Run("transport supplied by option", func(t *testing.T) {
		transport := configTestIdleTransport()
		client := configTestClient(t, WithAPIKey("k"), WithTransport(transport))

		if client.Config() == nil {
			t.Fatal("Config() = nil")
		}
		if client.Config() != client.Config() {
			t.Error("Config() returned different values across calls")
		}
		httpClient := client.HTTPClient()
		if httpClient == nil {
			t.Fatal("HTTPClient() = nil")
		}
		if httpClient.Transport != transport {
			t.Errorf("HTTPClient().Transport = %#v, want the supplied transport", httpClient.Transport)
		}
	})

	t.Run("http client supplied by option", func(t *testing.T) {
		supplied := &http.Client{Transport: configTestIdleTransport()}
		client := configTestClient(t, WithAPIKey("k"), WithHTTPClient(supplied))

		if got := client.HTTPClient(); got != supplied {
			t.Errorf("HTTPClient() = %#v, want the supplied *http.Client", got)
		}
		if got := client.Config().HTTPClient; got != supplied {
			t.Errorf("Config().HTTPClient = %#v, want the supplied *http.Client", got)
		}
	})

	t.Run("sdk owns its transport when none is supplied", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"))
		if client.HTTPClient() == nil || client.HTTPClient().Transport == nil {
			t.Fatal("HTTPClient() has no transport, want the SDK's own")
		}
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v, want nil", err)
		}
	})
}

func TestConfigDefaultHeaderMerging(t *testing.T) {
	t.Run("WithHeaders merges with WithHeader", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"),
			WithHeader("X-First", "one"),
			WithHeaders(map[string]string{"X-Second": "two", "X-First": "three"}),
		)
		headers := client.Config().DefaultHeaders
		if got := headers.Get("X-First"); got != "three" {
			t.Errorf("X-First = %q, want the later value %q", got, "three")
		}
		if got := headers.Get("X-Second"); got != "two" {
			t.Errorf("X-Second = %q, want %q", got, "two")
		}
	})

	t.Run("a later WithHeader wins over WithHeaders", func(t *testing.T) {
		client := configTestClient(t, WithAPIKey("k"),
			WithHeaders(map[string]string{"X-Only": "from-headers"}),
			WithHeader("X-Only", "from-header"),
		)
		headers := client.Config().DefaultHeaders
		if got := headers.Get("X-Only"); got != "from-header" {
			t.Errorf("X-Only = %q, want %q", got, "from-header")
		}
	})

	t.Run("a header added through Config cannot displace credentials", func(t *testing.T) {
		transport := configTestIdleTransport()
		client := configTestClient(t, WithAPIKey("k"), WithTransport(transport))
		client.Config().DefaultHeaders.Set(HeaderAuthorization, "Bearer attacker")
		client.Config().DefaultHeaders.Set(HeaderUserAgent, "attacker/1.0")

		if _, err := client.Models.List(t.Context()); err != nil {
			t.Fatalf("Models.List() error = %v", err)
		}
		call := transport.lastCall(t)
		if got := call.Header.Get(HeaderAuthorization); got != "Bearer k" {
			t.Errorf("%s = %q, want %q", HeaderAuthorization, got, "Bearer k")
		}
		if got := call.Header.Get(HeaderUserAgent); got != SDKName+"/"+SDKVersion {
			t.Errorf("%s = %q, want %q", HeaderUserAgent, got, SDKName+"/"+SDKVersion)
		}
	})
}
