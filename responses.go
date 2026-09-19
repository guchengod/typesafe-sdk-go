package typesafe

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
)

// Usage reports the token counts for a request.
type Usage struct {
	// InputTokens is the number of billable input tokens, or nil when the API did not report it.
	InputTokens *int `json:"input_tokens,omitzero"`

	// OutputTokens is the number of output tokens, or nil when the API did not report it.
	OutputTokens *int `json:"output_tokens,omitzero"`
}

// String renders the token counts, showing an unreported count as "-".
func (u Usage) String() string {
	return "Usage{input_tokens: " + optionalInt(u.InputTokens) + ", output_tokens: " + optionalInt(u.OutputTokens) + "}"
}

func optionalInt(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

// Answer is a single answer to a single question. The concrete type is [NoulAnswer],
// [ChoiceAnswer], or [ScoreAnswer], and always matches the corresponding question's type.
type Answer interface {
	// AnswerType returns the wire discriminator: "noul", "choice", or "score".
	AnswerType() string
}

// NoulAnswer is a yes/no answer.
//
// See the noul primitive (https://docs.typesafe.ai/primitives/noul) for details.
type NoulAnswer struct {
	// Type is always "noul".
	Type string `json:"type"`

	// Noul is the probability of a yes answer or a true statement, from 0 to 1.
	Noul float64 `json:"noul"`
}

// AnswerType returns the wire discriminator.
func (a *NoulAnswer) AnswerType() string { return "noul" }

// ChoiceAnswer is a selected label with its alternatives.
//
// See the choice primitive (https://docs.typesafe.ai/primitives/choice) for details.
type ChoiceAnswer struct {
	// Type is always "choice".
	Type string `json:"type"`

	// Choice is the criteria key with the highest probability.
	Choice string `json:"choice"`

	// Confidence is the confidence in the selection, from 0 to 1.
	Confidence float64 `json:"confidence"`

	// Probabilities is the probability of each criteria key, from 0 to 1.
	Probabilities map[string]float64 `json:"probabilities"`
}

// AnswerType returns the wire discriminator.
func (a *ChoiceAnswer) AnswerType() string { return "choice" }

// ScoreAnswer is a rating against an ordered rubric.
//
// See the score primitive (https://docs.typesafe.ai/primitives/score) for details.
type ScoreAnswer struct {
	// Type is always "score".
	Type string `json:"type"`

	// Score is the probability-weighted average of the rubric levels; it may fall between levels.
	Score float64 `json:"score"`

	// Confidence is the confidence in the rating, from 0 to 1.
	Confidence float64 `json:"confidence"`

	// Legend maps each rubric level to the criteria description supplied in the request.
	Legend map[int]any `json:"legend"`

	// Probabilities is the probability of each rubric level, keyed as in Legend.
	Probabilities map[int]float64 `json:"probabilities"`
}

// AnswerType returns the wire discriminator.
func (a *ScoreAnswer) AnswerType() string { return "score" }

// MarshalJSON encodes the rubric maps with their integer levels as JSON object keys.
func (a *ScoreAnswer) MarshalJSON() ([]byte, error) {
	type scoreAnswer ScoreAnswer
	return json.Marshal(struct {
		*scoreAnswer
		Legend        map[string]any     `json:"legend"`
		Probabilities map[string]float64 `json:"probabilities"`
	}{
		scoreAnswer:   (*scoreAnswer)(a),
		Legend:        stringifyKeys(a.Legend),
		Probabilities: stringifyKeys(a.Probabilities),
	})
}

func stringifyKeys[V any](source map[int]V) map[string]V {
	if source == nil {
		return nil
	}
	target := make(map[string]V, len(source))
	for key, value := range source {
		target[strconv.Itoa(key)] = value
	}
	return target
}

// SystemOneResponse holds the answers to a System One request, grouped by question kind.
//
// See System One (https://docs.typesafe.ai/concepts/system-one) for details.
type SystemOneResponse struct {
	// Model is the name of the model that answered the questions; it may differ from the alias
	// supplied in the request.
	Model string `json:"model"`

	// Usage reports the token counts for this evaluation.
	Usage Usage `json:"usage"`

	// Answers holds every answer keyed by the question name supplied in the request. Answer types
	// this SDK version does not model are omitted here and remain reachable through RawBody.
	Answers map[string]Answer `json:"answers"`

	nouls   map[string]*NoulAnswer
	choices map[string]*ChoiceAnswer
	scores  map[string]*ScoreAnswer

	requestID   string
	rawResponse *http.Response
	rawBody     []byte
}

// Nouls returns the yes/no answers keyed by question name.
func (r *SystemOneResponse) Nouls() map[string]*NoulAnswer { return r.nouls }

// Choices returns the choice answers keyed by question name.
func (r *SystemOneResponse) Choices() map[string]*ChoiceAnswer { return r.choices }

// Scores returns the score answers keyed by question name.
func (r *SystemOneResponse) Scores() map[string]*ScoreAnswer { return r.scores }

// RequestID returns the x-typesafe-request-id response header. It reports an error when the
// response did not carry one.
func (r *SystemOneResponse) RequestID() (string, error) {
	if r.requestID == "" {
		return "", &SDKError{Message: "The response did not include a request ID."}
	}
	return r.requestID, nil
}

// RawRequestID returns the request ID, or the empty string when absent.
func (r *SystemOneResponse) RawRequestID() string { return r.requestID }

// RawHTTPResponse returns the underlying *http.Response.
func (r *SystemOneResponse) RawHTTPResponse() (*http.Response, error) {
	if r.rawResponse == nil {
		return nil, &SDKError{Message: "The response was not created from a raw HTTP response."}
	}
	return r.rawResponse, nil
}

// RawBody returns the response payload exactly as received, including any answer types this SDK
// version does not model.
func (r *SystemOneResponse) RawBody() []byte { return r.rawBody }

// MarshalJSON encodes only the API payload, leaving HTTP metadata out of the result.
func (r *SystemOneResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Model   string            `json:"model"`
		Usage   Usage             `json:"usage"`
		Answers map[string]Answer `json:"answers"`
	}{r.Model, r.Usage, r.Answers})
}

