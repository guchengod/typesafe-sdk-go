package typesafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------------------------

// responseTestJSONClient returns a client that answers every request with body, carrying
// requestID as the x-typesafe-request-id header when it is not empty.
func responseTestJSONClient(t *testing.T, body, requestID string, opts ...Option) *Client {
	t.Helper()
	client, _ := newMockClient(t, func(*http.Request, int) *http.Response {
		if requestID == "" {
			return JSONResponse(http.StatusOK, body)
		}
		return JSONResponseWithHeaders(http.StatusOK, body, map[string]string{HeaderRequestID: requestID})
	}, opts...)
	return client
}

// responseTestDecode decodes a JSON body into a generic value for comparison.
func responseTestDecode(t *testing.T, body string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("test body is not valid JSON: %v (body=%q)", err, body)
	}
	return decoded
}

// responseTestAssertValidationError checks every observable part of a validation failure: the
// reported field path, status, request ID, decoded and raw bodies, and the exact message.
func responseTestAssertValidationError(t *testing.T, err error, endpoint, path, requestID, body string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want a validation error at %q", path)
	}
	validationErr, ok := errors.AsType[*APIResponseValidationError](err)
	if !ok {
		t.Fatalf("error = %T (%v), want *APIResponseValidationError", err, err)
	}
	if validationErr.FieldPath != path {
		t.Errorf("FieldPath = %q, want %q", validationErr.FieldPath, path)
	}
	if validationErr.Status != http.StatusOK {
		t.Errorf("Status = %d, want %d", validationErr.Status, http.StatusOK)
	}
	if validationErr.RequestID != requestID {
		t.Errorf("RequestID = %q, want %q", validationErr.RequestID, requestID)
	}
	if string(validationErr.RawBody) != body {
		t.Errorf("RawBody = %q, want %q", validationErr.RawBody, body)
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		decoded = body
	}
	if !reflect.DeepEqual(validationErr.Body, decoded) {
		t.Errorf("Body = %#v, want %#v", validationErr.Body, decoded)
	}

	want := fmt.Sprintf("%s: 200 Invalid response data at '%s'.", endpoint, path)
	if requestID != "" {
		want += " (request_id=" + requestID + ")"
	}
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	if marker := fmt.Sprintf("Invalid response data at '%s'.", path); !strings.Contains(err.Error(), marker) {
		t.Errorf("Error() = %q, want it to contain %q", err.Error(), marker)
	}
}

// ---------------------------------------------------------------------------------------------
// SystemOneResponse
// ---------------------------------------------------------------------------------------------

func TestSystemOneResponseHappyPath(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "req-42")

	response, err := client.SystemOne(t.Context(), "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}

	if response.Model != "jev-latest" {
		t.Errorf("Model = %q, want %q", response.Model, "jev-latest")
	}
	if response.Usage.InputTokens != 120 {
		t.Errorf("Usage.InputTokens = %v, want 120", response.Usage.InputTokens)
	}
	if response.Usage.OutputTokens != 12 {
		t.Errorf("Usage.OutputTokens = %v, want 12", response.Usage.OutputTokens)
	}
	if len(response.Answers) != 3 {
		t.Fatalf("len(Answers) = %d, want 3", len(response.Answers))
	}

	billing, ok := response.Answers["billing"].(*NoulAnswer)
	if !ok {
		t.Fatalf("Answers[billing] = %T, want *NoulAnswer", response.Answers["billing"])
	}
	if billing.Type != "noul" || billing.AnswerType() != "noul" || billing.Noul != 0.98 {
		t.Errorf("billing = %+v, want noul 0.98", billing)
	}

	tone, ok := response.Answers["tone"].(*ChoiceAnswer)
	if !ok {
		t.Fatalf("Answers[tone] = %T, want *ChoiceAnswer", response.Answers["tone"])
	}
	if tone.AnswerType() != "choice" || tone.Choice != "angry" || tone.Confidence != 0.9 {
		t.Errorf("tone = %+v, want choice angry at 0.9", tone)
	}
	if !reflect.DeepEqual(tone.Probabilities, map[string]float64{"calm": 0.1, "angry": 0.9}) {
		t.Errorf("tone.Probabilities = %#v", tone.Probabilities)
	}

	urgency, ok := response.Answers["urgency"].(*ScoreAnswer)
	if !ok {
		t.Fatalf("Answers[urgency] = %T, want *ScoreAnswer", response.Answers["urgency"])
	}
	if urgency.AnswerType() != "score" || urgency.Score != 1.7 || urgency.Confidence != 0.8 {
		t.Errorf("urgency = %+v, want score 1.7 at 0.8", urgency)
	}
	if !reflect.DeepEqual(urgency.Legend, map[string]any{"0": "can wait", "1": "this week", "2": "today"}) {
		t.Errorf("urgency.Legend = %#v", urgency.Legend)
	}
	if !reflect.DeepEqual(urgency.Probabilities, map[int]float64{0: 0.1, 1: 0.1, 2: 0.8}) {
		t.Errorf("urgency.Probabilities = %#v", urgency.Probabilities)
	}

	// The grouped views partition the answers by kind and share the decoded values.
	nouls, choices, scores := response.Nouls(), response.Choices(), response.Scores()
	if len(nouls) != 1 || len(choices) != 1 || len(scores) != 1 {
		t.Fatalf("group sizes = (%d, %d, %d), want (1, 1, 1)", len(nouls), len(choices), len(scores))
	}
	if nouls["billing"] != billing {
		t.Error("Nouls()[billing] is not the same value as Answers[billing]")
	}
	if choices["tone"] != tone {
		t.Error("Choices()[tone] is not the same value as Answers[tone]")
	}
	if scores["urgency"] != urgency {
		t.Error("Scores()[urgency] is not the same value as Answers[urgency]")
	}

	requestID, err := response.RequestID()
	if err != nil || requestID != "req-42" {
		t.Errorf("RequestID() = (%q, %v), want (%q, nil)", requestID, err, "req-42")
	}
	if response.RawRequestID() != "req-42" {
		t.Errorf("RawRequestID() = %q, want %q", response.RawRequestID(), "req-42")
	}

	raw, err := response.RawHTTPResponse()
	if err != nil {
		t.Fatalf("RawHTTPResponse() error = %v", err)
	}
	if raw.StatusCode != http.StatusOK {
		t.Errorf("RawHTTPResponse().StatusCode = %d, want %d", raw.StatusCode, http.StatusOK)
	}
	if got := raw.Header.Get(HeaderRequestID); got != "req-42" {
		t.Errorf("RawHTTPResponse() request ID header = %q, want %q", got, "req-42")
	}
	if got := raw.Header.Get("Content-Type"); got != ContentTypeJSON {
		t.Errorf("RawHTTPResponse() Content-Type = %q, want %q", got, ContentTypeJSON)
	}

	if got := string(response.RawBody()); got != fixedAnswerResponse {
		t.Errorf("RawBody() = %q, want the payload byte-for-byte", got)
	}
}

