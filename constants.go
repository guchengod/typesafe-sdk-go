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

	// ModelJevLatest is the alias for the latest stable model release (currently jev-1.13.0).
	ModelJevLatest = "jev-latest"

	// ModelJevPreview is the alias for the latest preview release (currently jev-1.14.0).
	ModelJevPreview = "jev-preview"

	// ModelJev114 is the versioned ID for Jev 1.14.
	ModelJev114 = "jev-1.14.0"

	// ModelJev113 is the versioned ID for Jev 1.13.
	ModelJev113 = "jev-1.13.0"

	// DefaultTimeout is the default HTTP timeout for operations.
	DefaultTimeout = 10 * time.Second

	// SDKName is the name of this SDK.
	SDKName = "typesafe-sdk-go"

	// SDKVersion is the current version of this SDK.
	SDKVersion = "0.13.0"

	// MaxErrorBodyLength limits the raw error body length included in error messages.
	MaxErrorBodyLength = 200

	// MaxRequestBodyLimit is the maximum request body size (32 MiB) accepted by the API.
	MaxRequestBodyLimit = 32 * 1024 * 1024

	// MaxResponseBodyLimit limits the maximum response body read (32 MiB) matching API spec.
	MaxResponseBodyLimit = 32 * 1024 * 1024

	// MaxImageSizeLimit is the maximum size (16 MiB) for an individual image file.
	MaxImageSizeLimit = 16 * 1024 * 1024

	// MinCompressionBytes is the minimum request body size (1 KiB) for automatic compression.
	MinCompressionBytes = 1024
)

// API Endpoint paths.
const (
	SystemOnePath = "/v1/systemone"
	ModelsPath    = "/v1/models"
)

// HTTP Status codes.
const (
	// StatusOverloaded is the HTTP 529 status code returned when TypeSafe is temporarily overloaded.
	StatusOverloaded = 529

	// StatusPayloadTooLarge is HTTP 413 Payload Too Large.
	StatusPayloadTooLarge = 413

	// StatusUnsupportedMediaType is HTTP 415 Unsupported Media Type.
	StatusUnsupportedMediaType = 415
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

	// Danger Zone feature headers.
	HeaderDangerZoneImages      = "TypeSafe-Danger-Zone-Images"
	HeaderDangerZoneLongContext = "TypeSafe-Danger-Zone-Long-Context"

	// Compression headers.
	HeaderContentEncoding = "Content-Encoding"
	EncodingGzip          = "gzip"
	EncodingZstd          = "zstd"
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