// ModelMetadata describes a model available to the account.
type ModelMetadata struct {
	// Name is the model name or alias accepted by a request's model field.
	Name string `json:"name"`

	// Description describes the model and its capabilities.
	Description string `json:"description"`

	// ReleaseDate is the model's release date as the API reports it. The published schema
	// describes a YYYY-MM-DD date; the live API currently returns a full timestamp.
	ReleaseDate string `json:"release_date"`
}

// ListModelsResponse holds the models available to the account.
type ListModelsResponse struct {
	// Models holds the available models.
	Models []ModelMetadata `json:"models"`

	requestID   string
	rawResponse *http.Response
	rawBody     []byte
}

// RequestID returns the x-typesafe-request-id response header. It reports an error when the
// response did not carry one.
func (r *ListModelsResponse) RequestID() (string, error) {
	if r.requestID == "" {
		return "", &SDKError{Message: "The response did not include a request ID."}
	}
	return r.requestID, nil
}

// RawRequestID returns the request ID, or the empty string when absent.
func (r *ListModelsResponse) RawRequestID() string { return r.requestID }

// RawHTTPResponse returns the underlying *http.Response.
func (r *ListModelsResponse) RawHTTPResponse() (*http.Response, error) {
	if r.rawResponse == nil {
		return nil, &SDKError{Message: "The response was not created from a raw HTTP response."}
	}
	return r.rawResponse, nil
}

// RawBody returns the response payload exactly as received.
func (r *ListModelsResponse) RawBody() []byte { return r.rawBody }

// MarshalJSON encodes only the API payload, leaving HTTP metadata out of the result.
func (r *ListModelsResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Models []ModelMetadata `json:"models"`
	}{r.Models})
}

// answerTypes are the answer discriminators this SDK version models. Answers of any other type
// are dropped from the typed view so a newer server cannot break an older client; they stay
// reachable through RawBody.
var answerTypes = map[string]bool{"noul": true, "choice": true, "score": true}

// Answers is a map of question names to answers, decoded by each entry's "type" discriminator.
//
// [SystemOneResponse.Answers] is a plain map because its entries are already decoded. Answers
// exists for caller-defined response models passed to [SystemOneAs], where Go needs a concrete
// type to decode into — a bare map[string][Answer] cannot be decoded, because an interface type
// carries no information about which concrete answer to build:
//
//	type Analysis struct {
//		Model   string         `json:"model"`
//		Usage   typesafe.Usage `json:"usage"`
//		Answers typesafe.Answers `json:"answers"`
//	}
type Answers map[string]Answer

// UnmarshalJSON decodes the answers object, dispatching each entry on its "type" field.
func (a *Answers) UnmarshalJSON(data []byte) error {
	entries, ok := objectEntries(data)
	if !ok {
		return &answerDecodeError{path: "answers", err: errors.New("answers must be a JSON object")}
	}
	decoded := make(Answers, len(entries))
	for name, raw := range entries {
		answer, err := parseAnswer(name, raw, &decoder{})
		if err != nil {
			return &answerDecodeError{path: "answers", err: err}
		}
		if answer == nil {
			return &answerDecodeError{path: answerPath(name, "type"), err: errors.New("unrecognized answer type")}
		}
		decoded[name] = answer
	}
	*a = decoded
	return nil
}

