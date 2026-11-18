package typesafe

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
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
// State can be a string, a JSON-serializable struct, a slice, or a map[string]any.
// Keep state focused on the relevant context to avoid distractor noise. Questions accepts
// [Questions], map[string]Question, or map[string]any.
//
// Images are collected automatically wherever they appear: in state, a question's instructions, or
// a question's criteria. A request carrying at least one image also sends the
// TypeSafe-Danger-Zone-Images header, unless the call disables it with [WithCallImages].
//
// Errors are typed: [SDKError] for invalid input, [APIError] and its status-specific subclasses
// ([BadRequestError], [AuthenticationError], [PermissionDeniedError], [NotFoundError],
// [UnprocessableEntityError], [PayloadTooLargeError], [UnsupportedMediaTypeError], [RateLimitError],
// [OverloadedError], [InternalServerError])
// for unsuccessful responses, [APIConnectionError] and [APITimeoutError] for transport failures,
// and [APIResponseValidationError] for a success response that could not be decoded.
func (c *Client) SystemOne(ctx context.Context, state any, questions any, opts ...RequestOption) (*SystemOneResponse, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	raw, asked, err := c.systemOne(ctx, state, questions, opts)
	if err != nil {
		return nil, err
	}
	return parseSystemOneResponseWithContext(ctx, raw, asked, c.config.Logger)
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
//	result, err := client.SystemOneAs[Ticket](ctx, state, questions)
//
// A top-level field that is not a pointer, map, slice, or interface, and that is tagged neither
// `omitzero` nor `omitempty`, is required: its absence is reported as an
// [APIResponseValidationError] naming the field rather than silently decoding to a zero value.
// Answer types this SDK version does not model are dropped from the decoded document, exactly as in
// the [Client.SystemOne] path.
func (c *Client) SystemOneAs[T any](ctx context.Context, state any, questions any, opts ...RequestOption) (*T, error) {
	if err := c.checkOpen(ctx); err != nil {
		return nil, err
	}
	raw, asked, err := c.systemOne(ctx, state, questions, opts)
	if err != nil {
		return nil, err
	}
	parsed, err := parseSystemOneResponseWithContext(ctx, raw, asked, c.config.Logger)
	if err != nil {
		return nil, err
	}
	document, err := liftAnswers(raw, parsed, asked)
	if err != nil {
		return nil, err
	}
	return decodeResponse[T](document, raw)
}

// SystemOneAs answers questions and decodes the response into T using client.
//
// Prefer the generic method [Client.SystemOneAs] in new Go 1.27+ code; this package-level wrapper
// remains supported for backwards compatibility.
func SystemOneAs[T any](ctx context.Context, client *Client, state any, questions any, opts ...RequestOption) (*T, error) {
	return client.SystemOneAs[T](ctx, state, questions, opts...)
}

// systemOne builds and executes a System One request without decoding the payload. It also returns
// the question ids the request carries, in a stable order, which the response parser needs to check
// that every question was answered.
func (c *Client) systemOne(ctx context.Context, state any, questions any, opts []RequestOption) (*response, []string, error) {
	normalized, err := normalizeQuestionsReadOnly(questions)
	if err != nil {
		return nil, nil, err
	}
	options := resolveRequestOptions(opts)
	if options.assetsErr != nil {
		return nil, nil, options.assetsErr
	}

	body := map[string]any{
		"state":     state,
		"model":     cmp.Or(options.model, c.config.DefaultModel),
		"questions": normalized,
	}
	// extra_body is a shallow, last-write-wins overlay: a key that collides with state, model, or
	// questions replaces it, and object values are replaced rather than merged. It is applied before
	// images are collected so the assets match the body that is actually sent.
	maps.Copy(body, options.extraBody)
	if _, overridden := options.extraBody["questions"]; overridden {
		validatedExtraQuestions, err := normalizeQuestionsReadOnly(body["questions"])
		if err != nil {
			return nil, nil, err
		}
		body["questions"] = validatedExtraQuestions
	}
	// Images may appear in state, instructions, or criteria, so the final state and questions are
	// scanned into a per-call copy (allocated lazily when options.assets is empty) so concurrent or
	// repeated requests never mutate shared caller maps and plain-text calls avoid map allocation.
	var assets map[string]any
	if len(options.assets) > 0 {
		assets = maps.Clone(options.assets)
	}
	if err := collectImagesInto(body["state"], &assets); err != nil {
		return nil, nil, err
	}
	if err := collectImagesInto(body["questions"], &assets); err != nil {
		return nil, nil, err
	}
	if _, overridden := body["assets"]; !overridden && len(assets) > 0 {
		body["assets"] = assets
	}
	// The images header is required for any request carrying image assets. Enable it for this call
	// unless the caller explicitly disabled it at the client or call level.
	if options.images == nil && !c.config.imagesSet && body["assets"] != nil {
		options.images = new(true)
	}

	req, err := c.newRequest(http.MethodPost, SystemOnePath, body, options)
	if err != nil {
		return nil, nil, err
	}
	raw, err := execute(ctx, c.config, c.execClient, req)
	if err != nil {
		return nil, nil, err
	}
	return raw, askedQuestionNames(body["questions"], normalized), nil
}

// sortedQuestionNames returns the question ids in a stable order, so a response that omits an answer
// is always reported for the same question.
func sortedQuestionNames(questions map[string]any) []string {
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// askedQuestionNames lists the question ids the request carries. extra_body may replace the questions
// the SDK built, and the response is checked against what was sent rather than what was planned.
func askedQuestionNames(sent any, built map[string]any) []string {
	if questions, ok := sent.(map[string]any); ok {
		return sortedQuestionNames(questions)
	}
	return sortedQuestionNames(built)
}

// liftAnswers rebuilds the response document so that every modeled answer is also available as a
// top-level key, unknown answer types are excluded from the decoded view, and unvalidated top-level
// keys cannot shadow validated answers or canonical envelope fields.
func liftAnswers(resp *response, parsed *SystemOneResponse, asked []string) (map[string]jsontext.Value, error) {
	var document map[string]jsontext.Value
	if err := jsonv2.Unmarshal(resp.Body, &document); err != nil {
		return nil, &SDKError{Message: "The response body could not be decoded as JSON", Cause: err}
	}
	encoded, ok := findCaseInsensitiveKey(document, "answers")
	if !ok {
		return document, nil
	}
	var entries map[string]jsontext.Value
	if err := jsonv2.Unmarshal(encoded, &entries); err != nil {
		return nil, newValidationError(resp, "answers")
	}
	modeled := entries
	if len(parsed.Answers) != len(entries) {
		modeled = make(map[string]jsontext.Value, len(parsed.Answers))
		for name := range parsed.Answers {
			if entry, exists := entries[name]; exists {
				modeled[name] = entry
			}
		}
		if answers, err := jsonv2.Marshal(modeled, marshalOptions); err == nil {
			deleteCaseInsensitiveKeys(document, "answers")
			document["answers"] = answers
		}
	}
	// Clear any unvalidated top-level keys that collide with asked question names, even when an
	// answer had an unrecognized type and was omitted from modeled.
	for _, name := range asked {
		if !isEnvelopeFieldName(name) {
			deleteCaseInsensitiveKeys(document, name)
		}
	}
	for name, entry := range modeled {
		if isEnvelopeFieldName(name) {
			continue
		}
		if slices.Contains(asked, name) {
			document[name] = entry
		} else if _, exists := findCaseInsensitiveKey(document, name); !exists {
			document[name] = entry
		}
	}
	return document, nil
}

func isEnvelopeFieldName(name string) bool {
	return strings.EqualFold(name, "model") ||
		strings.EqualFold(name, "usage") ||
		strings.EqualFold(name, "answers")
}

func findCaseInsensitiveKey(document map[string]jsontext.Value, target string) (jsontext.Value, bool) {
	if val, ok := document[target]; ok {
		return val, true
	}
	for key, val := range document {
		if strings.EqualFold(key, target) {
			return val, true
		}
	}
	return nil, false
}

func deleteCaseInsensitiveKeys(document map[string]jsontext.Value, target string) {
	maps.DeleteFunc(document, func(key string, _ jsontext.Value) bool {
		return strings.EqualFold(key, target)
	})
}

// decodeResponse decodes the lifted document into T, reporting the dotted path of the first
// field that is missing or has the wrong shape.
func decodeResponse[T any](document map[string]jsontext.Value, resp *response) (*T, error) {
	encoded, err := jsonv2.Marshal(document, marshalOptions)
	if err != nil {
		return nil, &SDKError{Message: "The response body could not be re-encoded as JSON", Cause: err}
	}
	var target T
	if err := jsonv2.Unmarshal(encoded, &target, unmarshalOptions); err != nil {
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
// dotted convention. The v2 decoder reports the offending location as a JSON Pointer.
func decodeFieldPath(err error) string {
	if semantic, ok := errors.AsType[*jsonv2.SemanticError](err); ok {
		return jsonPointerPath(semantic.JSONPointer)
	}
	return ""
}

// String implements fmt.Stringer for convenient debugging of a response.
func (r *SystemOneResponse) String() string {
	if r == nil {
		return "SystemOneResponse(nil)"
	}
	return fmt.Sprintf("SystemOneResponse{model: %s, answers: %d}", r.Model, len(r.Answers))
}
