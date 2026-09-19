package typesafe

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Environment variable names.
const (
	// APIKeyEnv is the environment variable for the TypeSafe API key.
	APIKeyEnv = "TYPESAFE_API_KEY"

	// BaseURLEnv is the environment variable for the API base URL.
	BaseURLEnv = "TYPESAFE_BASE_URL"

	// DefaultModelEnv is the environment variable for the default model.
	DefaultModelEnv = "TYPESAFE_DEFAULT_MODEL"

	// LogLevelEnv is the environment variable for the logging level.
	LogLevelEnv = "TYPESAFE_LOG_LEVEL"
)

// Client and protocol defaults.
const (
	// DefaultBaseURL is the default API root URL.
	DefaultBaseURL = "https://api.typesafe.ai"

	// DefaultModel is the default model name.
	DefaultModel = "jev-latest"

	// DefaultTimeout is the default HTTP timeout for operations.
	DefaultTimeout = 10 * time.Second

	// DefaultRetryBudget is the default overall retry timeout budget.
	DefaultRetryBudget = 30 * time.Second

	// SDKName is the name of this SDK.
	SDKName = "typesafe-sdk-go"

	// SDKVersion is the current version of this SDK.
	SDKVersion = "0.8.0"

	// MaxErrorBodyLength limits the raw error body length included in error messages.
	MaxErrorBodyLength = 200

	// MaxResponseBodyLimit limits the maximum response body read (16 MB) to prevent OOM DOS attacks.
	MaxResponseBodyLimit = 16 * 1024 * 1024
)

// API Endpoint paths.
const (
	SystemOnePath = "/v1/systemone"
	ModelsPath    = "/v1/models"
)

// Protocol headers.
const (
	HeaderAuthorization = "Authorization"
	HeaderAccept        = "Accept"
	HeaderContentType   = "Content-Type"
	HeaderUserAgent     = "User-Agent"
	HeaderSDK           = "X-TypeSafe-SDK"
	HeaderRuntime       = "X-TypeSafe-Runtime"
	HeaderRetryCount    = "X-TypeSafe-Retry-Count"
	HeaderRequestID     = "x-typesafe-request-id"
	HeaderRetryAfter    = "retry-after"
	HeaderRetryAfterMs  = "retry-after-ms"
	ContentTypeJSON     = "application/json"
)

// RuntimeString returns the runtime environment string for SDK identification, sent in the
// X-TypeSafe-Runtime header.
func RuntimeString() string {
	return fmt.Sprintf("go/%s (%s; %s)", strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)
}

// sdkUserAgent returns the identifier sent in the User-Agent and X-TypeSafe-SDK headers.
func sdkUserAgent() string {
	return SDKName + "/" + SDKVersion
}

// secretHeaders is the set of header names that must be redacted in logs.
var secretHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"api-key":             {},
	"cookie":              {},
	"set-cookie":          {},
}