// TestSystemOneResponseRequiresAnswers checks that a success response answers every question it was
// asked: the answers object may not be absent, null, empty, or short an entry.
func TestSystemOneResponseRequiresAnswers(t *testing.T) {
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	cases := []struct {
		name string
		body string
		path string
	}{
		{
			name: "absent",
			body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1}}`,
			path: "answers",
		},
		{
			name: "null",
			body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":null}`,
			path: "answers",
		},
		{
			name: "empty object",
			body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":{}}`,
			path: "answers.billing",
		},
		{
			name: "one question unanswered",
			body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":{"billing":{"type":"noul","noul":0.98}}}`,
			path: "answers.tone",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := responseTestJSONClient(t, testCase.body, "req-123")

			_, err := client.SystemOne(t.Context(), "state", fixedQuestions())
			responseTestAssertValidationError(t, err, endpoint, testCase.path, "req-123", testCase.body)
		})
	}
}

// TestSystemOneResponseRequiresTokenCounts pins that both usage counts are required, as the official
// SDK's types declare them.
func TestSystemOneResponseRequiresTokenCounts(t *testing.T) {
	t.Parallel()
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	cases := []struct {
		name string
		body string
		path string
	}{
		{
			name: "absent input count",
			body: `{"model":"test","usage":{"output_tokens":1},"answers":` + fixedAnswersJSON + `}`,
			path: "usage.input_tokens",
		},
		{
			name: "null output count",
			body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":null},"answers":` + fixedAnswersJSON + `}`,
			path: "usage.output_tokens",
		},
		{
			name: "fractional input count",
			body: `{"model":"test","usage":{"input_tokens":1.5,"output_tokens":1},"answers":` + fixedAnswersJSON + `}`,
			path: "usage.input_tokens",
		},
		{
			name: "usage is not an object",
			body: `{"model":"test","usage":[],"answers":` + fixedAnswersJSON + `}`,
			path: "usage",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := responseTestJSONClient(t, testCase.body, "req-123")

			_, err := client.SystemOne(t.Context(), "state", fixedQuestions())
			responseTestAssertValidationError(t, err, endpoint, testCase.path, "req-123", testCase.body)
		})
	}
}

// responseTestSystemOneCase is one malformed System One payload plus the field path the SDK must
// report. A case sets either body (a full payload) or answer (a single answers entry injected into
// a valid envelope).
type responseTestSystemOneCase struct {
	name   string
	answer string
	body   string
	path   string
}

func TestSystemOneResponseMalformedPayloads(t *testing.T) {
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	cases := []responseTestSystemOneCase{
		{name: "missing model", body: `{"usage":{"input_tokens":1,"output_tokens":1},"answers":{}}`, path: "model"},
		{name: "non-string model", body: `{"model":1,"usage":{"input_tokens":1,"output_tokens":1},"answers":{}}`, path: "model"},
		{name: "missing usage", body: `{"model":"test","answers":{}}`, path: "usage"},
		{name: "non-object usage", body: `{"model":"test","usage":"many","answers":{}}`, path: "usage"},
		{name: "answers not an object", body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":[]}`, path: "answers"},
		{name: "answers is a string", body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":"nope"}`, path: "answers"},
		{name: "null payload", body: `null`, path: ""},
		{name: "null answers", body: `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":null}`, path: "answers"},
		{name: "null usage", body: `{"model":"test","usage":null,"answers":{}}`, path: "usage"},
		{name: "null model", body: `{"model":null,"usage":{"input_tokens":1,"output_tokens":1},"answers":{}}`, path: "model"},

		{name: "answer not an object", answer: `"n":"not-a-mapping"`, path: "answers.n.type"},
		{name: "answer is an array", answer: `"n":[{"type":"noul","noul":0.5}]`, path: "answers.n.type"},
		{name: "answer is a number", answer: `"n":5`, path: "answers.n.type"},
		{name: "answer is null", answer: `"n":null`, path: "answers.n.type"},
		{name: "answer missing type", answer: `"n":{"noul":0.5}`, path: "answers.n.type"},
		{name: "answer empty type", answer: `"n":{"type":"","noul":0.5}`, path: "answers.n.type"},
		{name: "answer non-string type", answer: `"n":{"type":5,"noul":0.5}`, path: "answers.n.type"},

		{name: "noul missing noul", answer: `"n":{"type":"noul"}`, path: "answers.n.noul"},
		{name: "noul non-numeric noul", answer: `"n":{"type":"noul","noul":"0.5"}`, path: "answers.n.noul"},
		{name: "noul object noul", answer: `"n":{"type":"noul","noul":{"value":0.5}}`, path: "answers.n.noul"},

		{name: "choice missing confidence", answer: `"c":{"type":"choice","choice":"a","probabilities":{}}`, path: "answers.c.confidence"},
		{name: "choice non-numeric confidence", answer: `"c":{"type":"choice","choice":"a","confidence":"high","probabilities":{}}`, path: "answers.c.confidence"},
		{name: "choice missing choice", answer: `"c":{"type":"choice","confidence":0.5,"probabilities":{}}`, path: "answers.c.choice"},
		{name: "choice non-string choice", answer: `"c":{"type":"choice","choice":1,"confidence":0.5,"probabilities":{}}`, path: "answers.c.choice"},
		{name: "choice missing probabilities", answer: `"c":{"type":"choice","choice":"a","confidence":0.5}`, path: "answers.c.probabilities"},
		{name: "choice probabilities not an object", answer: `"c":{"type":"choice","choice":"a","confidence":0.5,"probabilities":[]}`, path: "answers.c.probabilities"},
		{name: "choice probability not a number", answer: `"c":{"type":"choice","choice":"a","confidence":0.5,"probabilities":{"billing":"high"}}`, path: "answers.c.probabilities.billing"},

		{name: "score missing score", answer: `"s":{"type":"score","confidence":1.0,"legend":{},"probabilities":{}}`, path: "answers.s.score"},
		{name: "score non-numeric score", answer: `"s":{"type":"score","score":"high","confidence":1.0,"legend":{},"probabilities":{}}`, path: "answers.s.score"},
		{name: "score missing confidence", answer: `"s":{"type":"score","score":1.0,"legend":{},"probabilities":{}}`, path: "answers.s.confidence"},
		{name: "score missing legend", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"probabilities":{}}`, path: "answers.s.legend"},
		{name: "score legend not an object", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"legend":[],"probabilities":{}}`, path: "answers.s.legend"},
		{name: "score missing probabilities", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"legend":{}}`, path: "answers.s.probabilities"},
		{name: "score probabilities not an object", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"legend":{},"probabilities":[]}`, path: "answers.s.probabilities"},
		{name: "score probabilities key not an integer", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"legend":{},"probabilities":{"x":0.5}}`, path: "answers.s.probabilities.x"},
		{name: "score probability not a number", answer: `"s":{"type":"score","score":1.0,"confidence":1.0,"legend":{},"probabilities":{"0":"high"}}`, path: "answers.s.probabilities.0"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := testCase.body
			if body == "" {
				body = fmt.Sprintf(`{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":{%s}}`, testCase.answer)
			}
			client := responseTestJSONClient(t, body, "req-123")

			_, err := client.SystemOne(t.Context(), "state", questionsCovering(t, body))
			responseTestAssertValidationError(t, err, endpoint, testCase.path, "req-123", body)
		})
	}
}