// answerDecodeError reports a malformed answer at a known path while decoding a custom model.
type answerDecodeError struct {
	path string
	err  error
}

func (e *answerDecodeError) Error() string { return e.path + ": " + e.err.Error() }

func (e *answerDecodeError) Unwrap() error { return e.err }

// envelope is the shape of a System One success payload: three fields whose names are part of the
// API contract, so they are modeled as a struct rather than a map of field names to values.
//
// Each field holds raw JSON because the SDK validates and decodes them itself, reporting the
// dotted path of whichever one is malformed. A field left nil was absent; the literal "null"
// decodes to a non-empty slice, which is how an explicit null is told apart from an absent field.
type envelope struct {
	Model   json.RawMessage `json:"model"`
	Usage   json.RawMessage `json:"usage"`
	Answers json.RawMessage `json:"answers"`
}

// decoder turns a response body into a typed response, reporting the dotted path of the first
// field that is missing or has the wrong shape.
type decoder struct {
	status   int
	header   http.Header
	endpoint string
	rawBody  []byte
	envelope envelope
}

func newDecoder(resp *response) *decoder {
	return &decoder{
		status:   resp.StatusCode,
		header:   resp.Header,
		endpoint: resp.Endpoint,
		rawBody:  resp.Body,
	}
}

// newValidationError builds the error the SDK raises for a success status whose body does not
// match the expected schema, naming the offending field.
func newValidationError(resp *response, path string) error {
	return &APIResponseValidationError{
		APIError: APIError{
			Status:    resp.StatusCode,
			Body:      decodeErrorBody(resp.Body),
			RawBody:   resp.Body,
			Headers:   resp.Header,
			Endpoint:  resp.Endpoint,
			RequestID: resp.RequestID,
		},
		FieldPath: path,
	}
}

// invalid builds the validation error the SDK raises for a malformed success response.
func (d *decoder) invalid(path string) error {
	return newValidationError(&response{
		StatusCode: d.status,
		Header:     d.header,
		Body:       d.rawBody,
		Endpoint:   d.endpoint,
		RequestID:  d.header.Get(HeaderRequestID),
	}, path)
}

// openInto decodes the response body into target. A body that is missing, null, or not a JSON
// object is reported with an empty field path, because no field is at fault.
func openInto(d *decoder, target any) error {
	if isJSONNull(d.rawBody) {
		return d.invalid("")
	}
	if err := json.Unmarshal(d.rawBody, target); err != nil {
		return d.invalid("")
	}
	return nil
}

// decodeModel decodes the required model name.
func (d *decoder) decodeModel(target *string) error {
	value, ok := decodeText(d.envelope.Model)
	if !ok {
		return d.invalid("model")
	}
	*target = value
	return nil
}

// decodeUsage decodes the required usage object, reporting the exact path of a malformed count.
// A count that is absent or null stays nil, which is how the API reports "not measured".
func (d *decoder) decodeUsage(target *Usage) error {
	fields, ok := objectEntries(d.envelope.Usage)
	if !ok {
		return d.invalid("usage")
	}
	for _, counter := range []struct {
		name   string
		target **int
	}{
		{"input_tokens", &target.InputTokens},
		{"output_tokens", &target.OutputTokens},
	} {
		value, present := fields[counter.name]
		if !present {
			continue
		}
		count, ok := decodeOptionalInt(value)
		if !ok {
			return d.invalid("usage." + counter.name)
		}
		*counter.target = count
	}
	return nil
}

