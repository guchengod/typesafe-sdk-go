package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// SystemOne answers named questions about text or structured state.
//
// See System One (https://docs.typesafe.ai/concepts/system-one) for details.
//
// The response carries every answer keyed by the question name supplied here, alongside the
// model that answered and the token usage for the evaluation:
//
//	response, err := client.SystemOne(ctx,
//		map[string]any{"message": "I was charged twice. Please help."},
//		typesafe.Questions{
//			"billing": typesafe.NewNoul("Is this message about billing?"),
//			"tone": typesafe.NewChoice(
//				map[string]any{"calm": nil, "angry": nil},
//				typesafe.WithInstructions("What is the tone?"),
//			),
//		},
//	)
//	if err != nil {
//		return err
//	}
//	fmt.Println(response.Nouls()["billing"].Noul)
//
// Errors are typed: [SDKError] for invalid input, [APIError] and its status-specific subclasses
// for unsuccessful responses, [APIConnectionError] and [APITimeoutError] for transport failures,
// and [APIResponseValidationError] for a success response that could not be decoded.
func (c *Client) SystemOne(ctx context.Context, state any, questions any, opts ...RequestOption) (*SystemOneResponse, error) {
	raw, err := c.systemOne(ctx, state, questions, opts)
	if err != nil {
		return nil, err
	}
	return parseSystemOneResponse(raw, c.config.Logger)
}

// SystemOneAs answers questions and decodes the response into T, a caller-defined struct.
//
// T may declare the envelope fields ("model", "usage", "answers") and, additionally, one field
// per answer, named after the question. Each such field is populated from the matching entry of
// "answers":
//
//	type Ticket struct {
//		Model   string                 `json:"model"`
//		Usage   typesafe.Usage         `json:"usage"`
//		Billing typesafe.NoulAnswer    `json:"billing"`
//		Tone    typesafe.ChoiceAnswer  `json:"tone"`
//	}
//
//	result, err := typesafe.SystemOneAs[Ticket](ctx, client, state, questions)
//
// A top-level field that is not a pointer, map, slice, or interface, and that is not tagged
// `omitempty`, is required: its absence is reported as an [APIResponseValidationError] naming the
// field rather than silently decoding to a zero value. Answer types this SDK version does not
// model are dropped from the decoded document, exactly as in the [Client.SystemOne] path.
func SystemOneAs[T any](ctx context.Context, client *Client, state any, questions any, opts ...RequestOption) (*T, error) {
	raw, err := client.systemOne(ctx, state, questions, opts)
	if err != nil {
		return nil, err
	}
	parsed, err := parseSystemOneResponse(raw, client.config.Logger)
	if err != nil {
		return nil, err
	}
	document, err := liftAnswers(raw.Body, parsed)
	if err != nil {
		return nil, err
	}
	return decodeResponse[T](document, raw)
}

// systemOne builds and executes a System One request without decoding the payload.
func (c *Client) systemOne(ctx context.Context, state any, questions any, opts []RequestOption) (*response, error) {
	normalized, err := NormalizeQuestions(questions)
	if err != nil {
		return nil, err
	}
	options := resolveRequestOptions(opts)

	body := map[string]any{
		"state":     state,
		"model":     cmpOrString(options.model, c.config.DefaultModel),
		"questions": normalized,
	}
	// extra_body is a shallow, last-write-wins overlay: a key that collides with state, model, or
	// questions replaces it, and object values are replaced rather than merged.
	for key, value := range options.extraBody {
		body[key] = value
	}

	req, err := c.newRequest(http.MethodPost, SystemOnePath, body, options)
	if err != nil {
		return nil, err
	}
	return execute(ctx, c.config, c.httpClient, req)
}

// liftAnswers rebuilds the response document so that every modeled answer is also available as a
// top-level key, and unknown answer types are excluded from the decoded view.
func liftAnswers(body []byte, parsed *SystemOneResponse) (map[string]json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, &SDKError{Message: "The response body could not be decoded as JSON", Cause: err}
	}
	encoded, ok := document["answers"]
	if !ok {
		return document, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &entries); err != nil {
		return document, nil
	}
	modeled := make(map[string]json.RawMessage, len(parsed.Answers))
	for name := range parsed.Answers {
		if entry, ok := entries[name]; ok {
			modeled[name] = entry
		}
	}
	if len(modeled) != len(entries) {
		if answers, err := json.Marshal(modeled); err == nil {
			document["answers"] = answers
		}
	}
	for name, entry := range modeled {
		// Never displace a real envelope field; the SDK's own fields always win.
		if _, exists := document[name]; !exists {
			document[name] = entry
		}
	}
	return document, nil
}

// decodeResponse decodes the lifted document into T, reporting the dotted path of the first
// field that is missing or has the wrong shape.
func decodeResponse[T any](document map[string]json.RawMessage, resp *response) (*T, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, &SDKError{Message: "The response body could not be re-encoded as JSON", Cause: err}
	}
	var target T
	if err := json.Unmarshal(encoded, &target); err != nil {
		if answerErr, ok := errors.AsType[*answerDecodeError](err); ok {
			return nil, newValidationError(resp, answerErr.path)
		}
		return nil, newValidationError(resp, decodeFieldPath(err))
	}
	if missing := missingRequiredField(document, typeOf[T]()); missing != "" {
		return nil, newValidationError(resp, missing)
	}
	return &target, nil
}

// decodeFieldPath renders the field path reported by a JSON decode failure, keeping the SDK's
// dotted convention.
func decodeFieldPath(err error) string {
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return typeErr.Field
	}
	return ""
}

// cmpOrString returns the first non-empty value.
func cmpOrString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// String implements fmt.Stringer for convenient debugging of a response.
func (r *SystemOneResponse) String() string {
	return fmt.Sprintf("SystemOneResponse{model: %s, answers: %d}", r.Model, len(r.Answers))
}