// questionsCovering returns one question per answer id in body, so a malformed payload reaches the
// field under test instead of failing the check that every question was answered.
func questionsCovering(t *testing.T, body string) Questions {
	t.Helper()
	var document struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	// An answers value that is not an object yields no ids; the placeholder question below still
	// reaches the field under test, because the SDK rejects the whole answers value first.
	_ = json.Unmarshal([]byte(body), &document)
	questions := make(Questions, len(document.Answers))
	for name := range document.Answers {
		questions[name] = NewNoul("Placeholder?")
	}
	if len(questions) == 0 {
		questions["q"] = NewNoul("Placeholder?")
	}
	return questions
}

// TestScoreAnswerLegendKeepsTheAPIsKeys checks the legend is decoded as the reference types it — an
// object keyed by whatever the API sends — rather than demanding integer keys.
func TestScoreAnswerLegendKeepsTheAPIsKeys(t *testing.T) {
	const body = `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":{"urgency":{"type":"score","score":1.0,"confidence":0.9,"legend":{"0":"can wait","1":"today","note":"levels are illustrative"},"probabilities":{"0":0.5,"1":0.5}}}}`
	client := responseTestJSONClient(t, body, "")

	response, err := client.SystemOne(t.Context(), "state", Questions{"urgency": NewScore([]any{"can wait", "today"})})
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	urgency := response.Scores()["urgency"]
	if urgency == nil {
		t.Fatal("Scores()[urgency] = nil, want a score answer")
	}
	want := map[string]any{"0": "can wait", "1": "today", "note": "levels are illustrative"}
	if !reflect.DeepEqual(urgency.Legend, want) {
		t.Errorf("Legend = %#v, want %#v", urgency.Legend, want)
	}
	if legend, present := urgency.LegendFor(1); !present || legend != "today" {
		t.Errorf("LegendFor(1) = %#v, %v, want %q, true", legend, present, "today")
	}
	if !reflect.DeepEqual(urgency.Probabilities, map[int]float64{0: 0.5, 1: 0.5}) {
		t.Errorf("Probabilities = %#v, want %#v", urgency.Probabilities, map[int]float64{0: 0.5, 1: 0.5})
	}
}

func TestSystemOneResponseBodyNotAnObject(t *testing.T) {
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	cases := []struct {
		name string
		body string
	}{
		{"array", `[1, 2, 3]`},
		{"string", `"hello"`},
		{"number", `42`},
		{"boolean", `true`},
		{"invalid json", `{"model":`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := responseTestJSONClient(t, testCase.body, "req-123")

			_, err := client.SystemOne(t.Context(), "state", fixedQuestions())
			responseTestAssertValidationError(t, err, endpoint, "", "req-123", testCase.body)
		})
	}
}

// TestSystemOneResponseRejectsNullEnvelopeFields checks that an explicit null is rejected for
// every required envelope field. Decoding null into a zero value
// would turn a malformed response into a plausible-looking one.
func TestSystemOneResponseRejectsNullEnvelopeFields(t *testing.T) {
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	for _, testCase := range []struct {
		name string
		body string
		path string
	}{
		{"model", `{"model":null,"usage":{"input_tokens":1,"output_tokens":1},"answers":{}}`, "model"},
		{"usage", `{"model":"test","usage":null,"answers":{}}`, "usage"},
		{"answers", `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":null}`, "answers"},
		{"whole payload", `null`, ""},
		{"blank payload", ` `, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := responseTestJSONClient(t, testCase.body, "req-null")

			_, err := client.SystemOne(t.Context(), "state", fixedQuestions())
			responseTestAssertValidationError(t, err, endpoint, testCase.path, "req-null", testCase.body)
		})
	}
}

