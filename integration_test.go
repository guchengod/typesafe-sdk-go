package typesafe

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveAPIKey returns the API key for integration tests, loading .env from the module root when
// TYPESAFE_API_KEY is not already set. Tests that need it call t.Skip when no key is available.
func liveAPIKey(t *testing.T) string {
	t.Helper()
	if os.Getenv("TYPESAFE_RUN_INTEGRATION") != "1" {
		t.Skip("skipping live integration test: set TYPESAFE_RUN_INTEGRATION=1 to run")
	}
	if key := strings.TrimSpace(os.Getenv(APIKeyEnv)); key != "" {
		return key
	}
	file, err := os.Open(filepath.Join(".", ".env"))
	if err != nil {
		t.Skipf("skipping integration test: %s is not set", APIKeyEnv)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(name) == APIKeyEnv {
			if key := strings.Trim(strings.TrimSpace(value), `"'`); key != "" {
				return key
			}
		}
	}
	t.Skipf("skipping integration test: %s is not set", APIKeyEnv)
	return ""
}

// liveClient builds a client against the real API.
func liveClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(
		WithAPIKey(liveAPIKey(t)),
		WithTimeout(120*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestIntegrationModels lists the models available to the account.
func TestIntegrationModels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := liveClient(t)

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	if len(response.Models) == 0 {
		t.Fatal("Models.List() returned no models")
	}
	for _, model := range response.Models {
		if model.Name == "" || model.Description == "" || model.ReleaseDate == "" {
			t.Errorf("model %+v has an empty field", model)
		}
	}
	if _, err := response.RequestID(); err != nil {
		t.Logf("response carried no request id: %v", err)
	}
}

// TestIntegrationSystemOne exercises all three primitives against the live API and checks the
// answer shapes the API contract guarantees.
func TestIntegrationSystemOne(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := liveClient(t)

	response, err := client.SystemOne(
		t.Context(),
		map[string]any{
			"subject": "Charged twice this month",
			"body":    "I see two charges of $49. I only have one account. Please fix this ASAP.",
		},
		Questions{
			"billing": NewNoul(
				"",
				WithInstructions("Is this ticket about billing?"),
				WithNoulCriteria(NoulCriteria{
					True: map[string]any{"meaning": "Payments or invoices", "examples": []any{"charged twice"}},
				}),
			),
			"tone": NewChoice(
				map[string]any{"calm": nil, "frustrated": nil, "angry": nil},
				WithInstructions("What is the customer's tone?"),
			),
			"urgency": NewScore(
				[]any{"can wait", "this week", "today"},
				WithInstructions("How urgent is this ticket?"),
			),
		},
	)
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}

	if response.Model == "" {
		t.Error("response.Model is empty")
	}
	if response.Usage.InputTokens != nil && *response.Usage.InputTokens <= 0 {
		t.Errorf("Usage.InputTokens = %d, want > 0", *response.Usage.InputTokens)
	}

	billing, ok := response.Nouls()["billing"]
	if !ok {
		t.Fatal("no noul answer named billing")
	}
	if billing.Noul < 0 || billing.Noul > 1 {
		t.Errorf("billing.Noul = %v, want within [0,1]", billing.Noul)
	}

	tone, ok := response.Choices()["tone"]
	if !ok {
		t.Fatal("no choice answer named tone")
	}
	if !contains([]string{"calm", "frustrated", "angry"}, tone.Choice) {
		t.Errorf("tone.Choice = %q, want one of calm/frustrated/angry", tone.Choice)
	}
	if total := sumValues(tone.Probabilities); total < 0.9 || total > 1.1 {
		t.Errorf("tone probabilities sum to %v, want approximately 1", total)
	}

	urgency, ok := response.Scores()["urgency"]
	if !ok {
		t.Fatal("no score answer named urgency")
	}
	if urgency.Score < 0 || urgency.Score > 2 {
		t.Errorf("urgency.Score = %v, want within [0,2]", urgency.Score)
	}
	for level := range 3 {
		legend, present := urgency.LegendFor(level)
		if !present {
			t.Errorf("urgency legend is missing level %d", level)
			continue
		}
		if want := []string{"can wait", "this week", "today"}[level]; legend != want {
			t.Errorf("urgency.LegendFor(%d) = %v, want %q", level, legend, want)
		}
		if _, present := urgency.Probabilities[level]; !present {
			t.Errorf("urgency.Probabilities is missing level %d", level)
		}
	}
	if total := sumValues(urgency.Probabilities); total < 0.9 || total > 1.1 {
		t.Errorf("urgency probabilities sum to %v, want approximately 1", total)
	}
	if len(response.RawBody()) == 0 {
		t.Error("RawBody() is empty")
	}
}

