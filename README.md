# TypeSafe AI Go SDK

[![CI](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/guchengod/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go)
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
- **A CLI when you want one.** `cmd/typesafe` wraps the same client for shell scripts and
  pipelines; install it with `go install` or grab a binary from
  [Releases](https://github.com/guchengod/typesafe-sdk-go/releases).

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
	defer func() { _ = client.Close() }()

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

## Patterns & Model Guidance

TypeSafe's **Jev** is a calibrated System One model designed for fast, discrete qualitative decisions rather than continuous generation or arithmetic calculation. Keep determinism and arithmetic in Go code, and use Jev for structured judgments.

### 1. Confidence-Gated Routing (置信度三段式路由)

Every `Choice` and `Score` answer includes a `Confidence` score between `0.0` and `1.0`, derived from how probability mass concentrates across alternatives. Use confidence as an operational control axis — the thresholds below are an example, not an API-defined rule: the docs gate at `0.5` for a high-stakes decision ([confidence](https://docs.typesafe.ai/confidence)) and at `0.6` for a moderation queue ([confidence routing](https://docs.typesafe.ai/patterns/confidence-routing)).

```go
category := response.Choices()["category"]

switch {
case category.Confidence >= 0.85:
	// High confidence: act automatically without human intervention
	dispatchTicket(category.Choice)

case category.Confidence >= 0.50:
	// Medium confidence: proceed with caution, flag for confirmation
	flagForOperatorReview(category.Choice)

default:
	// Low confidence: model is genuinely unsure ("I don't know"); route to human triage
	routeToHumanTriage()
}
```

### 2. Counting & Speculative Fan-Out (计数与投机性扇出)

Jev is not an arithmetic calculator and does not count items reliably. When counting items that match a condition across a slice or list, **keep the arithmetic in Go**: fan out atomic `Noul` questions in a single `SystemOne` request, then sum the matching probabilities in code:

```go
items := []string{"urgent payment bug", "typo in docs", "security vulnerability"}

questions := make(typesafe.Questions, len(items))
for i, item := range items {
	questions[fmt.Sprintf("item_%d", i)] = typesafe.NewNoul(
		fmt.Sprintf("Does this issue describe a severe production or security incident: %q?", item),
	)
}

// Jev ingests the state once and evaluates all questions in parallel
resp, err := client.SystemOne(ctx, map[string]any{"items": items}, questions)
if err != nil {
	log.Fatal(err)
}

// Aggregate in Go code
severeCount := 0
for i := range items {
	if resp.Nouls()[fmt.Sprintf("item_%d", i)].Noul >= 0.5 {
		severeCount++
	}
}
fmt.Printf("Severe issues: %d of %d\n", severeCount, len(items))
```

### 3. Score Rubric: Threshold Filtering vs Arithmetic (评分题准则)

`ScoreAnswer.Score` is the probability-weighted average across discrete levels (`0` to `len(levels)-1`):
- **Do:** Use `score` for threshold gating (e.g. `urgency.Score >= 1.5`).
- **Don't:** Do not use `score` as a continuous linear interpolator to reconstruct exact numerical quantities between levels.
- **Bounds:** Score rubrics require between **2 and 10 levels**. Choice questions accept up to **255 options**.

---

## Command line

The repository ships a CLI, built on the standard library only, for use without writing Go:

```bash
# Install the latest release into $(go env GOPATH)/bin
go install github.com/guchengod/typesafe-sdk-go/cmd/typesafe@latest

# Or download a binary for your platform
# https://github.com/guchengod/typesafe-sdk-go/releases
```

```bash
export TYPESAFE_API_KEY="ts_live_..."

typesafe models --format text

typesafe ask \
  --state '{"body": "I was charged twice on invoice #4912."}' \
  --noul 'billing=Is this about billing?' \
  --choice 'queue=billing:Charges,technical:API issues,other' \
  --score 'urgency=routine,elevated,critical' \
  --format text
```

```text
model    jev-1.13.0
usage    Usage{input_tokens: 367, output_tokens: 69}
billing  noul 0.9900
queue    choice billing (confidence 1.00)
urgency  score 0.53 of 2 (confidence 0.20)
```

`--format json` (the default) prints the API response, so it composes with `jq`:

```bash
curl -s https://example.test/ticket.json | typesafe ask --state - --questions questions.json | jq '.answers'
```

Exit codes tell a script what happened: `0` success, `1` usage error, `2` the API returned an
error status, `3` the request never reached the API. See the
[CLI guide](https://github.com/guchengod/typesafe-sdk-go/wiki/CLI) for every flag.

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
| [CLI](https://github.com/guchengod/typesafe-sdk-go/wiki/CLI) | The `typesafe` command: flags, output formats, exit codes |
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
golangci-lint run ./...              # linters
go build ./cmd/typesafe              # the CLI
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.
