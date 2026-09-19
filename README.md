# TypeSafe AI Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/guchengod/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/guchengod/typesafe-sdk-go)](https://goreportcard.com/report/github.com/guchengod/typesafe-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Go SDK for [TypeSafe AI](https://typesafe.ai) — deterministic evaluation models that answer
classification and rating questions about any text or JSON you already have.

Ask up to dozens of questions in one call, and get back probabilities, labels, and rubric scores
instead of free-form text you have to parse.

- **Standard library only.** `net/http`, `encoding/json`, and `log/slog`. No runtime dependencies.
- **Concurrency-safe by construction.** A `Client` holds no per-request state and pools
  connections; every call takes a `context.Context`.
- **Resilient by default.** Jittered exponential backoff with an overall retry budget and
  `Retry-After` support.
- **Typed everything.** Typed errors you can match with `errors.As`, typed answers, and
  `SystemOneAs[T]` for your own response models.

Requires **Go 1.26 or newer**.

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

---

## Documentation

Full guides live in the [**wiki**](https://github.com/guchengod/typesafe-sdk-go/wiki):

| Guide | Covers |
| --- | --- |
| [Use Cases](https://github.com/guchengod/typesafe-sdk-go/wiki/Use-Cases) | What people build with it, with a worked triage example |
| [Questions](https://github.com/guchengod/typesafe-sdk-go/wiki/Questions) | The noul, choice, and score primitives, their options, and raw questions |
| [Responses](https://github.com/guchengod/typesafe-sdk-go/wiki/Responses) | Response accessors and the decoding rules |
| [Custom Response Models](https://github.com/guchengod/typesafe-sdk-go/wiki/Custom-Response-Models) | Decoding straight into your own struct with `SystemOneAs` |
| [Configuration](https://github.com/guchengod/typesafe-sdk-go/wiki/Configuration) | Client options, environment variables, per-call options |
| [Retry Policy](https://github.com/guchengod/typesafe-sdk-go/wiki/Retry-Policy) | Retries, backoff, and the retry budget |
| [Errors](https://github.com/guchengod/typesafe-sdk-go/wiki/Errors) | The typed error taxonomy and how to match it |
| [Logging](https://github.com/guchengod/typesafe-sdk-go/wiki/Logging) | Structured logging and credential redaction |
| [Security](https://github.com/guchengod/typesafe-sdk-go/wiki/Security) | Redaction, protected headers, redirects, bounded reads, TLS |
| [Concurrency](https://github.com/guchengod/typesafe-sdk-go/wiki/Concurrency) | Sharing one client across goroutines |
| [Examples](https://github.com/guchengod/typesafe-sdk-go/wiki/Examples) | Runnable programs in this repository |

Also see:

- [**API reference**](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go) — every exported type
  and function.
- [TypeSafe documentation](https://docs.typesafe.ai/) — what TypeSafe is and what it can do.

---

## Examples

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

## Contributing

Issues and pull requests are welcome at
[github.com/guchengod/typesafe-sdk-go](https://github.com/guchengod/typesafe-sdk-go).

```bash
go build ./...
go vet ./...
go test ./...          # unit tests
go test -run TestIntegration ./...   # live tests, need TYPESAFE_API_KEY
go test -bench . -run XXX ./...      # benchmarks, no network
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.