// ticketAnswers is a custom response model exercising the answer-lifting rule. The answer fields
// are lifted from "answers" by name; the map keeps every answer reachable as well.
type ticketAnswers struct {
	Model   string       `json:"model"`
	Usage   Usage        `json:"usage"`
	Billing NoulAnswer   `json:"billing"`
	Tone    ChoiceAnswer `json:"tone"`
	Urgency ScoreAnswer  `json:"urgency"`
	Answers Answers      `json:"answers"`
}

// TestIntegrationSystemOneAs decodes a live response into a caller-defined struct.
func TestIntegrationSystemOneAs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := liveClient(t)

	state := map[string]any{
		"subject": "Charged twice this month",
		"body":    "I see two charges of $49. I only have one account. Please fix this ASAP.",
	}
	questions := Questions{
		"billing": RawQuestion{"type": "noul", "instructions": "Is this ticket about billing?"},
		"tone": NewChoice(
			map[string]any{"calm": nil, "frustrated": nil, "angry": nil},
			WithInstructions("What is the customer's tone?"),
		),
		"urgency": NewScore(
			[]any{"can wait", "this week", "today"},
			WithInstructions("How urgent is this ticket?"),
		),
	}

	answers, err := SystemOneAs[ticketAnswers](t.Context(), client, state, questions)
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}
	if answers.Model == "" {
		t.Error("Model is empty")
	}
	if answers.Billing.Type != "noul" || answers.Billing.Noul < 0 || answers.Billing.Noul > 1 {
		t.Errorf("Billing = %+v, want a noul answer within [0,1]", answers.Billing)
	}
	if !contains([]string{"calm", "frustrated", "angry"}, answers.Tone.Choice) {
		t.Errorf("Tone = %+v, want a modeled choice", answers.Tone)
	}
	if answers.Urgency.Score < 0 || answers.Urgency.Score > 2 {
		t.Errorf("Urgency.Score = %v, want within [0,2]", answers.Urgency.Score)
	}
	if len(answers.Answers) != 3 {
		t.Errorf("Answers has %d entries, want 3", len(answers.Answers))
	}
}

// TestIntegrationUnauthorizedKey checks that the live API's rejection is mapped to the typed
// authentication error rather than a generic failure.
func TestIntegrationUnauthorizedKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	liveAPIKey(t) // skip when no key is configured, matching the other integration tests.

	client, err := NewClient(
		WithAPIKey("ts_invalid_key_for_authentication_test"),
		WithRetryPolicy(noRetryPolicy()),
		WithTimeout(60*time.Second),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Models.List(t.Context())
	if err == nil {
		t.Fatal("Models.List() with an invalid key succeeded")
	}
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("Models.List() error = %v (%T), want *AuthenticationError", err, err)
	}
	if authErr.Status != 401 {
		t.Errorf("Status = %d, want 401", authErr.Status)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatal("AsAPIError() did not match the authentication error")
	}
	if apiErr.Endpoint == "" {
		t.Error("APIError.Endpoint is empty")
	}
}

// TestIntegrationContextCancellation checks that a cancelled context aborts a live call promptly.
func TestIntegrationContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := liveClient(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	started := time.Now()
	_, err := client.SystemOne(ctx, "text", Questions{"q": NewNoul("Is this a question?")})
	if err == nil {
		t.Fatal("SystemOne() with a cancelled context succeeded")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("cancelled call took %v, want a prompt return", elapsed)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sumValues[K comparable](values map[K]float64) float64 {
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total
}