func TestSystemOneResponseDropsUnknownAnswerType(t *testing.T) {
	const body = `{
		"model": "test",
		"usage": {"input_tokens": 1, "output_tokens": 1},
		"answers": {
			"spam": {"type": "noul", "noul": 0.9},
			"mystery": {"type": "aurora", "value": 3}
		}
	}`
	logger, logs := capturedLogs()
	client := responseTestJSONClient(t, body, "req-9", WithLogger(logger))

	response, err := client.SystemOne(t.Context(), "state", Questions{
		"spam":    NewNoul("Is this spam?"),
		"mystery": RawQuestion{"type": "aurora", "value": 3},
	})
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}

	if _, ok := response.Answers["mystery"]; ok {
		t.Error("Answers contains the unrecognized answer type")
	}
	if len(response.Answers) != 1 {
		t.Errorf("len(Answers) = %d, want 1", len(response.Answers))
	}
	if _, ok := response.Nouls()["mystery"]; ok {
		t.Error("Nouls() contains the unrecognized answer type")
	}
	if len(response.Choices()) != 0 || len(response.Scores()) != 0 {
		t.Errorf("Choices/Scores = (%d, %d) entries, want none", len(response.Choices()), len(response.Scores()))
	}
	if answer := response.Nouls()["spam"]; answer == nil || answer.Noul != 0.9 {
		t.Errorf("Nouls()[spam] = %+v, want noul 0.9", answer)
	}

	// The unknown answer survives in the raw payload even though the typed view dropped it.
	if got := string(response.RawBody()); got != body {
		t.Errorf("RawBody() = %q, want the payload byte-for-byte", got)
	}
	var raw struct {
		Answers map[string]struct {
			Type string `json:"type"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(response.RawBody(), &raw); err != nil {
		t.Fatalf("RawBody() is not decodable: %v", err)
	}
	if got := raw.Answers["mystery"].Type; got != "aurora" {
		t.Errorf("raw answers[mystery].type = %q, want %q", got, "aurora")
	}

	logged := logs.String()
	if !strings.Contains(logged, "unrecognized type") {
		t.Errorf("logs = %q, want a warning about an unrecognized answer type", logged)
	}
	if !strings.Contains(logged, "mystery") {
		t.Errorf("logs = %q, want the offending answer name", logged)
	}
}

func TestSystemOneResponseToleratesUnknownFields(t *testing.T) {
	const body = `{
		"model": "test",
		"usage": {"input_tokens": 1, "output_tokens": 1, "reasoning_tokens": 9, "billing_units": 1},
		"answers": {"spam": {"type": "noul", "noul": 0.9, "explanation": "spammy"}},
		"future_field": {"nested": true}
	}`
	client := responseTestJSONClient(t, body, "")

	response, err := client.SystemOne(t.Context(), "state", Questions{"spam": NewNoul("Is this spam?")})
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	if answer := response.Nouls()["spam"]; answer == nil || answer.Noul != 0.9 {
		t.Errorf("Nouls()[spam] = %+v, want noul 0.9", answer)
	}
	if response.Usage.InputTokens != 1 || response.Usage.OutputTokens != 1 {
		t.Errorf("Usage = %+v, want 1 input and 1 output token", response.Usage)
	}
	encoded, err := json.Marshal(response.Usage)
	if err != nil {
		t.Fatalf("json.Marshal(Usage) error = %v", err)
	}
	if got, want := string(encoded), `{"input_tokens":1,"output_tokens":1}`; got != want {
		t.Errorf("Usage JSON = %s, want %s", got, want)
	}
	if got := string(response.RawBody()); got != body {
		t.Errorf("RawBody() = %q, want the payload byte-for-byte", got)
	}
}

func TestSystemOneResponsePreservesNestedLegendJSON(t *testing.T) {
	const body = `{
		"model": "test",
		"usage": {"input_tokens": 1, "output_tokens": 1},
		"answers": {
			"quality": {
				"type": "score",
				"score": 0.0,
				"confidence": 1.0,
				"legend": {"0": {"examples": ["a", {"note": null}]}},
				"probabilities": {"0": 1.0}
			}
		}
	}`
	client := responseTestJSONClient(t, body, "")

	response, err := client.SystemOne(t.Context(), "state", Questions{"quality": NewScore([]any{"low", "high"})})
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	answer := response.Scores()["quality"]
	if answer == nil {
		t.Fatal("Scores()[quality] = nil, want a score answer")
	}
	want := map[string]any{"0": map[string]any{"examples": []any{"a", map[string]any{"note": nil}}}}
	if !reflect.DeepEqual(answer.Legend, want) {
		t.Errorf("Legend = %#v, want %#v", answer.Legend, want)
	}
	if !reflect.DeepEqual(answer.Probabilities, map[int]float64{0: 1.0}) {
		t.Errorf("Probabilities = %#v, want %#v", answer.Probabilities, map[int]float64{0: 1.0})
	}

	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("json.Marshal(ScoreAnswer) error = %v", err)
	}
	var restored ScoreAnswer
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("round-tripped score answer is not decodable: %v", err)
	}
	if !reflect.DeepEqual(restored.Legend, want) {
		t.Errorf("round-tripped Legend = %#v, want %#v", restored.Legend, want)
	}
}

func TestSystemOneResponseMissingRequestID(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "")

	response, err := client.SystemOne(t.Context(), "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	if response.RawRequestID() != "" {
		t.Errorf("RawRequestID() = %q, want the empty string", response.RawRequestID())
	}
	if _, err := response.RequestID(); err == nil {
		t.Error("RequestID() error = nil, want an error")
	} else if got := err.Error(); !strings.Contains(got, "request ID") {
		t.Errorf("RequestID() error = %q, want it to mention the request ID", got)
	}
}

func TestSystemOneResponseAccessorsWithoutHTTPMetadata(t *testing.T) {
	response := &SystemOneResponse{Model: "test", Usage: Usage{}, Answers: map[string]Answer{}}

	if got := response.String(); got != "SystemOneResponse{model: test, answers: 0}" {
		t.Errorf("String() = %q", got)
	}
	if response.RawRequestID() != "" {
		t.Errorf("RawRequestID() = %q, want the empty string", response.RawRequestID())
	}
	if response.RawBody() != nil {
		t.Errorf("RawBody() = %q, want nil", response.RawBody())
	}
	if _, err := response.RequestID(); err == nil {
		t.Error("RequestID() error = nil, want an error")
	}
	if _, err := response.RawHTTPResponse(); err == nil {
		t.Error("RawHTTPResponse() error = nil, want an error")
	} else if got := err.Error(); !strings.Contains(got, "raw HTTP response") {
		t.Errorf("RawHTTPResponse() error = %q, want it to mention the raw HTTP response", got)
	}
}

func TestSystemOneResponseString(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "req-42")

	response, err := client.SystemOne(t.Context(), "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	if got, want := response.String(), "SystemOneResponse{model: jev-latest, answers: 3}"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestSystemOneResponseMarshalJSONEncodesOnlyThePayload(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "req-export")

	response, err := client.SystemOne(t.Context(), "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal(SystemOneResponse) error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("marshaled response is not a JSON object: %v", err)
	}
	for _, key := range []string{"model", "usage", "answers"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("marshaled response is missing %q", key)
		}
	}
	for _, key := range []string{"nouls", "choices", "scores", "requestID", "rawResponse", "rawBody"} {
		if _, ok := payload[key]; ok {
			t.Errorf("marshaled response leaked %q", key)
		}
	}
	if len(payload) != 3 {
		t.Errorf("marshaled response keys = %v, want exactly model, usage, answers", payload)
	}
	if want := responseTestDecode(t, fixedAnswerResponse); !reflect.DeepEqual(payload, want) {
		t.Errorf("marshaled payload = %#v, want %#v", payload, want)
	}

	billing := response.Answers["billing"].(*NoulAnswer)
	tone := response.Answers["tone"].(*ChoiceAnswer)
	urgency := response.Answers["urgency"].(*ScoreAnswer)

	// Round-tripping the payload yields the same typed field values.
	var restored struct {
		Model   string                     `json:"model"`
		Usage   Usage                      `json:"usage"`
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("marshaled response is not decodable: %v", err)
	}
	if restored.Model != response.Model || !reflect.DeepEqual(restored.Usage, response.Usage) {
		t.Errorf("round-tripped envelope = %+v, want model %q and %+v", restored, response.Model, response.Usage)
	}
	var restoredBilling NoulAnswer
	if err := json.Unmarshal(restored.Answers["billing"], &restoredBilling); err != nil {
		t.Fatalf("round-tripped billing answer is not decodable: %v", err)
	}
	if !reflect.DeepEqual(&restoredBilling, billing) {
		t.Errorf("round-tripped billing = %+v, want %+v", &restoredBilling, billing)
	}
	var restoredTone ChoiceAnswer
	if err := json.Unmarshal(restored.Answers["tone"], &restoredTone); err != nil {
		t.Fatalf("round-tripped tone answer is not decodable: %v", err)
	}
	if !reflect.DeepEqual(&restoredTone, tone) {
		t.Errorf("round-tripped tone = %+v, want %+v", &restoredTone, tone)
	}
	var restoredUrgency ScoreAnswer
	if err := json.Unmarshal(restored.Answers["urgency"], &restoredUrgency); err != nil {
		t.Fatalf("round-tripped urgency answer is not decodable: %v", err)
	}
	if !reflect.DeepEqual(&restoredUrgency, urgency) {
		t.Errorf("round-tripped urgency = %+v, want %+v", &restoredUrgency, urgency)
	}
}

func TestScoreAnswerMarshalJSONEncodesLevelsAsObjectKeys(t *testing.T) {
	answer := &ScoreAnswer{
		Type:          "score",
		Score:         1.7,
		Confidence:    0.8,
		Legend:        map[string]any{"0": "can wait", "1": "this week", "2": "today"},
		Probabilities: map[int]float64{0: 0.1, 1: 0.1, 2: 0.8},
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("json.Marshal(ScoreAnswer) error = %v", err)
	}
	want := map[string]any{
		"type":          "score",
		"score":         1.7,
		"confidence":    0.8,
		"legend":        map[string]any{"0": "can wait", "1": "this week", "2": "today"},
		"probabilities": map[string]any{"0": 0.1, "1": 0.1, "2": 0.8},
	}
	if got := responseTestDecode(t, string(encoded)); !reflect.DeepEqual(got, want) {
		t.Errorf("marshaled score answer = %#v, want %#v", got, want)
	}

	var restored ScoreAnswer
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("json.Unmarshal(ScoreAnswer) error = %v", err)
	}
	if !reflect.DeepEqual(&restored, answer) {
		t.Errorf("round-tripped score answer = %+v, want %+v", &restored, answer)
	}

	// A score answer without rubric entries keeps the keys, encoded as null.
	empty, err := json.Marshal(&ScoreAnswer{Type: "score"})
	if err != nil {
		t.Fatalf("json.Marshal(empty ScoreAnswer) error = %v", err)
	}
	wantEmpty := map[string]any{
		"type":          "score",
		"score":         float64(0),
		"confidence":    float64(0),
		"legend":        nil,
		"probabilities": nil,
	}
	if got := responseTestDecode(t, string(empty)); !reflect.DeepEqual(got, wantEmpty) {
		t.Errorf("marshaled empty score answer = %#v, want %#v", got, wantEmpty)
	}
}

// ---------------------------------------------------------------------------------------------
// ListModelsResponse
// ---------------------------------------------------------------------------------------------

const responseTestModelsBody = `{"models":[` +
	`{"name":"jev-latest","description":"Fast model","release_date":"2026-08-01"},` +
	`{"name":"jev-mini","description":"Small model","release_date":"2026-07-15"}]}`

func TestListModelsResponseHappyPath(t *testing.T) {
	client := responseTestJSONClient(t, responseTestModelsBody, "req-7")

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	want := []ModelMetadata{
		{Name: "jev-latest", Description: "Fast model", ReleaseDate: "2026-08-01"},
		{Name: "jev-mini", Description: "Small model", ReleaseDate: "2026-07-15"},
	}
	if !reflect.DeepEqual(response.Models, want) {
		t.Errorf("Models = %+v, want %+v", response.Models, want)
	}

	requestID, err := response.RequestID()
	if err != nil || requestID != "req-7" {
		t.Errorf("RequestID() = (%q, %v), want (%q, nil)", requestID, err, "req-7")
	}
	if response.RawRequestID() != "req-7" {
		t.Errorf("RawRequestID() = %q, want %q", response.RawRequestID(), "req-7")
	}
	raw, err := response.RawHTTPResponse()
	if err != nil {
		t.Fatalf("RawHTTPResponse() error = %v", err)
	}
	if raw.StatusCode != http.StatusOK {
		t.Errorf("RawHTTPResponse().StatusCode = %d, want %d", raw.StatusCode, http.StatusOK)
	}
	if got := raw.Header.Get(HeaderRequestID); got != "req-7" {
		t.Errorf("RawHTTPResponse() request ID header = %q, want %q", got, "req-7")
	}
	if got := string(response.RawBody()); got != responseTestModelsBody {
		t.Errorf("RawBody() = %q, want the payload byte-for-byte", got)
	}
}

func TestListModelsResponseMalformedPayloads(t *testing.T) {
	const (
		endpoint = "GET https://api.typesafe.ai/v1/models"
		full     = `{"name":"test","description":"Test model","release_date":"2026-09-14"}`
	)
	without := func(missing string) string {
		var fields map[string]any
		if err := json.Unmarshal([]byte(full), &fields); err != nil {
			t.Fatalf("test fixture is not decodable: %v", err)
		}
		delete(fields, missing)
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("test fixture is not encodable: %v", err)
		}
		return string(encoded)
	}

	cases := []struct {
		name string
		body string
		path string
	}{
		{name: "empty object", body: `{}`, path: "models"},
		{name: "null payload", body: `null`, path: ""},
		{name: "body not an object", body: `[]`, path: ""},
		{name: "invalid json", body: `{"models":`, path: ""},
		{name: "models is an object", body: `{"models":{"name":"test"}}`, path: "models"},
		{name: "models is a string", body: `{"models":"test"}`, path: "models"},
		{name: "models is a number", body: `{"models":1}`, path: "models"},
		{name: "item not an object", body: fmt.Sprintf(`{"models":[%s,"not-a-model"]}`, full), path: "models[1]"},
		{name: "item is null", body: fmt.Sprintf(`{"models":[%s,null]}`, full), path: "models[1]"},
		{name: "item missing name", body: fmt.Sprintf(`{"models":[%s,%s]}`, full, without("name")), path: "models[1].name"},
		{name: "item missing description", body: fmt.Sprintf(`{"models":[%s,%s]}`, full, without("description")), path: "models[1].description"},
		{name: "item missing release date", body: fmt.Sprintf(`{"models":[%s,%s]}`, full, without("release_date")), path: "models[1].release_date"},
		{name: "item non-string name", body: fmt.Sprintf(`{"models":[%s,{"name":1,"description":"Test model","release_date":"2026-09-14"}]}`, full), path: "models[1].name"},
		{name: "item non-string release date", body: fmt.Sprintf(`{"models":[%s,{"name":"test","description":"Test model","release_date":20260914}]}`, full), path: "models[1].release_date"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := responseTestJSONClient(t, testCase.body, "req-123")

			_, err := client.Models.List(t.Context())
			responseTestAssertValidationError(t, err, endpoint, testCase.path, "req-123", testCase.body)
		})
	}
}

func TestListModelsResponseMarshalJSONEncodesOnlyThePayload(t *testing.T) {
	client := responseTestJSONClient(t, responseTestModelsBody, "req-export")

	response, err := client.Models.List(t.Context())
	if err != nil {
		t.Fatalf("Models.List() error = %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal(ListModelsResponse) error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("marshaled response is not a JSON object: %v", err)
	}
	if len(payload) != 1 {
		t.Errorf("marshaled response keys = %v, want exactly models", payload)
	}
	if want := responseTestDecode(t, responseTestModelsBody); !reflect.DeepEqual(payload, want) {
		t.Errorf("marshaled payload = %#v, want %#v", payload, want)
	}

	var restored ListModelsResponse
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("json.Unmarshal(ListModelsResponse) error = %v", err)
	}
	if !reflect.DeepEqual(restored.Models, response.Models) {
		t.Errorf("round-tripped models = %+v, want %+v", restored.Models, response.Models)
	}
	reencoded, err := json.Marshal(&restored)
	if err != nil {
		t.Fatalf("json.Marshal(restored) error = %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Errorf("re-encoded response = %s, want %s", reencoded, encoded)
	}
}

func TestListModelsResponseAccessorsWithoutHTTPMetadata(t *testing.T) {
	response := &ListModelsResponse{}

	if response.RawRequestID() != "" {
		t.Errorf("RawRequestID() = %q, want the empty string", response.RawRequestID())
	}
	if response.RawBody() != nil {
		t.Errorf("RawBody() = %q, want nil", response.RawBody())
	}
	if _, err := response.RequestID(); err == nil {
		t.Error("RequestID() error = nil, want an error")
	}
	if _, err := response.RawHTTPResponse(); err == nil {
		t.Error("RawHTTPResponse() error = nil, want an error")
	}
}

// ---------------------------------------------------------------------------------------------
// SystemOneAs
// ---------------------------------------------------------------------------------------------

// responseTestEnvelope declares the envelope fields plus one field per lifted answer.
type responseTestEnvelope struct {
	Model   string       `json:"model"`
	Usage   Usage        `json:"usage"`
	Billing NoulAnswer   `json:"billing"`
	Tone    ChoiceAnswer `json:"tone"`
	Urgency ScoreAnswer  `json:"urgency"`
}

// responseTestAnswersOnly declares only lifted answer fields.
type responseTestAnswersOnly struct {
	Billing NoulAnswer `json:"billing"`
}

// responseTestOptionals declares every shape that may be absent from the document.
type responseTestOptionals struct {
	Model string         `json:"model"`
	Usage Usage          `json:"usage"`
	Maybe *NoulAnswer    `json:"maybe"`
	Items []string       `json:"items"`
	Meta  map[string]any `json:"meta"`
	Extra any            `json:"extra"`
	Spare string         `json:"spare,omitempty"`
}

// responseTestRequired declares a required field whose absence must be reported, alongside fields
// that are allowed to be absent.
type responseTestRequired struct {
	Model    string         `json:"model"`
	Maybe    *NoulAnswer    `json:"maybe"`
	Items    []string       `json:"items"`
	Meta     map[string]any `json:"meta"`
	Extra    any            `json:"extra"`
	Spare    string         `json:"spare,omitempty"`
	Required string         `json:"required"`
}

// responseTestBillingText declares an answer field with the wrong type.
type responseTestBillingText struct {
	Billing string `json:"billing"`
}

// responseTestBillingNoul declares an answer field whose nested field has the wrong type.
type responseTestBillingNoul struct {
	Billing struct {
		Noul string `json:"noul"`
	} `json:"billing"`
}

// responseTestUnknownAnswers declares a field for an answer type this SDK version does not model.
type responseTestUnknownAnswers struct {
	Spam    NoulAnswer                 `json:"spam"`
	Mystery json.RawMessage            `json:"mystery"`
	Answers map[string]json.RawMessage `json:"answers"`
}

func TestSystemOneAsLiftsAnswersOntoCustomStruct(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "req-as")

	result, err := SystemOneAs[responseTestEnvelope](t.Context(), client, "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}

	if result.Model != "jev-latest" {
		t.Errorf("Model = %q, want %q", result.Model, "jev-latest")
	}
	if result.Usage.InputTokens != 120 {
		t.Errorf("Usage.InputTokens = %v, want 120", result.Usage.InputTokens)
	}
	if result.Billing.Type != "noul" || result.Billing.Noul != 0.98 {
		t.Errorf("Billing = %+v, want noul 0.98", result.Billing)
	}
	if result.Tone.Type != "choice" || result.Tone.Choice != "angry" || result.Tone.Confidence != 0.9 {
		t.Errorf("Tone = %+v, want choice angry at 0.9", result.Tone)
	}
	if !reflect.DeepEqual(result.Tone.Probabilities, map[string]float64{"calm": 0.1, "angry": 0.9}) {
		t.Errorf("Tone.Probabilities = %#v", result.Tone.Probabilities)
	}
	if result.Urgency.Type != "score" || result.Urgency.Score != 1.7 || result.Urgency.Confidence != 0.8 {
		t.Errorf("Urgency = %+v, want score 1.7 at 0.8", result.Urgency)
	}
	if legend, present := result.Urgency.LegendFor(2); !present || legend != "today" {
		t.Errorf("Urgency.LegendFor(2) = %#v, want %q", legend, "today")
	}
	if result.Urgency.Probabilities[2] != 0.8 {
		t.Errorf("Urgency.Probabilities[2] = %v, want 0.8", result.Urgency.Probabilities[2])
	}
}

func TestSystemOneAsAnswerOnlyStruct(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "")

	result, err := SystemOneAs[responseTestAnswersOnly](t.Context(), client, "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}
	if result.Billing.Noul != 0.98 {
		t.Errorf("Billing.Noul = %v, want 0.98", result.Billing.Noul)
	}
}

func TestSystemOneAsOptionalFieldsMayBeAbsent(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "")

	result, err := SystemOneAs[responseTestOptionals](t.Context(), client, "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}
	if result.Model != "jev-latest" {
		t.Errorf("Model = %q, want %q", result.Model, "jev-latest")
	}
	if result.Maybe != nil {
		t.Errorf("Maybe = %+v, want nil", result.Maybe)
	}
	if result.Items != nil {
		t.Errorf("Items = %#v, want nil", result.Items)
	}
	if result.Meta != nil {
		t.Errorf("Meta = %#v, want nil", result.Meta)
	}
	if result.Extra != nil {
		t.Errorf("Extra = %#v, want nil", result.Extra)
	}
	if result.Spare != "" {
		t.Errorf("Spare = %q, want the empty string", result.Spare)
	}
}

func TestSystemOneAsMissingRequiredField(t *testing.T) {
	const body = fixedAnswerResponse
	client := responseTestJSONClient(t, body, "req-123")

	_, err := SystemOneAs[responseTestRequired](t.Context(), client, "state", fixedQuestions())
	responseTestAssertValidationError(t, err, "POST https://api.typesafe.ai/v1/systemone", "required", "req-123", body)

	t.Run("embedded anonymous struct missing required field", func(t *testing.T) {
		type embeddedStruct struct {
			responseTestRequired
		}
		_, err := SystemOneAs[embeddedStruct](t.Context(), client, "state", fixedQuestions())
		responseTestAssertValidationError(t, err, "POST https://api.typesafe.ai/v1/systemone", "required", "req-123", body)
	})

	t.Run("null value for non-nullable required field", func(t *testing.T) {
		const nullBody = `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":` + fixedAnswersJSON + `,"required":null}`
		nullClient := responseTestJSONClient(t, nullBody, "req-null")
		_, err := SystemOneAs[responseTestRequired](t.Context(), nullClient, "state", fixedQuestions())
		responseTestAssertValidationError(t, err, "POST https://api.typesafe.ai/v1/systemone", "required", "req-null", nullBody)
	})
}

