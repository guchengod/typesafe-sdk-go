// Package typesafe is the official Go SDK for TypeSafe AI (https://typesafe.ai).
//
// The TypeSafe AI platform evaluates deterministic classification and rating primitives through
// System One models. A [Client] sends one request that answers any number of named questions
// about a state, and returns every answer keyed by the name you supplied.
//
// # Primitives
//
// Three question kinds are supported:
//
//   - Noul: the probability that a statement holds, from 0 to 1.
//   - Choice: the best-matching criteria key, its confidence, and a probability per key.
//   - Score: an expected value across an ordered rubric whose levels start at 0.
//
// # Quickstart
//
//	client, err := typesafe.NewClient() // reads TYPESAFE_API_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer client.Close()
//
//	response, err := client.SystemOne(ctx,
//		map[string]any{"document": "I was charged twice. Please fix this ASAP."},
//		typesafe.Questions{
//			"billing": typesafe.NewNoul(
//				"Is this ticket about billing?",
//				typesafe.WithNoulCriteria(typesafe.NoulCriteria{
//					True:  "Charges, refunds, or invoices",
//					False: "Anything else",
//				}),
//			),
//			"category": typesafe.NewChoice(
//				map[string]any{"billing": nil, "technical": nil, "other": nil},
//				typesafe.WithInstructions("What is this ticket about?"),
//			),
//			"urgency": typesafe.NewScore(
//				[]any{"can wait", "this week", "today"},
//				typesafe.WithInstructions("How urgent is this issue?"),
//			),
//		},
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	fmt.Printf("billing: %.2f\n", response.Nouls()["billing"].Noul)
//	fmt.Printf("category: %s (%.2f)\n", response.Choices()["category"].Choice, response.Choices()["category"].Confidence)
//	fmt.Printf("urgency: %.2f\n", response.Scores()["urgency"].Score)
//
// # Custom Response Models
//
// [SystemOneAs] decodes the payload into a caller-defined struct, so the compiler knows the shape
// of your answers. The struct may declare the envelope fields and one field per answer, named
// after the question:
//
//	type Ticket struct {
//		Model   string                 `json:"model"`
//		Usage   typesafe.Usage         `json:"usage"`
//		Billing typesafe.NoulAnswer    `json:"billing"`
//		Tone    *typesafe.ChoiceAnswer `json:"tone"` // optional: a pointer can be absent
//	}
//
//	result, err := typesafe.SystemOneAs[Ticket](ctx, client, state, questions)
//
// A top-level field that cannot represent absence — anything but a pointer, map, slice,
// interface, function, or channel — is required, unless it is tagged omitempty or omitzero. A
// missing required field is reported as an [APIResponseValidationError] naming the field.
//
// # Errors
//
// Failures are typed and match with errors.As. Unsuccessful HTTP statuses map onto [APIError]
// and its status-specific subclasses ([BadRequestError], [AuthenticationError],
// [PermissionDeniedError], [NotFoundError], [UnprocessableEntityError], [RateLimitError],
// [InternalServerError]); each unwraps to [APIError], so a single check finds both forms.
// Transport failures are [APIConnectionError] or [APITimeoutError], a malformed success body is
// [APIResponseValidationError], and invalid configuration or input is [SDKError]. [AsAPIError],
// [AsRateLimitError], [AsTimeoutError], and [AsValidationError] are convenience extractors.
//
// # Configuration
//
// [NewClient] resolves configuration from its [Option] values first, then the environment
// (TYPESAFE_API_KEY, TYPESAFE_BASE_URL, TYPESAFE_DEFAULT_MODEL, TYPESAFE_LOG_LEVEL), then the SDK
// defaults. [Client.SystemOne] and [ModelsService.List] accept [RequestOption] values that
// override the client for a single call without mutating it: [WithModel], [WithCallTimeout],
// [WithCallRetry], [WithCallHeader], [WithCallHeaders], and [WithExtraBody].
//
// # Design
//
// The SDK is engineered for high concurrency and safety:
//
//   - Functional options: [Option] configures a [Client], [RequestOption] a single call, and
//     [QuestionOption] a single question.
//   - Resilient retries: jittered exponential backoff with an overall budget, honoring the
//     retry-after-ms and Retry-After headers. See [RetryPolicy] and [DefaultRetryPolicy].
//   - Security by design: credential headers are redacted from logs ([NewRedactingHandler]
//     extends that to your own records), credentials in URLs are stripped from error endpoints,
//     response bodies are bounded to 16 MB, and the SDK's transport refuses TLS below 1.2.
//     Headers the SDK owns cannot be displaced by caller-supplied ones.
//   - Concurrency: a [Client] holds no per-request state and is safe for concurrent use; every
//     call takes a context.Context for cancellation and deadlines.
//   - Modern Go: for range over integers, min and max, slices and maps helpers, and generics via
//     [SystemOneAs]. Standard library only, and Go 1.26 or newer.
package typesafe
