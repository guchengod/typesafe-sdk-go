# TypeSafe AI Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/guchengod/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/guchengod/typesafe-sdk-go)](https://goreportcard.com/report/github.com/guchengod/typesafe-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go SDK for [TypeSafe AI](https://typesafe.ai) — deterministic evaluation models that answer
classification and rating questions about any text or JSON you already have.

Ask up to dozens of questions in one call, and get back probabilities, labels, and rubric
scores instead of free-form text you have to parse.

- **Standard library only.** `net/http`, `encoding/json`, and `log/slog`. No runtime dependencies.
- **Concurrency-safe by construction.** A `Client` holds no per-request state and pools
  connections; every call takes a `context.Context`.
- **Resilient by default.** Jittered exponential backoff with an overall retry budget and
  `Retry-After` support.
- **Typed everything.** Typed errors you can match with `errors.As`, typed answers, and
  `SystemOneAs[T]` for your own response models.
- **Small and predictable.** One request shape, three question kinds, no hidden global state.

Requires **Go 1.26 or newer**.

---

## Contents

- [Installation](#installation)
- [Quickstart](#quickstart)
- [What you can build](#what-you-can-build)
- [How it works](#how-it-works)
- [Questions](#questions)
- [Responses](#responses)
- [Custom response models](#custom-response-models)
- [Configuration](#configuration)
- [Per-call options](#per-call-options)
- [Retry policy](#retry-policy)
- [Errors](#errors)
- [Logging and redaction](#logging-and-redaction)
- [Security](#security)
- [Concurrency](#concurrency)
- [Examples](#examples)

---

## Installation

```bash
go get github.com/guchengod/typesafe-sdk-go
```

```go
import typesafe "github.com/guchengod/typesafe-sdk-go"
```

---

## Quickstart

Set your API key in the environment:

```bash
export TYPESAFE_API_KEY="ts_live_..."
```

Then create a client and ask questions about your content:

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient() // reads TYPESAFE_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	response, err := client.SystemOne(
		context.Background(),
		map[string]any{
			"channel":  "email",
			"document": "I was charged twice on my credit card for invoice #4912. Please refund immediately.",
		},
		typesafe.Questions{
			"billing": typesafe.NewNoul("Is this message about billing or payments?"),
			"category": typesafe.NewChoice(
				map[string]any{
					"billing":   "Charges, refunds, or invoices",
					"technical": "Bugs, crashes, or API problems",
					"other":     nil, // nil means the label has no description
				},
				typesafe.WithInstructions("What department should handle this ticket?"),
			),
			"urgency": typesafe.NewScore(
				[]any{"Can wait until next week", "Needs attention within 48 hours", "Requires same-day attention"},
				typesafe.WithInstructions("How urgently does this customer require a response?"),
			),
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("billing p(true) = %.2f\n", response.Nouls()["billing"].Noul)
	fmt.Printf("category        = %s (confidence %.2f)\n", response.Choices()["category"].Choice, response.Choices()["category"].Confidence)
	fmt.Printf("urgency         = %.2f of 2\n", response.Scores()["urgency"].Score)
}
```

Learn what TypeSafe is, what it can do, and how to use it in the
[TypeSafe documentation](https://docs.typesafe.ai/).

---

## What you can build

Every use case below is the same call: a `state` (the content) plus a map of named questions.
The question kind decides the answer shape.

| Use case | Typical questions | Answer kinds |
| --- | --- | --- |
| **Support ticket triage** — route work to the right queue before a human reads it | Which department? Is it about billing? How urgent is it? Is it a duplicate? | choice, noul, score |
| **Content moderation** — screen user-generated text at ingest | Is this spam or abuse? Which policy category? How severe? | noul, choice, score |
| **Intent and sentiment analysis** — turn free text into fields | What is the intent? What is the tone? Is the customer at churn risk? | choice, noul |
| **LLM output evaluation** — score a model's answer before showing it | Is it grounded in the source? Does it follow the format? How good is it? | noul, score |
| **Guardrails and routing for agent pipelines** — decide what runs next | Does this need a human? Is the input in scope? Which tool should handle it? | noul, choice |
| **Data labeling and enrichment** — tag a corpus or a backlog | Is this relevant to topic X? Project management vs. personal? | noul, choice |
| **Structured extraction** — pull fields out of unstructured records | Is the sender a company or an individual? Which product is named? | choice, noul |

A worked example, triaging tickets as they arrive:

```go
type Ticket struct {
	ID       int
	Subject  string
	Body     string
	Customer string
}

questions := typesafe.Questions{
	"queue": typesafe.NewChoice(
		map[string]any{
			"billing":   "Charges, refunds, invoices, or payment problems",
			"technical": "Bugs, crashes, or API problems",
			"account":   "Logins, access, or account settings",
			"other":     nil,
		},
		typesafe.WithInstructions("Which queue should handle this ticket?"),
	),
	"needs_human": typesafe.NewNoul(
		"",
		typesafe.WithInstructions("Does this ticket require a human response, or can it be automated?"),
	),
	"priority": typesafe.NewScore(
		[]any{"routine", "elevated", "critical"},
		typesafe.WithInstructions("How severe is this issue?"),
	),
}

for _, ticket := range tickets {
	state := map[string]any{"subject": ticket.Subject, "body": ticket.Body, "customer": ticket.Customer}

	response, err := client.SystemOne(ctx, state, questions)
	if err != nil {
		log.Printf("ticket %d: %v", ticket.ID, err)
		continue
	}

	answer := response.Choices()["queue"]
	log.Printf("ticket %d -> %s (%.2f) escalate=%v priority=%.1f",
		ticket.ID, answer.Choice, answer.Confidence, response.Nouls()["needs_human"].Noul > 0.5, response.Scores()["priority"].Score)
}
```

Each answer carries the confidence the model has in it, so you can automate the confident cases
and hand the uncertain ones to a person.

---

## How it works

One call, `SystemOne`, evaluates a set of questions against a state:

- **State** — the content the questions refer to. Any JSON-encodable value: a string, a map, a
  struct, or an array. Send a ticket, a message, a document, a model's answer, or a record from
  your database.
- **Questions** — a map of names you choose to questions. The names are how you read the answers
  back, so name them after the decision they support (`"queue"`, `"is_spam"`, `"urgency"`).
- **Answers** — a response keyed by those names, one answer per question, whose type always matches
  the question's type. The response also reports the model that answered and the token usage.

```go
response, err := client.SystemOne(ctx, state, questions)
if err != nil {
	return err
}
fmt.Println(response.Nouls()["is_billing"].Noul)     // 0.98
fmt.Println(response.Choices()["queue"].Choice)      // billing
fmt.Println(response.Scores()["priority"].Score)     // 1.7
```

To see which models your account can use, and which one `model` accepts:

```go
models, err := client.Models.List(ctx)
if err != nil {
	return err
}
for _, model := range models.Models {
	fmt.Printf("%s\t%s\t%s\n", model.Name, model.ReleaseDate, model.Description)
}
```

---

## Questions

Three question kinds are supported. All three accept `instructions` — what the model should
decide — as a string, a JSON object, or an array, and all are answered with a probability
distribution you can threshold yourself.

Questions travel as a map because the names are yours to choose: they are the keys you read the
answers back with, so they cannot be struct fields. The map's values are typed as
`typesafe.Question`, which means a value that is not a question fails to compile. For a question
shape this version does not model, wrap a plain map in `typesafe.RawQuestion` — see
[Raw questions](#raw-questions). A caller holding an untyped `map[string]any` passes it to the
client directly; `SystemOne` accepts any of the containers `typesafe.NormalizeQuestions` documents
and validates them at run time.

### Noul — yes/no

The probability that a statement holds, in `[0, 1]`. Values near `1` mean yes or true, values near
`0` mean no or false, and values near `0.5` mean the content does not decide it.

```go
typesafe.NewNoul("Is this message spam?")

typesafe.NewNoul("Is this message spam?", typesafe.WithNoulCriteria(typesafe.NoulCriteria{
	True:  "Unsolicited advertising or phishing",
	False: "A legitimate conversation",
}))
```

`NewNoul(instructions any, opts ...QuestionOption) *NoulQuestion` — the instructions may be a
string, a map, or a slice, and may be `nil` when criteria alone describe the question. Criteria are
free-form: use a string, or a structured object when examples help.

```go
typesafe.NewNoul("", typesafe.WithNoulCriteria(typesafe.NoulCriteria{
	True: map[string]any{"meaning": "Payments or invoices", "examples": []any{"charged twice", "refund"}},
}))
```

Threshold the answer where your decision needs it:

```go
if response.Nouls()["is_spam"].Noul > 0.9 {
	quarantine(message)
}
```

### Choice — categorical

The best-matching label, with the model's confidence and a probability for every label. Use it
whenever the options are known ahead of time and exactly one applies.

```go
typesafe.NewChoice(
	map[string]any{
		"billing":   "Charges, refunds, or invoices",
		"technical": "Bugs, crashes, or API problems",
		"other":     nil, // nil means "no description"
	},
	typesafe.WithInstructions("Which department should handle this request?"),
)
```

`NewChoice(criteria map[string]any, opts ...QuestionOption) *ChoiceQuestion`. Each criteria value is
a label description: a string, a JSON object, an array, or `nil` for a label the name already
describes. A choice question with no criteria is rejected before the request is sent.

```go
answer := response.Choices()["category"]
fmt.Println(answer.Choice)         // billing
fmt.Println(answer.Confidence)     // 0.87
fmt.Println(answer.Probabilities)  // map[billing:0.87 technical:0.09 other:0.04]
```

### Score — ordinal rubric

An expected value across an ordered rubric whose levels start at `0`. Use it for priority, quality,
and severity scales where "how much" matters more than "which one".

```go
typesafe.NewScore(
	[]any{"routine", "elevated", "critical emergency"},
	typesafe.WithInstructions("Rate the severity of this issue"),
)
```

`NewScore(criteria []any, opts ...QuestionOption) *ScoreQuestion`. The score is the
probability-weighted average of the rubric levels, so it may fall between two levels; the reported
legend and probabilities are keyed by level, so you can always read the distribution. An empty
rubric is rejected with a `*typesafe.ScoreError`.

```go
answer := response.Scores()["urgency"]
fmt.Println(answer.Score)                  // 1.7
fmt.Println(answer.Legend[2])              // "critical emergency"
fmt.Println(answer.Probabilities[2])       // 0.8
```

### Question options

| Option | Effect |
| --- | --- |
| `WithInstructions(any)` | The question to ask: a string, a JSON object, or an array. Overrides instructions passed positionally. |
| `WithNoulCriteria(NoulCriteria)` | Describes the yes and no outcomes of a noul question. |
| `WithQuestionField(key string, value any)` | Adds a field this version does not model to the wire form; a colliding field replaces the modeled one. |

Instructions that are `nil` or blank are left off the wire entirely, so
`typesafe.NewNoul("", typesafe.WithNoulCriteria(...))` sends a question described only by its
criteria. A structured instruction (an object or an array) is always sent, even when empty.

### Raw questions

`RawQuestion` sends a question as a plain map, for request fields this version does not model:

```go
typesafe.Questions{
	"billing": typesafe.RawQuestion{"type": "noul", "instructions": "Is this about billing?"},
	"queue": typesafe.RawQuestion{
		"type":       "choice",
		"criteria":   map[string]any{"billing": nil, "other": nil},
		"weight":     3, // a hypothetical field a newer API accepts
	},
}
```

`NormalizeQuestions(questions)` is the same validation the client applies. It accepts
`Questions`, `map[string]Question`, the typed question maps (`map[string]*NoulQuestion` and
siblings), `map[string]any`, and `RawQuestion`, and rejects an empty set with
`At least one question is required.`

---

## Responses

`SystemOne` returns a `*SystemOneResponse`:

```go
response.Model             // the model that answered; may differ from the requested alias
response.Usage             // Usage{InputTokens, OutputTokens *int} — nil when not reported
response.Answers           // map[string]Answer, keyed by question name
response.Nouls()           // map[string]*NoulAnswer
response.Choices()         // map[string]*ChoiceAnswer
response.Scores()          // map[string]*ScoreAnswer
response.RawRequestID()    // x-typesafe-request-id, or ""
response.RequestID()       // (string, error) — errors when the header is absent
response.RawBody()         // the payload exactly as received
response.RawHTTPResponse() // (*http.Response, error)
```

Answers carry the primitive's data: `NoulAnswer{Type, Noul}`, `ChoiceAnswer{Type, Choice,
Confidence, Probabilities}`, and `ScoreAnswer{Type, Score, Confidence, Legend, Probabilities}`.
`ScoreAnswer.MarshalJSON` encodes `Legend` and `Probabilities` with their integer levels as JSON
object keys, so a response round-trips.

`Usage` is a value type with a `String` method, so `fmt.Println(response.Usage)` prints
`Usage{input_tokens: 492, output_tokens: 72}`.

### Decoding rules

- `model` and `usage` are required. A success response missing either — or holding a field of the
  wrong shape, including an explicit `null` — is reported as an `*APIResponseValidationError`
  naming the dotted field path, such as `answers.tone.confidence`.
- `answers` is optional and defaults to empty.
- An answer whose `type` is not `noul`, `choice`, or `score` is dropped from `Answers` and the
  grouped accessors, so a newer server cannot break an older client. It stays reachable through
  `RawBody()`.
- `client.Models.List(ctx)` returns a `*ListModelsResponse`; every field of every model (`name`,
  `description`, `release_date`) is required.

A malformed response is never silently coerced into a plausible one: a missing probability, a
`null` where a number belongs, or a non-integer rubric level all surface as an error naming the
field, rather than a zero value you might act on.

---

## Custom response models

`SystemOneAs[T]` decodes the payload into a struct you define, so the compiler knows the shape of
your answers:

```go
type TicketAnalysis struct {
	Model   string                `json:"model"`
	Usage   typesafe.Usage        `json:"usage"`
	Tone    typesafe.ChoiceAnswer `json:"tone"`    // lifted from answers
	Billing *typesafe.NoulAnswer  `json:"billing"` // optional: a pointer can be absent
	Summary string                `json:"summary,omitempty"`
}

ticket, err := typesafe.SystemOneAs[TicketAnalysis](ctx, client, state, typesafe.Questions{
	"tone":    typesafe.NewChoice(map[string]any{"calm": nil, "angry": nil}),
	"billing": typesafe.NewNoul("Is this about billing?"),
})
if err != nil {
	return err
}
fmt.Printf("tone=%s billing=%.2f\n", ticket.Tone.Choice, ticket.Billing.Noul)
```

**Answer lifting.** `T` may declare the envelope fields (`model`, `usage`, `answers`) and, in
addition, one field per answer, named after the question. Each such field is populated from the
matching entry of `answers`. A real envelope field is never displaced by an answer of the same
name, and answer types this version does not model are dropped, exactly as in `Client.SystemOne`.

To keep every answer keyed by question name, declare `Answers typesafe.Answers \`json:"answers"\``;
`typesafe.Answers` decodes each entry from its `type` field, which a bare `map[string]typesafe.Answer`
cannot do. A nested struct works too — `Answers struct { Tone typesafe.ChoiceAnswer \`json:"tone"\` } \`json:"answers"\``.

**Required fields.** A top-level field is required unless it can represent absence — a pointer, map,
slice, interface, function, or channel holds `nil` for "not reported" — or it is tagged
`omitempty`/`omitzero`. Untagged, embedded, and unexported fields are not checked. A missing
required field is reported as an `*APIResponseValidationError` naming the field rather than
silently decoding to a zero value. Only the top level is checked; nested structures use standard
`encoding/json` semantics.

---

## Configuration

### Client options

Explicit options win over environment variables, which win over the SDK defaults. Blank or
whitespace-only environment values are treated as unset.

```go
client, err := typesafe.NewClient(
	typesafe.WithAPIKey("ts_live_..."),
	typesafe.WithBaseURL("https://api.typesafe.ai"),
	typesafe.WithDefaultModel("jev-latest"),
	typesafe.WithTimeout(15*time.Second),
	typesafe.WithRetryPolicy(typesafe.DefaultRetryPolicy()),
	typesafe.WithHeader("X-Environment", "production"),
	typesafe.WithLogger(logger),
)
if err != nil {
	log.Fatal(err)
}
defer client.Close()
```

| Option | Purpose | Environment fallback |
| --- | --- | --- |
| `WithAPIKey(string)` | Bearer credential sent as `Authorization`. Required. | `TYPESAFE_API_KEY` |
| `WithBaseURL(string)` | API root; trailing slashes are removed. | `TYPESAFE_BASE_URL` |
| `WithDefaultModel(string)` | Model used when a call names none. | `TYPESAFE_DEFAULT_MODEL` |
| `WithTimeout(time.Duration)` | HTTP timeout for one attempt. Must be positive. Adopted from `WithHTTPClient`'s timeout when one is supplied and no explicit timeout is given. | — (default `10s`) |
| `WithRetryPolicy(*RetryPolicy)` | Retry policy for every call. | — (default policy) |
| `WithHeader(key, value string)` | Default header sent with every request. | — |
| `WithHeaders(map[string]string)` | Merges several default headers. | — |
| `WithHTTPClient(*http.Client)` | Supplies the client and its transport. Mutually exclusive with `WithTransport`. | — |
| `WithTransport(http.RoundTripper)` | Supplies the transport only. Mutually exclusive with `WithHTTPClient`. | — |
| `WithLogger(*slog.Logger)` | Structured logger for requests, responses, and retries. | `TYPESAFE_LOG_LEVEL` |

`NewClient` fails with an `*SDKError` when no API key is available, when the timeout is not
positive, when both `WithHTTPClient` and `WithTransport` are supplied, or when a retry policy fails
validation. `(*Client).Config()` returns the resolved configuration, `(*Client).HTTPClient()` the
underlying `*http.Client`, and `(*Client).Close()` releases the connection pool (idempotent).

### Environment variables

| Variable | Meaning |
| --- | --- |
| `TYPESAFE_API_KEY` | API key. Required unless `WithAPIKey` supplies one. |
| `TYPESAFE_BASE_URL` | API root. Default `https://api.typesafe.ai`. |
| `TYPESAFE_DEFAULT_MODEL` | Default model. Default `jev-latest`. |
| `TYPESAFE_LOG_LEVEL` | `debug`, `info`, `warn`, `warning`, `error`, or `off`. Unset means the SDK logs nothing. |

The per-attempt timeout has no environment variable; set it with `WithTimeout` or a per-call
`WithCallTimeout`. The names above are exported as `typesafe.APIKeyEnv`, `typesafe.BaseURLEnv`,
`typesafe.DefaultModelEnv`, and `typesafe.LogLevelEnv`.

---

## Per-call options

`SystemOne` and `Models.List` accept request options that override the client for one call without
mutating it:

```go
response, err := client.SystemOne(ctx, state, questions,
	typesafe.WithModel("jev-fast"),
	typesafe.WithCallTimeout(5*time.Second),
	typesafe.WithCallRetry(policy),
	typesafe.WithCallHeader("X-Correlation-ID", "1234"),
	typesafe.WithExtraBody(map[string]any{"max_output_tokens": 512}),
)
```

| Option | Effect |
| --- | --- |
| `WithModel(string)` | Overrides the model for this call. |
| `WithCallTimeout(time.Duration)` | Overrides the HTTP timeout for one attempt of this call. Must be positive. |
| `WithCallRetry(*RetryPolicy)` | Overrides the retry policy for this call. The client's policy is left untouched. |
| `WithCallHeader(key, value string)` | Sets or overrides one request header. |
| `WithCallHeaders(map[string]string)` | Merges several request headers. |
| `WithExtraBody(map[string]any)` | Shallow-merges extra top-level body fields over `state`, `model`, and `questions`. Last write wins: a colliding key replaces the SDK's value and object values are replaced, not deep-merged. |

---

## Retry policy

Retries are on by default. `DefaultRetryPolicy()` returns:

| Field | Default | Meaning |
| --- | --- | --- |
| `MaxRetries` | `2` | Retries after the initial attempt. `0` disables retries. |
| `BackoffInitial` | `500ms` | First backoff delay; doubles per attempt up to `BackoffMax`. `0` disables backoff. |
| `BackoffMax` | `5s` | Cap on a single backoff delay. `0` disables backoff. |
| `BackoffJitter` | `0.25` | Fraction of each delay randomly subtracted, between `0` and `1`. |
| `HTTPStatuses` | `408`, `429`, and every `5xx` | Status codes that are retried. `DefaultRetryStatuses()` builds this set. |
| `RespectRetryAfter` | `true` | Honors `retry-after-ms` and `Retry-After` (seconds or HTTP date). |
| `APIConnectionError` | `true` | Retries failures that never produced an HTTP response. |
| `APITimeoutError` | `true` | Retries requests that exceeded their timeout. |
| `RetryErrors` | `nil` | Extra sentinel errors that trigger a retry; matched with `errors.Is`. |
| `Predicate` | `nil` | `func(error) bool` consulted for every failure; `true` triggers a retry. |
| `Timeout` | `30s` (`DefaultRetryBudget`) | Total budget for one call, covering the initial attempt, retries, and delays. A retry whose delay would reach the budget is abandoned and the last error is returned. `nil` disables the budget. |
| `Sleep` | `nil` | Seam for tests: `func(ctx, delay) error` replaces the context-aware timer. |
| `RandFloat` | `nil` | Seam for tests: `func() float64` in `[0, 1)` replaces the jitter source. |

Start from the default and adjust, or set only the fields you care about:

```go
policy := typesafe.DefaultRetryPolicy()
policy.MaxRetries = 4
policy.BackoffInitial = 250 * time.Millisecond
policy.Timeout = new(15 * time.Second)

client, err := typesafe.NewClient(typesafe.WithRetryPolicy(policy))
```

A cancelled or expired context is never retried. `(*RetryPolicy).Validate()` reports malformed
policies, `(*RetryPolicy).Clone()` deep-copies one, and `(*RetryPolicy).Execute(ctx, fn)` runs a
function under the policy. When a retry happens, the SDK adds the `X-TypeSafe-Retry-Count` header
(`1` for the first retry, `2` for the second, …) so the server can trace attempts.

---

## Errors

Every error the SDK returns implements `typesafe.TypeSafeError`. Unsuccessful HTTP statuses map
onto typed errors, and each typed error unwraps to `*APIError`, so `errors.As` finds both the
specific and the general form.

| Status | Type |
| --- | --- |
| 400 | `*BadRequestError` |
| 401 | `*AuthenticationError` |
| 403 | `*PermissionDeniedError` |
| 404 | `*NotFoundError` |
| 422 | `*UnprocessableEntityError` |
| 429 | `*RateLimitError` (adds `RetryAfterMS *float64` and `RetryAfter() (time.Duration, bool)`) |
| 5xx | `*InternalServerError` |
| any other non-2xx | `*APIError` |

Non-HTTP failures are also typed:

| Type | Raised for |
| --- | --- |
| `*SDKError` | Invalid configuration or input, an unencodable body, an unreadable response. Never an unsuccessful status. |
| `*APIConnectionError` | The request never produced an HTTP response. |
| `*APITimeoutError` | The request exceeded its timeout; carries `Timeout time.Duration`. |
| `*APIResponseValidationError` | A success status whose body does not match the schema; carries `FieldPath`. |
| `*ScoreError` | A score question with an empty rubric. |

`*APIError` carries `Status`, `Body`, `RawBody`, `Headers`, `Endpoint` (`"METHOD URL"`, with
credentials, query, and fragment removed), `RequestID`, and `Message`. Its `Error()` reads
`<METHOD URL>: <status> <message> (request_id=<id>)`, omitting the parts that do not apply.

```go
response, err := client.SystemOne(ctx, state, questions)
if err != nil {
	var rateLimit *typesafe.RateLimitError
	var validation *typesafe.APIResponseValidationError
	var apiErr *typesafe.APIError
	var timeout *typesafe.APITimeoutError

	switch {
	case errors.As(err, &rateLimit):
		if wait, ok := rateLimit.RetryAfter(); ok {
			log.Printf("rate limited, retry after %v", wait)
		}
	case errors.As(err, &validation):
		log.Printf("invalid response field %q", validation.FieldPath)
	case errors.As(err, &apiErr):
		log.Printf("http %d (request_id=%s): %s", apiErr.Status, apiErr.RequestID, apiErr.Message)
	case errors.As(err, &timeout):
		log.Printf("timed out after %v", timeout.Timeout)
	default:
		log.Printf("system one failed: %v", err)
	}
	return err
}
```

The same checks are available as helpers: `typesafe.AsAPIError(err)`, `typesafe.AsRateLimitError(err)`,
`typesafe.AsTimeoutError(err)`, and `typesafe.AsValidationError(err)`, each returning `(value, bool)`.

---

## Logging and redaction

The SDK logs through `log/slog`. It logs nothing unless you configure it: without `WithLogger` and
without `TYPESAFE_LOG_LEVEL`, records are discarded, which is what library code should do.

```go
// Quick default: send info records to stderr.
// export TYPESAFE_LOG_LEVEL=debug

// Or own the handler:
logger := slog.New(typesafe.NewRedactingHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelDebug,
})))
client, err := typesafe.NewClient(typesafe.WithLogger(logger))
```

- `ParseLogLevel(name)` converts `debug`, `info`, `warn`, `warning`, `error`, or `off` into a `slog.Level`.
- Requests, responses, and retries log at `info`; `request.wire` and the header and body attributes
  of a response log at `debug`.
- `NewRedactingHandler(handler)` wraps any `slog.Handler` and replaces credential-bearing
  attributes with `"***"` before they reach it. It matches well-known credential headers
  (`Authorization`, `Proxy-Authorization`, `X-API-Key`, `API-Key`, `Cookie`, `Set-Cookie`) and,
  additionally, any attribute whose name contains `token` or `secret`. Values held in a
  `map[string]string` attribute are inspected key by key.

Headers are redacted; **bodies are not**. Keep credentials out of `state`, and prefer log levels
below `debug` in production.

---

## Security

- **Credential redaction.** The SDK redacts well-known credential headers, plus any header whose
  name contains `token` or `secret`, from every record it logs. `NewRedactingHandler` extends the
  guarantee to records you route through the SDK's logger.
- **URL sanitization.** Error endpoints are rendered as `"METHOD URL"` with userinfo, query
  parameters, and fragments removed, so a credential embedded in a URL never reaches an error
  message or a log line.
- **Protected headers.** `Authorization`, `Accept`, `User-Agent`, `X-TypeSafe-SDK`, and
  `X-TypeSafe-Runtime` are applied after your default and per-call headers and cannot be displaced,
  whatever case you spell them in. A caller-supplied `X-TypeSafe-Retry-Count` is stripped, and
  `Content-Type` is set only when the request has a body.
- **No redirects.** The SDK's own client does not follow redirects, so the API key is never
  replayed to another host. A 3xx response surfaces as an `*APIError`. A client you supply is used
  as-is.
- **Bounded responses.** Response bodies are read through a 16 MB limit
  (`typesafe.MaxResponseBodyLimit`), and an oversized payload is rejected rather than truncated into
  a confusing decode failure. Error messages include at most `MaxErrorBodyLength` (200) bytes of an
  unrecognized body.
- **Strict decoding.** Missing fields, `null` where a value belongs, and wrong-typed fields in a
  success response are reported as errors naming the field, never silently decoded into a zero
  value that reads as a real answer.
- **TLS 1.2+.** The SDK's own transport refuses TLS versions below 1.2, enables HTTP/2 when
  available, and pools connections. A transport you supply is used as-is.

---

## Concurrency

A `Client` is safe for concurrent use by any number of goroutines. It holds no per-request state,
its configuration is read-only after construction, and the underlying `http.Client` pools
connections (`MaxIdleConnsPerHost: 100`). Create one client, share it, and pass a
`context.Context` per call for cancellation and deadlines:

```go
var wg sync.WaitGroup
for _, ticket := range tickets {
	wg.Add(1)
	go func(ticket string) {
		defer wg.Done()
		response, err := client.SystemOne(ctx, ticket, questions)
		if err != nil {
			log.Printf("%s: %v", ticket, err)
			return
		}
		fmt.Printf("%s -> %s\n", ticket, response.Choices()["category"].Choice)
	}(ticket)
}
wg.Wait()
```

Use a semaphore or a worker pool if you need to bound how many calls are in flight at once. `Close`
releases idle connections; call it once, when the program is done with the client.

---

## Examples

Runnable programs live in this repository:

| Example | Shows |
| --- | --- |
| [`examples/quickstart`](examples/quickstart/main.go) | One `SystemOne` call with all three primitives, printing every answer. |
| [`examples/advanced`](examples/advanced/main.go) | `SystemOneAs` with a custom model, `Models.List`, per-call options, `WithExtraBody`, typed error handling, and a redacting logger. |

```bash
export TYPESAFE_API_KEY="ts_live_..."
go run ./examples/quickstart
go run ./examples/advanced
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.