// isJSONNull reports whether a raw JSON value is absent or null.
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// objectEntries decodes a JSON object into its members, rejecting null and non-object values.
func objectEntries(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if isJSONNull(raw) {
		return nil, false
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	return entries, true
}

// decodeOptionalInt decodes an optional whole number. Null and absence both mean "not reported";
// a fractional or out-of-range value is rejected.
func decodeOptionalInt(raw json.RawMessage) (*int, bool) {
	if isJSONNull(raw) {
		return nil, true
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return new(value), true
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil || math.Trunc(number) != number {
		return nil, false
	}
	if number > math.MaxInt64 || number < math.MinInt64 {
		return nil, false
	}
	return new(int(number)), true
}

// decodeNumber decodes a required JSON number, rejecting null and every non-numeric value.
func decodeNumber(raw json.RawMessage) (float64, bool) {
	if isJSONNull(raw) {
		return 0, false
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	return number, true
}

// decodeText decodes a required JSON string, rejecting null and every non-string value.
func decodeText(raw json.RawMessage) (string, bool) {
	if isJSONNull(raw) {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

// parseSystemOneResponse decodes a System One success response.
//
// The model and usage fields are required; answers is optional and defaults to empty. An answer
// whose type this SDK version does not model is skipped with a warning, leaving the full payload
// available through [SystemOneResponse.RawBody].
func parseSystemOneResponse(resp *response, logger *slog.Logger) (*SystemOneResponse, error) {
	d := newDecoder(resp)
	if err := openInto(d, &d.envelope); err != nil {
		return nil, err
	}

	result := &SystemOneResponse{
		requestID:   resp.RequestID,
		rawResponse: rawHTTPResponse(resp),
		rawBody:     resp.Body,
		Answers:     map[string]Answer{},
		nouls:       map[string]*NoulAnswer{},
		choices:     map[string]*ChoiceAnswer{},
		scores:      map[string]*ScoreAnswer{},
	}
	if err := d.decodeModel(&result.Model); err != nil {
		return nil, err
	}
	if err := d.decodeUsage(&result.Usage); err != nil {
		return nil, err
	}
	if raw := d.envelope.Answers; raw != nil {
		// Absent answers default to empty; an explicit null is malformed, because null is not a
		// JSON object and would otherwise decode to the same empty map.
		if isJSONNull(raw) {
			return nil, d.invalid("answers")
		}
		if err := parseAnswers(raw, result, d, logger); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// parseAnswers decodes the answers object into typed answers plus the grouped accessors.
func parseAnswers(raw json.RawMessage, result *SystemOneResponse, d *decoder, logger *slog.Logger) error {
	entries, ok := objectEntries(raw)
	if !ok {
		return d.invalid("answers")
	}
	for name, entry := range entries {
		answer, err := parseAnswer(name, entry, d)
		if err != nil {
			return err
		}
		switch typed := answer.(type) {
		case nil:
			// Forward compatibility: a type this SDK version does not model is dropped from the
			// typed view and stays reachable through RawBody.
			logger.Warn("Ignoring an answer with an unrecognized type", slog.String("name", name))
		case *NoulAnswer:
			result.Answers[name], result.nouls[name] = typed, typed
		case *ChoiceAnswer:
			result.Answers[name], result.choices[name] = typed, typed
		case *ScoreAnswer:
			result.Answers[name], result.scores[name] = typed, typed
		}
	}
	return nil
}

// parseAnswer decodes a single answer. An unrecognized discriminator yields a nil answer.
func parseAnswer(name string, raw json.RawMessage, d *decoder) (Answer, error) {
	fields, ok := objectEntries(raw)
	if !ok {
		return nil, d.invalid(answerPath(name, "type"))
	}
	answerType, ok := decodeText(fields["type"])
	if !ok || answerType == "" {
		return nil, d.invalid(answerPath(name, "type"))
	}
	if !answerTypes[answerType] {
		return nil, nil
	}

	number := func(field string) (float64, error) {
		value, ok := decodeNumber(fields[field])
		if !ok {
			return 0, d.invalid(answerPath(name, field))
		}
		return value, nil
	}
	text := func(field string) (string, error) {
		value, ok := decodeText(fields[field])
		if !ok {
			return "", d.invalid(answerPath(name, field))
		}
		return value, nil
	}

	switch answerType {
	case "noul":
		noul, err := number("noul")
		if err != nil {
			return nil, err
		}
		return &NoulAnswer{Type: "noul", Noul: noul}, nil

	case "choice":
		confidence, err := number("confidence")
		if err != nil {
			return nil, err
		}
		choice, err := text("choice")
		if err != nil {
			return nil, err
		}
		probabilities, err := floatMap(fields, "probabilities", name, d)
		if err != nil {
			return nil, err
		}
		return &ChoiceAnswer{
			Type:          "choice",
			Choice:        choice,
			Confidence:    confidence,
			Probabilities: probabilities,
		}, nil

	default: // "score"
		score, err := number("score")
		if err != nil {
			return nil, err
		}
		confidence, err := number("confidence")
		if err != nil {
			return nil, err
		}
		legend, err := levelMap(fields, "legend", name, d)
		if err != nil {
			return nil, err
		}
		probabilities, err := integerMap(fields, "probabilities", name, d)
		if err != nil {
			return nil, err
		}
		return &ScoreAnswer{
			Type:          "score",
			Score:         score,
			Confidence:    confidence,
			Legend:        legend,
			Probabilities: probabilities,
		}, nil
	}
}

// floatMap decodes a field holding a JSON object of numbers keyed by name.
func floatMap(fields map[string]json.RawMessage, field, name string, d *decoder) (map[string]float64, error) {
	entries, ok := objectEntries(fields[field])
	if !ok {
		return nil, d.invalid(answerPath(name, field))
	}
	decoded := make(map[string]float64, len(entries))
	for key, entry := range entries {
		number, ok := decodeNumber(entry)
		if !ok {
			return nil, d.invalid(answerPath(name, field, key))
		}
		decoded[key] = number
	}
	return decoded, nil
}

// levelMap decodes a field holding a JSON object keyed by integer rubric levels.
func levelMap(fields map[string]json.RawMessage, field, name string, d *decoder) (map[int]any, error) {
	entries, ok := objectEntries(fields[field])
	if !ok {
		return nil, d.invalid(answerPath(name, field))
	}
	decoded := make(map[int]any, len(entries))
	for key, entry := range entries {
		level, err := strconv.Atoi(key)
		if err != nil {
			return nil, d.invalid(answerPath(name, field, key))
		}
		var item any
		if err := json.Unmarshal(entry, &item); err != nil {
			return nil, d.invalid(answerPath(name, field, key))
		}
		decoded[level] = item
	}
	return decoded, nil
}

// integerMap decodes a field holding a JSON object of numbers keyed by integer rubric levels.
func integerMap(fields map[string]json.RawMessage, field, name string, d *decoder) (map[int]float64, error) {
	entries, ok := objectEntries(fields[field])
	if !ok {
		return nil, d.invalid(answerPath(name, field))
	}
	decoded := make(map[int]float64, len(entries))
	for key, entry := range entries {
		level, err := strconv.Atoi(key)
		if err != nil {
			return nil, d.invalid(answerPath(name, field, key))
		}
		number, ok := decodeNumber(entry)
		if !ok {
			return nil, d.invalid(answerPath(name, field, key))
		}
		decoded[level] = number
	}
	return decoded, nil
}

// answerPath renders the dotted field path for an answer field, dropping any trailing empty
// segment so the path stays stable when the offending key is unknown.
func answerPath(name string, segments ...string) string {
	path := "answers." + name
	for _, segment := range segments {
		if segment != "" {
			path += "." + segment
		}
	}
	return path
}

// modelsEnvelope is the shape of a List Models success payload. The field name is part of the
// API contract, so it is modeled as a struct.
type modelsEnvelope struct {
	Models json.RawMessage `json:"models"`
}

// parseListModelsResponse decodes a List Models success response. Every field is required.
func parseListModelsResponse(resp *response) (*ListModelsResponse, error) {
	d := newDecoder(resp)
	var wire modelsEnvelope
	if err := openInto(d, &wire); err != nil {
		return nil, err
	}
	if isJSONNull(wire.Models) {
		return nil, d.invalid("models")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(wire.Models, &entries); err != nil {
		return nil, d.invalid("models")
	}
	models := make([]ModelMetadata, 0, len(entries))
	for index, entry := range entries {
		path := "models[" + strconv.Itoa(index) + "]"
		fields, ok := objectEntries(entry)
		if !ok {
			return nil, d.invalid(path)
		}
		var model ModelMetadata
		for _, target := range []struct {
			name  string
			value *string
		}{
			{"name", &model.Name},
			{"description", &model.Description},
			{"release_date", &model.ReleaseDate},
		} {
			value, ok := fields[target.name]
			if !ok {
				return nil, d.invalid(path + "." + target.name)
			}
			if err := json.Unmarshal(value, target.value); err != nil {
				return nil, d.invalid(path + "." + target.name)
			}
		}
		models = append(models, model)
	}

	return &ListModelsResponse{
		Models:      models,
		requestID:   resp.RequestID,
		rawResponse: rawHTTPResponse(resp),
		rawBody:     resp.Body,
	}, nil
}

// rawHTTPResponse rebuilds an *http.Response for the raw-response accessor, carrying the status,
// headers, and originating request. The body itself is exposed through
// [SystemOneResponse.RawBody] and [ListModelsResponse.RawBody].
func rawHTTPResponse(resp *response) *http.Response {
	raw := &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       http.NoBody,
		Request: &http.Request{
			Method: resp.Method,
			URL:    resp.URL,
			Header: make(http.Header),
		},
	}
	if resp.RequestID != "" {
		raw.Header.Set(HeaderRequestID, resp.RequestID)
	}
	return raw
}