func TestSystemOneAsCustomFieldTypeMismatch(t *testing.T) {
	const endpoint = "POST https://api.typesafe.ai/v1/systemone"
	client := responseTestJSONClient(t, fixedAnswerResponse, "req-123")

	t.Run("top-level answer field", func(t *testing.T) {
		_, err := SystemOneAs[responseTestBillingText](t.Context(), client, "state", fixedQuestions())
		responseTestAssertValidationError(t, err, endpoint, "billing", "req-123", fixedAnswerResponse)
	})
	t.Run("nested answer field", func(t *testing.T) {
		_, err := SystemOneAs[responseTestBillingNoul](t.Context(), client, "state", fixedQuestions())
		responseTestAssertValidationError(t, err, endpoint, "billing.noul", "req-123", fixedAnswerResponse)
	})
}

func TestSystemOneAsExcludesUnknownAnswerTypes(t *testing.T) {
	const body = `{
		"model": "test",
		"usage": {"input_tokens": 1, "output_tokens": 1},
		"answers": {
			"spam": {"type": "noul", "noul": 0.98},
			"mystery": {"type": "aurora", "value": 1}
		}
	}`
	client := responseTestJSONClient(t, body, "")

	result, err := SystemOneAs[responseTestUnknownAnswers](t.Context(), client, "state", Questions{
		"spam":    NewNoul("Is this spam?"),
		"mystery": RawQuestion{"type": "aurora", "value": 1},
	})
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}
	if result.Spam.Noul != 0.98 {
		t.Errorf("Spam.Noul = %v, want 0.98", result.Spam.Noul)
	}
	if result.Mystery != nil {
		t.Errorf("Mystery = %q, want nil for an unrecognized answer type", result.Mystery)
	}
	if len(result.Answers) != 1 {
		t.Fatalf("Answers = %#v, want exactly the recognized answer", result.Answers)
	}
	if _, ok := result.Answers["spam"]; !ok {
		t.Error("Answers is missing the recognized answer")
	}
}

