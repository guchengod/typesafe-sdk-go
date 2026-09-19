// Command quickstart is the smallest useful TypeSafe AI Go SDK program: one System One request
// that answers a noul, a choice, and a score question about a structured state.
//
//	export TYPESAFE_API_KEY="ts_live_..."
//	go run ./examples/quickstart
package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"time"

	typesafe "github.com/guchengod/typesafe-sdk-go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	apiKey := os.Getenv(typesafe.APIKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("no API key: set %s to your TypeSafe API key", typesafe.APIKeyEnv)
	}

	client, err := typesafe.NewClient(typesafe.WithAPIKey(apiKey))
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The state is any JSON-encodable value. A map keeps a structured ticket together; a plain
	// string would be sent as-is.
	state := map[string]any{
		"channel":  "email",
		"customer": map[string]any{"plan": "pro", "tenure_months": 14},
		"document": "I was charged twice on my credit card for invoice #4912. Please refund immediately.",
	}

	// The three primitives, keyed by the names the answers come back under.
	questions := typesafe.Questions{
		"is_billing": typesafe.NewNoul(
			"Is this message about billing or payments?",
			typesafe.WithNoulCriteria(typesafe.NoulCriteria{
				True:  "Charges, refunds, invoices, or payment problems",
				False: "Anything else",
			}),
		),
		"category": typesafe.NewChoice(
			map[string]any{
				"billing":   "Inquiries about charges, refunds, or invoices",
				"technical": "Software bugs, crashes, or API problems",
				"other":     "Anything else",
			},
			typesafe.WithInstructions("What department should handle this ticket?"),
		),
		"urgency": typesafe.NewScore(
			[]any{
				"Can wait until next week",
				"Needs attention within 48 hours",
				"Requires immediate same-day attention",
			},
			typesafe.WithInstructions("How urgently does this customer require a response?"),
		),
	}

	response, err := client.SystemOne(ctx, state, questions)
	if err != nil {
		return fmt.Errorf("system one: %w", err)
	}

	fmt.Printf("model: %s\n", response.Model)
	if input, output := response.Usage.InputTokens, response.Usage.OutputTokens; input != nil && output != nil {
		fmt.Printf("tokens: %d in / %d out\n", *input, *output)
	}
	fmt.Printf("request id: %s\n", response.RawRequestID())

	// Answers are grouped by kind and keyed by the question name used in the request. Every
	// answer also stays reachable through response.Answers as an typesafe.Answer.
	for _, name := range slices.Sorted(maps.Keys(response.Nouls())) {
		answer := response.Nouls()[name]
		fmt.Printf("noul   %-10s p(true)=%.4f\n", name, answer.Noul)
	}
	for _, name := range slices.Sorted(maps.Keys(response.Choices())) {
		answer := response.Choices()[name]
		fmt.Printf("choice %-10s %s (confidence %.2f, p=%v)\n", name, answer.Choice, answer.Confidence, answer.Probabilities)
	}
	for _, name := range slices.Sorted(maps.Keys(response.Scores())) {
		answer := response.Scores()[name]
		fmt.Printf("score  %-10s %.2f/%.1f (confidence %.2f)\n",
			name, answer.Score, float64(len(answer.Legend)-1), answer.Confidence)
	}

	// The generic view carries the same concrete answers, for code that does not know the
	// question kind ahead of time.
	for _, name := range slices.Sorted(maps.Keys(response.Answers)) {
		fmt.Printf("answer %-10s type=%s\n", name, response.Answers[name].AnswerType())
	}

	return nil
}
