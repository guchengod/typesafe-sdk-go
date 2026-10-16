# TypeSafe AI Go SDK

[![CI](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/guchengod/typesafe-sdk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/guchengod/typesafe-sdk-go.svg)](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

[简体中文](README.md) | **English**

---

Official Go client SDK for [TypeSafe AI](https://typesafe.ai) — calibrated, deterministic System One evaluation models designed for classification, qualitative grading, verification, and bounding box object localization over text, structured JSON, and images.

Ask dozens of questions in a single parallel call and receive calibrated probabilities, categorical labels, scores, and bounding box coordinates rather than unstructured LLM text that requires fragile parsing.

- **Standard Library Only**: Built with `net/http`, `encoding/json`, and `log/slog`. Zero runtime external dependencies.
- **Concurrency-Safe**: Client instances are stateless across calls and share HTTP connection pools. All methods accept `context.Context`.
- **Automatic Gzip Compression**: Transparently compresses request bodies $\ge 1\text{ KiB}$ with standard gzip.
- **Multimodal & Object Localization**: First-class support for `Image` assets and `Box` bounding box question primitives.
- **Resilient Retries**: Exponential backoff with jitter, honoring upstream `Retry-After` response headers.
- **Strongly Typed**: Typed error taxonomy (`errors.As`), typed responses, and `SystemOneAs[T]` for custom struct decoding.
- **Built-in CLI**: `cmd/typesafe` for pipelines, shell scripts, and terminal inspection.

> Requires **Go 1.26 or newer**.

---

## Table of Contents

- [Installation](#installation)
- [Quickstart](#quickstart)
- [Question Primitives](#question-primitives)
- [Multimodal Images & Object Localization (Box)](#multimodal-images--object-localization-box)
- [Custom Response Models (`SystemOneAs`)](#custom-response-models-systemoneas)
- [Patterns & Model Guidance](#patterns--model-guidance)
- [🤖 AI Coding Agent System Prompt](#-ai-coding-agent-system-prompt)
- [Command Line Interface (CLI)](#command-line-interface-cli)
- [Error Handling](#error-handling)
- [Documentation & Examples](#documentation--examples)
- [Contributing](#contributing)
- [License](#license)

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
	client, err := typesafe.NewClient() // reads TYPESAFE_API_KEY automatically
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
	fmt.Printf("urgency score   = %.2f of 2 (confidence %.2f)\n", response.Scores()["urgency"].Score, response.Scores()["urgency"].Confidence)
}
```

---

## Question Primitives

TypeSafe provides four atomic question types for deterministic decision-making:

| Primitive | Constructor | Result Output | Typical Use Cases |
| --- | --- | --- | --- |
| **Noul** | `NewNoul(instruction, ...)` | `NoulAnswer.Noul` (probability from 0.0 to 1.0) | Yes/No decisions, guardrails, compliance filtering |
| **Choice** | `NewChoice(criteria, ...)` | `ChoiceAnswer.Choice`, `Probabilities`, `Confidence` | Routing, triage, classification (up to 255 options) |
| **Score** | `NewScore(criteria, ...)` | `ScoreAnswer.Score` (weighted expectation), `Confidence`, `Legend` | Rubric rating, urgency, severity (2 to 10 levels) |
| **Box** | `NewBox(instruction, ...)` | `BoxAnswer.Box` (edges: Top, Bottom, Left, Right), `Confidence` | Object localization, cropping coordinates |

---

## Multimodal Images & Object Localization (Box)

TypeSafe supports embedding images in `state`, instructions, or criteria, as well as locating visual elements with the `Box` question type.

> Image support and Box questions require the `jev-preview` model and the `TypeSafe-Danger-Zone-Images: true` header (enabled via `typesafe.WithImages(true)` on client configuration or `typesafe.WithCallImages(true)` per call).

```go
package main

import (
	"context"
	"fmt"
	"log"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	client, err := typesafe.NewClient(typesafe.WithImages(true))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	// Load image from disk (PNG or JPEG, up to 16 MiB)
	photo, err := typesafe.NewImageFromFile("kitchen.jpg")
	if err != nil {
		log.Fatal(err)
	}

	// Embed *Image directly into state; the SDK automatically serializes references and attaches assets
	resp, err := client.SystemOne(
		context.Background(),
		map[string]any{"photo": photo},
		typesafe.Questions{
			"has_cup": typesafe.NewNoul("Is there a coffee mug in `photo`?"),
			"cup_box": typesafe.NewBox("the coffee mug in `photo`"),
		},
		typesafe.WithModel(typesafe.ModelJevPreview),
	)
	if err != nil {
		log.Fatal(err)
	}

	if resp.Nouls()["has_cup"].Noul > 0.8 {
		box := resp.Boxes()["cup_box"].Box
		fmt.Printf("Mug found! Edges: Top=%.2f, Bottom=%.2f, Left=%.2f, Right=%.2f\n",
			box.Top, box.Bottom, box.Left, box.Right)
	}
}
```

---

## Custom Response Models (`SystemOneAs`)

Use `SystemOneAs[T]` to decode answers directly into your custom business struct:

```go
type TicketAnalysis struct {
	Model    string                  `json:"model"`
	Usage    typesafe.Usage          `json:"usage"`
	Billing  typesafe.NoulAnswer     `json:"billing"`
	Category typesafe.ChoiceAnswer   `json:"category"`
	Urgency  typesafe.ScoreAnswer    `json:"urgency"`
	Target   *typesafe.BoxAnswer     `json:"target,omitempty"`
}

result, err := typesafe.SystemOneAs[TicketAnalysis](ctx, client, state, questions)
if err != nil {
	log.Fatal(err)
}
fmt.Println(result.Category.Choice, result.Billing.Noul)
```

---

## Patterns & Model Guidance

TypeSafe's **Jev** model is calibrated for discrete decisions and qualitative judgments. Keep arithmetic calculation and counting in Go code:

### 1. Confidence-Gated Routing
Every `Choice` and `Score` answer includes a `Confidence` score between `0.0` and `1.0`:
- **High confidence ($\ge 0.85$)**: Act automatically without human intervention;
- **Medium confidence ($0.50 \sim 0.85$)**: Proceed with caution, flag for operator review;
- **Low confidence ($< 0.50$)**: Model is genuinely unsure ("I don't know"), route to human triage.

### 2. Counting & Speculative Fan-Out
Jev is not an arithmetic calculator and does not count items reliably. **Keep counting in Go**: fan out atomic `Noul` questions across items in a single `SystemOne` request, then sum the matching probabilities in code.

### 3. Score Rubric Bounds
- Do not use `score` as a continuous linear interpolator to reconstruct exact numerical quantities.
- Use `score` for threshold gating (e.g. `urgency.Score >= 1.5`).
- Score rubrics accept between **2 and 10 levels**. Choice questions accept up to **255 options**.

---

## 🤖 AI Coding Agent System Prompt

If you are using AI coding assistants (such as **Cursor**, **Claude Code**, **GitHub Copilot**, **Windsurf**, or **Antigravity**), add the following system instructions into `.cursorrules`, `CLAUDE.md`, or your agent prompt to ensure zero-hallucination, idiomatic integration:

````markdown
### TypeSafe AI Go SDK Coding Guidelines for Agent

You are working with the official TypeSafe AI Go SDK (`github.com/guchengod/typesafe-sdk-go`).
TypeSafe uses calibrated System One models (Jev) for deterministic classification, scoring, verification, and bounding-box object location. It does NOT generate unstructured free text.

#### 1. Core Principles
- Keep arithmetic, counting, string parsing, and business logic strictly in Go. Use Jev strictly for qualitative judgments.
- Always reuse a single `*typesafe.Client` (concurrency-safe, pools connections). Close it when done.
- Pass `context.Context` to all API calls.

#### 2. Initializing Client
```go
import typesafe "github.com/guchengod/typesafe-sdk-go"

client, err := typesafe.NewClient(
    // Optional configurations:
    // typesafe.WithAPIKey("ts_live_..."), // defaults to TYPESAFE_API_KEY env var
    // typesafe.WithTimeout(30 * time.Second),
    // typesafe.WithImages(true), // Danger zone: enables image & box questions
    // typesafe.WithLongContext(true), // Danger zone: enables 262k token window
    // typesafe.WithCompression(true), // Enabled by default for bodies >= 1 KiB
)
```

#### 3. Defining Questions
Use `typesafe.Questions` (map[string]typesafe.Question):
- **Yes/No Probability**: `typesafe.NewNoul("Question text", opts...)`
  Result: `response.Nouls()["key"].Noul` (float64, 0.0=No, 1.0=Yes)
- **Categorization**: `typesafe.NewChoice(map[string]any{"label": "description", "other": nil}, typesafe.WithInstructions("..."))`
  Max 255 options. Result: `response.Choices()["key"].Choice` (string), `.Confidence` (float64)
- **Rubric Rating**: `typesafe.NewScore([]any{"level0", "level1", "level2"}, typesafe.WithInstructions("..."))`
  Between 2 and 10 levels. Result: `response.Scores()["key"].Score` (float64, weighted expectation), `.Confidence` (float64)
- **Object Localization**: `typesafe.NewBox("the target object in `photo`")`
  Requires `typesafe.WithModel(typesafe.ModelJevPreview)` and `typesafe.WithImages(true)` or `typesafe.WithCallImages(true)`.
  Result: `response.Boxes()["key"].Box` (`Top`, `Bottom`, `Left`, `Right` in fractions 0..1), `.Confidence` (*float64)

#### 4. Multimodal Images
```go
img, err := typesafe.NewImageFromFile("path/to/image.jpg") // PNG or JPEG, max 16 MiB
// Embed img directly in state map or slice. The SDK automatically serializes it to a reference string and populates the assets payload.
resp, err := client.SystemOne(ctx, map[string]any{"photo": img}, questions,
    typesafe.WithModel(typesafe.ModelJevPreview),
    typesafe.WithCallImages(true),
)
```

#### 5. Confidence-Gated Decision Routing
Always inspect `Confidence` on Choice/Score:
- `Confidence >= 0.85`: High certainty, trigger automated action.
- `0.50 <= Confidence < 0.85`: Ambiguous, flag for human review.
- `Confidence < 0.50`: Uncertain, route to human triage fallback.

#### 6. Strongly-Typed Decoding (`SystemOneAs`)
Decode straight into a custom struct:
```go
type MyResult struct {
    Billing  typesafe.NoulAnswer   `json:"billing"`
    Category typesafe.ChoiceAnswer `json:"category"`
    Urgency  typesafe.ScoreAnswer  `json:"urgency"`
}
res, err := typesafe.SystemOneAs[MyResult](ctx, client, state, questions)
```

#### 7. Error Handling
SDK errors are strongly typed:
```go
if apiErr, ok := typesafe.AsRateLimitError(err); ok {
    wait, _ := apiErr.RetryAfter()
    time.Sleep(wait)
} else if ptlErr, ok := typesafe.AsPayloadTooLargeError(err); ok {
    // 413 Payload Too Large (>32 MiB)
} else if apiErr, ok := typesafe.AsAPIError(err); ok {
    log.Printf("API error %d: %s (req_id=%s)", apiErr.Status, apiErr.Message, apiErr.RequestID)
}
```
````

---

## Command Line Interface (CLI)

The repository ships a pure standard library CLI `cmd/typesafe`:

```bash
go install github.com/guchengod/typesafe-sdk-go/cmd/typesafe@latest
```

Examples:

```bash
export TYPESAFE_API_KEY="ts_live_..."

typesafe models --format text

typesafe ask \
  --state '{"body": "I was charged twice on invoice #4912."}' \
  --noul 'billing=Is this about billing?' \
  --choice 'queue=billing:Charges,technical:API issues,other' \
  --score 'urgency=routine,elevated,critical' \
  --box 'target=the coffee mug in photo' \
  --format text
```

Pipe JSON output into `jq` in shell pipelines:

```bash
curl -s https://example.test/ticket.json | typesafe ask --state - --questions questions.json | jq '.answers'
```

---

## Error Handling

The SDK provides a complete taxonomy of typed errors:

```go
resp, err := client.SystemOne(ctx, state, questions)
if err != nil {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		log.Println("Call timed out")
	default:
		if rateLimitErr, ok := typesafe.AsRateLimitError(err); ok {
			wait, _ := rateLimitErr.RetryAfter()
			log.Printf("Rate limited; retry after: %v", wait)
		} else if ptlErr, ok := typesafe.AsPayloadTooLargeError(err); ok {
			log.Printf("Payload too large (>32 MiB): %v", ptlErr)
		} else if apiErr, ok := typesafe.AsAPIError(err); ok {
			log.Printf("API error: status=%d, request_id=%s", apiErr.Status, apiErr.RequestID)
		}
	}
}
```

---

## Documentation & Examples

- [**Wiki Documentation**](https://github.com/guchengod/typesafe-sdk-go/wiki) — detailed guides for configuration, concurrency, logging, and error taxonomy.
- [**API Reference (pkg.go.dev)**](https://pkg.go.dev/github.com/guchengod/typesafe-sdk-go) — complete package reference.
- [`examples/quickstart`](examples/quickstart/main.go) — basic `SystemOne` usage.
- [`examples/advanced`](examples/advanced/main.go) — `SystemOneAs` with custom struct, per-call options, and typed error handling.

---

## Contributing

Issues and pull requests are welcome at [github.com/guchengod/typesafe-sdk-go](https://github.com/guchengod/typesafe-sdk-go).

```bash
go build ./...
go vet ./...
go test ./...                        # unit tests
go test -run TestIntegration ./...   # integration tests (requires TYPESAFE_API_KEY)
go test -bench . -run XXX ./...      # benchmarks
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.