func TestSystemOneAsPreservesAPIErrors(t *testing.T) {
	const body = `{"detail":"Invalid request"}`
	client, _ := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponseWithHeaders(http.StatusBadRequest, body, map[string]string{HeaderRequestID: "req-error"})
	})

	_, err := SystemOneAs[responseTestEnvelope](t.Context(), client, "state", fixedQuestions())
	if err == nil {
		t.Fatal("SystemOneAs() error = nil, want an API error")
	}
	badRequest, ok := errors.AsType[*BadRequestError](err)
	if !ok {
		t.Fatalf("error = %T (%v), want *BadRequestError", err, err)
	}
	if badRequest.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", badRequest.Status, http.StatusBadRequest)
	}
	if want := map[string]any{"detail": "Invalid request"}; !reflect.DeepEqual(badRequest.Body, want) {
		t.Errorf("Body = %#v, want %#v", badRequest.Body, want)
	}
	if badRequest.RequestID != "req-error" {
		t.Errorf("RequestID = %q, want %q", badRequest.RequestID, "req-error")
	}
}

func TestSystemOneAsMapDocument(t *testing.T) {
	client := responseTestJSONClient(t, fixedAnswerResponse, "")

	document, err := SystemOneAs[map[string]any](t.Context(), client, "state", fixedQuestions())
	if err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}
	decoded := *document
	if decoded["model"] != "jev-latest" {
		t.Errorf("document[model] = %#v, want %q", decoded["model"], "jev-latest")
	}
	if usage, ok := decoded["usage"].(map[string]any); !ok || usage["input_tokens"] != float64(120) {
		t.Errorf("document[usage] = %#v", decoded["usage"])
	}
	answers, ok := decoded["answers"].(map[string]any)
	if !ok {
		t.Fatalf("document[answers] = %#v, want an object", decoded["answers"])
	}
	if len(answers) != 3 {
		t.Errorf("len(document[answers]) = %d, want 3", len(answers))
	}
	if billing, ok := answers["billing"].(map[string]any); !ok || billing["noul"] != 0.98 {
		t.Errorf("document[answers][billing] = %#v", answers["billing"])
	}
}

