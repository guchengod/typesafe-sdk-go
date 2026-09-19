// Command advanced exercises the parts of the TypeSafe AI Go SDK a production caller reaches for:
// per-call overrides, a custom retry policy, a typed response model, a second API resource, and
// the typed error taxonomy.
//
//	export TYPESAFE_API_KEY="ts_live_..."
//	go run ./examples/advanced
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

// TicketClassification is a custom response model. The envelope fields are declared as on
// typesafe.SystemOneResponse; the answer fields are named after the questions and are lifted out
// of "answers" onto the document by SystemOneAs.
type TicketClassification struct {
	// Model and Usage are required: they are neither pointers nor tagged omitempty, so the SDK
	// reports an APIResponseValidationError naming the field when the response omits them.
	Model string         `json:"model"`
	Usage typesafe.Usage `json:"usage"`

	// Tone and Urgency are answer fields, also required.
	Tone    typesafe.ChoiceAnswer `json:"tone"`
	Urgency typesafe.ScoreAnswer  `json:"urgency"`

	// Billing is optional because a pointer can represent "not reported".
	Billing *typesafe.NoulAnswer `json:"billing"`

	// Summary is optional because the tag marks it so.
	Summary string `json:"summary,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := os.Getenv(typesafe.APIKeyEnv)
	if apiKey == "" {
		// A missing key is a configuration mistake, not a crash: explain how to fix it.
		fmt.Printf(`%s is not set.

Set it to your TypeSafe API key and run the example again:

    export %s="ts_live_..."
    go run ./examples/advanced

`, typesafe.APIKeyEnv, typesafe.APIKeyEnv)
		return nil
	}

	// The SDK redacts credentials from the records it logs. Wrapping the handler extends that to
	// any credential-bearing attribute routed through this logger.
	logger := slog.New(typesafe.NewRedactingHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	client, err := typesafe.NewClient(
		typesafe.WithAPIKey(apiKey),
		typesafe.WithTimeout(10*time.Second),
		typesafe.WithLogger(logger),
		typesafe.WithHeader("X-Environment", "production"),
	)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A per-call policy overrides the client's default without mutating it.
	callPolicy := typesafe.DefaultRetryPolicy()
	callPolicy.MaxRetries = 3
	callPolicy.BackoffInitial = 250 * time.Millisecond
	callPolicy.BackoffMax = 2 * time.Second
	callPolicy.Timeout = new(15 * time.Second)

	fmt.Println("available models:")
	models, err := client.Models.List(ctx, typesafe.WithCallHeader("X-Example", "advanced"))
	if err != nil {
		reportError(err)
	} else {
		fmt.Printf("  request id: %s\n", models.RawRequestID())
		for _, model := range models.Models {
			fmt.Printf("  %-14s %-12s %s\n", model.Name, model.ReleaseDate, model.Description)
		}
	}

	state := map[string]any{
		"sender":  "vip-customer@example.com",
		"subject": "Production cluster returning 502s",
		"body":    "Our production cluster has returned 502 errors since 10am. This is blocking checkout. Please investigate urgently.",
	}

	questions := typesafe.Questions{
		"billing": typesafe.NewNoul(
			"Is this message about invoicing or payment?",
			typesafe.WithNoulCriteria(typesafe.NoulCriteria{
				True:  "Charges, refunds, invoices, or payment problems",
				False: "Anything else",
			}),
		),
		"tone": typesafe.NewChoice(
			map[string]any{
				"calm":    "Measured and factual",
				"anxious": "Concerned about impact",
				"enraged": "Hostile or abusive",
			},
			typesafe.WithInstructions("What is the emotional state of the sender?"),
		),
		"urgency": typesafe.NewScore(
			[]any{"routine", "elevated", "critical emergency"},
			typesafe.WithInstructions("Rate the severity of this issue"),
		),
	}

	// SystemOneAs decodes the payload straight into TicketClassification, lifting the declared
	// answer fields onto the document.
	//
	// WithExtraBody is the escape hatch for request-body fields this SDK does not model yet: it is
	// shallow-merged over the body last, so a key that collides with "state", "model", or
	// "questions" replaces the SDK's value. Here it overrides the model WithModel set, which is
	// why the request asks for jev-latest even though WithModel named jev-preview.
	ticket, err := typesafe.SystemOneAs[TicketClassification](
		ctx,
		client,
		state,
		questions,
		typesafe.WithModel("jev-preview"),
		typesafe.WithCallTimeout(15*time.Second),
		typesafe.WithCallRetry(callPolicy),
		typesafe.WithExtraBody(map[string]any{"model": "jev-latest"}),
	)
	if err != nil {
		reportError(err)
		return nil
	}

	fmt.Println("\nticket classification:")
	fmt.Printf("  model:      %s\n", ticket.Model)
	if input, output := ticket.Usage.InputTokens, ticket.Usage.OutputTokens; input != nil && output != nil {
		fmt.Printf("  tokens:     %d in / %d out\n", *input, *output)
	}
	fmt.Printf("  tone:       %s (confidence %.2f)\n", ticket.Tone.Choice, ticket.Tone.Confidence)
	fmt.Printf("  urgency:    %.2f of %d (confidence %.2f)\n", ticket.Urgency.Score, len(ticket.Urgency.Legend)-1, ticket.Urgency.Confidence)
	if ticket.Billing != nil {
		fmt.Printf("  billing:    p(true)=%.4f\n", ticket.Billing.Noul)
	} else {
		fmt.Println("  billing:    not reported")
	}
	if ticket.Summary != "" {
		fmt.Printf("  summary:    %s\n", ticket.Summary)
	}

	return nil
}

// reportError prints the most specific description the SDK exposes for err. Every typed failure
// unwraps to the errors its more general siblings cover, so the checks run most specific first.
func reportError(err error) {
	var rateLimit *typesafe.RateLimitError
	var validation *typesafe.APIResponseValidationError
	var apiErr *typesafe.APIError
	var timeout *typesafe.APITimeoutError

	switch {
	case errors.As(err, &rateLimit):
		if wait, ok := rateLimit.RetryAfter(); ok {
			fmt.Printf("  rate limited: retry after %v (request_id=%s)\n", wait, rateLimit.RequestID)
		} else {
			fmt.Printf("  rate limited: no Retry-After header (request_id=%s)\n", rateLimit.RequestID)
		}
	case errors.As(err, &validation):
		fmt.Printf("  invalid response field %q: %v\n", validation.FieldPath, validation)
	case errors.As(err, &apiErr):
		fmt.Printf("  http %d: %v\n", apiErr.Status, apiErr)
	case errors.As(err, &timeout):
		fmt.Printf("  timed out after %v\n", timeout.Timeout)
	default:
		fmt.Printf("  %v\n", err)
	}
}
