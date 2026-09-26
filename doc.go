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
// Alternatively, [SystemOneInto] populates an existing struct pointer in-place.
// A top-level field that cannot represent absence — anything but a pointer, map, slice,
// interface, function, or channel — is required, unless it is tagged omitempty or omitzero. A
// missing required field is reported as an [APIResponseValidationError] naming the field.
//
// # Patterns and Model Best Practices
//
// TypeSafe's Jev model is a calibrated System One engine. It excels at qualitative classification,
// judgment, and routing, but does not perform open-ended text generation or reliable numeric arithmetic.
//
// 1. Confidence-Gated Routing:
// Choice and Score answers include a statistical Confidence (0 to 1). A proven pattern is to tier
// actions. The bands below are an example: the boundaries are yours to choose, because they trade the
// cost of a wrong automatic action against the cost of a review. The docs gate at 0.5 for a
// high-stakes decision (https://docs.typesafe.ai/confidence) and at 0.6 for a moderation queue
// (https://docs.typesafe.ai/patterns/confidence-routing).
//
//	switch {
//	case answer.Confidence >= 0.85:
//		// High confidence: act automatically without human involvement
//	case answer.Confidence >= 0.50:
//		// Medium confidence: proceed with caution or ask for confirmation
//	default:
//		// Low confidence: model is unsure ("I don't know"); route to human triage
//	}
//
// 2. Counting & Speculative Fan-Out:
// Do not ask Jev to count items or compute sums. Keep arithmetic in Go: fan out atomic Noul questions
// across candidates in a single [Client.SystemOne] request, then aggregate the matching counts in Go.
//
// 3. Score Rubrics:
// Score questions require between 2 and 10 discrete rubric levels. The resulting [ScoreAnswer.Score]
// is an expected value suited for threshold filtering (e.g. score >= 1.5). Do not use Score as a
// continuous interpolator to reconstruct exact numeric quantities.
//
// 4. Choice Questions:
// Choice questions support up to 255 options per question.
//
// 5. What the SDK Validates:
// Questions are checked locally for the shape the request schema requires: a question type, criteria
// for a choice or a score question, at least one and at most 255 options for a choice, and between 2
// and 10 levels for a score. Fields the schema makes optional — instructions above all — are sent
// only when the caller sets them; the HTTP reference page marks instructions required, but the
// request schema requires only criteria, for choice and score, so the SDK lets the API judge a
// question that omits them.
//
// # Errors
//
// Failures are typed and match with errors.As. Unsuccessful HTTP statuses map onto [APIError]
// and its status-specific subclasses ([BadRequestError], [AuthenticationError],
// [PermissionDeniedError], [NotFoundError], [UnprocessableEntityError], [RateLimitError],
// [OverloadedError], [InternalServerError]); each unwraps to [APIError], so a single check finds both forms.
// [OverloadedError] (HTTP 529) and [RateLimitError] (HTTP 429) provide RetryAfter() for backoff delays.
// Transport failures are [APIConnectionError] or [APITimeoutError], a malformed success body is
// [APIResponseValidationError], and invalid configuration or input is [SDKError]. [AsAPIError],
// [AsRateLimitError], [AsOverloadedError], [AsTimeoutError], and [AsValidationError] are convenience extractors.
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