func TestSystemOneAsSendsSameRequestAsSystemOne(t *testing.T) {
	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, fixedAnswerResponse)
	})

	if _, err := client.SystemOne(t.Context(), "state", fixedQuestions()); err != nil {
		t.Fatalf("SystemOne() error = %v", err)
	}
	if _, err := SystemOneAs[responseTestEnvelope](t.Context(), client, "state", fixedQuestions()); err != nil {
		t.Fatalf("SystemOneAs() error = %v", err)
	}

	calls := transport.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(calls))
	}
	first, second := calls[0], calls[1]
	if first.Method != second.Method {
		t.Errorf("methods = (%q, %q), want both %q", first.Method, second.Method, first.Method)
	}
	if first.URL != second.URL {
		t.Errorf("URLs = (%q, %q), want the same", first.URL, second.URL)
	}
	if !bytes.Equal(first.Body, second.Body) {
		t.Errorf("bodies = (%s, %s), want the same", first.Body, second.Body)
	}
	if !reflect.DeepEqual(first.Header, second.Header) {
		t.Errorf("headers = (%v, %v), want the same", first.Header, second.Header)
	}
}

// TestAnswersDecodesByDiscriminator checks the Answers map type a caller-defined response model
// uses, which decodes each entry from its "type" field.
func TestAnswersDecodesByDiscriminator(t *testing.T) {
	var decoded Answers
	body := `{"billing":{"type":"noul","noul":0.98},"tone":{"type":"choice","choice":"angry","confidence":0.9,"probabilities":{"angry":0.9,"calm":0.1}},"urgency":{"type":"score","score":1.7,"confidence":0.8,"legend":{"0":"can wait"},"probabilities":{"0":1}}}`
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded %d answers, want 3", len(decoded))
	}
	if noul, ok := decoded["billing"].(*NoulAnswer); !ok || noul.Noul != 0.98 {
		t.Errorf("billing = %#v, want a noul answer of 0.98", decoded["billing"])
	}
	if choice, ok := decoded["tone"].(*ChoiceAnswer); !ok || choice.Choice != "angry" {
		t.Errorf("tone = %#v, want a choice answer of angry", decoded["tone"])
	}
	if score, ok := decoded["urgency"].(*ScoreAnswer); !ok {
		t.Errorf("urgency = %#v, want a score answer with a legend", decoded["urgency"])
	} else if legend, present := score.LegendFor(0); !present || legend != "can wait" {
		t.Errorf("urgency legend[0] = %#v, want %q", legend, "can wait")
	}
}

// TestAnswersRejectsMalformedInput checks that a bad entry is reported with its path instead of
// silently decoding to a zero-valued answer.
func TestAnswersRejectsMalformedInput(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want string
	}{
		{"not an object", `[]`, "answers"},
		{"unknown type", `{"q":{"type":"aurora","noul":0.5}}`, "answers.q.type"},
		{"missing type", `{"q":{"noul":0.5}}`, "answers.q.type"},
		{"missing field", `{"q":{"type":"noul"}}`, "answers.q.noul"},
		{"null answer", `{"q":null}`, "answers.q.type"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var decoded Answers
			err := json.Unmarshal([]byte(testCase.body), &decoded)
			if err == nil {
				t.Fatalf("Unmarshal(%s) = %#v, want an error", testCase.body, decoded)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// TestAnswersCarriesResponseValidationPath checks that a malformed answers object reached through
// SystemOneAs reports the offending answer field, not an empty path.
func TestAnswersCarriesResponseValidationPath(t *testing.T) {
	type custom struct {
		Model   string  `json:"model"`
		Usage   Usage   `json:"usage"`
		Answers Answers `json:"answers"`
	}
	// The payload's answers are validated before the custom model sees them, so this asserts the
	// path reported for a problem the envelope itself detects.
	body := `{"model":"test","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":{"type":"noul"}}}`
	client := responseTestJSONClient(t, body, "req-answers")

	_, err := SystemOneAs[custom](t.Context(), client, "state", Questions{"q": NewNoul("Placeholder?")})
	responseTestAssertValidationError(t, err, "POST https://api.typesafe.ai/v1/systemone", "answers.q.noul", "req-answers", body)
}
