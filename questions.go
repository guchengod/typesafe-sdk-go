package typesafe

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Question is implemented by every question the SDK can send: [NoulQuestion], [ChoiceQuestion],
// [ScoreQuestion], and [RawQuestion].
type Question interface {
	// QuestionType returns the wire discriminator: "noul", "choice", or "score".
	QuestionType() string

	// Validate reports whether the question is well formed under the given name.
	Validate(name string) error
}

// Questions maps the names you choose to the questions they identify. The names key the answers in
// the response.
//
// The values are typed as [Question], so a value that is not a question is a compile error. Build
// them with [NewNoul], [NewChoice], and [NewScore], or with [RawQuestion] for a question shape this
// version does not model. To pass a plain map[string]any, hand it to the client directly — the
// question parameter accepts any accepted container, which [NormalizeQuestions] validates.
type Questions map[string]Question

// NoulCriteria describes what counts as a yes or no answer. Both fields accept a string, a map,
// or a slice, and may be left nil.
type NoulCriteria struct {
	// True describes what counts as a yes answer.
	True any `json:"true,omitzero"`

	// False describes what counts as a no answer.
	False any `json:"false,omitzero"`
}

// QuestionOption refines a question constructed by [NewNoul], [NewChoice], or [NewScore].
type QuestionOption func(*questionOptions)

type questionOptions struct {
	instructions any
	criteria     *NoulCriteria
	extra        map[string]any
}

// WithInstructions sets the question's instructions: the question to ask, expressed as a string,
// a JSON object, or an array.
func WithInstructions(instructions any) QuestionOption {
	return func(o *questionOptions) { o.instructions = instructions }
}

// WithNoulCriteria sets the criteria describing the yes and no outcomes of a noul question.
func WithNoulCriteria(criteria NoulCriteria) QuestionOption {
	return func(o *questionOptions) { o.criteria = &criteria }
}

// WithQuestionField adds a field to a question's wire form. It exists for forward compatibility
// with request fields this SDK version does not model; a field that collides with a modeled one
// replaces it.
func WithQuestionField(key string, value any) QuestionOption {
	return func(o *questionOptions) {
		if o.extra == nil {
			o.extra = make(map[string]any)
		}
		o.extra[key] = value
	}
}

func resolveQuestionOptions(opts []QuestionOption) questionOptions {
	var options questionOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}

// resolveInstructions picks the instructions for a question: an explicit [WithInstructions] wins
// over the value passed positionally. Instructions that are absent or blank are left off the wire,
// because the server treats a question with neither instructions nor criteria as malformed.
func resolveInstructions(positional any, options questionOptions) any {
	instructions := positional
	if options.instructions != nil {
		instructions = options.instructions
	}
	if text, ok := instructions.(string); ok && strings.TrimSpace(text) == "" {
		return nil
	}
	return instructions
}

// NoulQuestion is a yes/no question or statement.
//
// See the noul primitive (https://docs.typesafe.ai/primitives/noul) for details.
type NoulQuestion struct {
	// Type is always "noul".
	Type string `json:"type"`

	// Instructions is the question to ask, expressed as a string, a JSON object, or an array.
	Instructions any `json:"instructions,omitzero"`

	// Criteria describes what counts as a yes or no answer.
	Criteria *NoulCriteria `json:"criteria,omitzero"`

	extra map[string]any
}

// NewNoul creates a yes/no question. The instructions may be a string, a map, or a slice, and may
// be nil when criteria alone describe the question.
//
//	typesafe.NewNoul("Is this message spam?")
//	typesafe.NewNoul("Is this spam?", typesafe.WithNoulCriteria(typesafe.NoulCriteria{
//		True:  "Unsolicited advertising",
//		False: "A legitimate conversation",
//	}))
//
// Options refine the question: WithInstructions overrides the instructions given positionally.
func NewNoul(instructions any, opts ...QuestionOption) *NoulQuestion {
	options := resolveQuestionOptions(opts)
	return &NoulQuestion{
		Type:         "noul",
		Instructions: resolveInstructions(instructions, options),
		Criteria:     options.criteria,
		extra:        options.extra,
	}
}

// QuestionType returns the wire discriminator.
func (q *NoulQuestion) QuestionType() string { return "noul" }

// Validate accepts every noul question; the server interprets a bare statement with no criteria.
func (q *NoulQuestion) Validate(string) error { return nil }

// MarshalJSON encodes the question, including any fields added by [WithQuestionField].
func (q *NoulQuestion) MarshalJSON() ([]byte, error) {
	type wire NoulQuestion
	return marshalWithExtra(wire(*q), q.extra)
}

// ChoiceQuestion selects between named alternatives.
//
// See the choice primitive (https://docs.typesafe.ai/primitives/choice) for details.
type ChoiceQuestion struct {
	// Type is always "choice".
	Type string `json:"type"`

	// Criteria maps each label to its description: a string, a JSON object, an array, or nil for
	// an undescribed label.
	Criteria map[string]any `json:"criteria"`

	// Instructions is the question to ask, expressed as a string, a JSON object, or an array.
	Instructions any `json:"instructions,omitzero"`

	extra map[string]any
}

// NewChoice creates a choice question.
//
//	typesafe.NewChoice(
//		map[string]any{"calm": nil, "angry": "An upset or hostile message"},
//		typesafe.WithInstructions("What is the tone of this message?"),
//	)
func NewChoice(criteria map[string]any, opts ...QuestionOption) *ChoiceQuestion {
	options := resolveQuestionOptions(opts)
	return &ChoiceQuestion{
		Type:         "choice",
		Criteria:     criteria,
		Instructions: resolveInstructions(nil, options),
		extra:        options.extra,
	}
}

// QuestionType returns the wire discriminator.
func (q *ChoiceQuestion) QuestionType() string { return "choice" }

// Validate reports a question that has no criteria or exceeds 255 options.
func (q *ChoiceQuestion) Validate(name string) error {
	if q.Criteria == nil {
		return &SDKError{Message: fmt.Sprintf("Question %q requires \"criteria\".", name)}
	}
	if len(q.Criteria) > 255 {
		if name != "" {
			return &SDKError{Message: fmt.Sprintf("Choice question %q exceeds maximum of 255 options.", name)}
		}
		return &SDKError{Message: "Choice question exceeds maximum of 255 options."}
	}
	return nil
}

// MarshalJSON encodes the question, including any fields added by [WithQuestionField].
func (q *ChoiceQuestion) MarshalJSON() ([]byte, error) {
	type wire ChoiceQuestion
	return marshalWithExtra(wire(*q), q.extra)
}

// ScoreQuestion rates content using an ordered rubric.
//
// See the score primitive (https://docs.typesafe.ai/primitives/score) for details.
type ScoreQuestion struct {
	// Type is always "score".
	Type string `json:"type"`

	// Criteria is the ordered rubric: each entry's position is its score, starting at zero. Each
	// entry is a string, a JSON object, or an array.
	Criteria []any `json:"criteria"`

	// Instructions is the question to ask, expressed as a string, a JSON object, or an array.
	Instructions any `json:"instructions,omitzero"`

	extra map[string]any
}

// NewScore creates a score question with an ordered rubric.
//
//	typesafe.NewScore(
//		[]any{"can wait", "needs attention this week", "needs attention today"},
//		typesafe.WithInstructions("How urgent is this message?"),
//	)
func NewScore(criteria []any, opts ...QuestionOption) *ScoreQuestion {
	options := resolveQuestionOptions(opts)
	return &ScoreQuestion{
		Type:         "score",
		Criteria:     criteria,
		Instructions: resolveInstructions(nil, options),
		extra:        options.extra,
	}
}

// QuestionType returns the wire discriminator.
func (q *ScoreQuestion) QuestionType() string { return "score" }

// Validate reports a score question whose rubric does not have between 2 and 10 levels.
func (q *ScoreQuestion) Validate(name string) error {
	if len(q.Criteria) < 2 || len(q.Criteria) > 10 {
		return &ScoreError{Name: name}
	}
	return nil
}

// MarshalJSON encodes the question, including any fields added by [WithQuestionField].
func (q *ScoreQuestion) MarshalJSON() ([]byte, error) {
	type wire ScoreQuestion
	return marshalWithExtra(wire(*q), q.extra)
}

// ScoreError reports a score question with an invalid rubric.
type ScoreError struct {
	// Name is the question name that failed validation.
	Name string
}

func (e *ScoreError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("Score question %q requires between 2 and 10 levels.", e.Name)
	}
	return "Score question requires between 2 and 10 levels."
}

func (e *ScoreError) isTypeSafeError() {}

// RawQuestion is a question sent as a plain map, for request fields this SDK version does not
// model:
//
//	typesafe.RawQuestion{"type": "noul", "instructions": "Is this about billing?"}
type RawQuestion map[string]any

// QuestionType returns the "type" field, or the empty string when absent.
func (q RawQuestion) QuestionType() string {
	questionType, _ := q["type"].(string)
	return questionType
}

// Validate applies the structural checks the SDK can make without duplicating the server's
// schema: the type must be a non-empty string, and a choice or score question must carry
// criteria.
func (q RawQuestion) Validate(name string) error {
	questionType, ok := q["type"].(string)
	if !ok || questionType == "" {
		return &SDKError{
			Message: fmt.Sprintf("Question %q must be a question object or a map with a nonempty string \"type\".", name),
		}
	}
	if questionType != "choice" && questionType != "score" {
		return nil
	}
	criteria, present := q["criteria"]
	if !present {
		return &SDKError{Message: fmt.Sprintf("Question %q requires \"criteria\".", name)}
	}
	if questionType == "choice" {
		if criteriaMap, ok := criteria.(map[string]any); ok && len(criteriaMap) > 255 {
			if name != "" {
				return &SDKError{Message: fmt.Sprintf("Choice question %q exceeds maximum of 255 options.", name)}
			}
			return &SDKError{Message: "Choice question exceeds maximum of 255 options."}
		}
	}
	if questionType == "score" && isInvalidScoreCriteria(criteria) {
		return &ScoreError{Name: name}
	}
	return nil
}

// isInvalidScoreCriteria reports whether a raw criteria value does not have between 2 and 10 score levels.
func isInvalidScoreCriteria(criteria any) bool {
	switch value := criteria.(type) {
	case nil:
		return true
	case []any:
		return len(value) < 2 || len(value) > 10
	case []string:
		return len(value) < 2 || len(value) > 10
	case json.RawMessage:
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err == nil {
			return len(items) < 2 || len(items) > 10
		}
		return string(value) == "[]" || string(value) == "null"
	default:
		return false
	}
}

// NormalizeQuestions validates the questions before they are encoded and returns the map that is
// sent on the wire.
//
// It accepts [Questions], map[string]Question, map[string]*NoulQuestion and the sibling maps,
// map[string]any, and [RawQuestion] values.
func NormalizeQuestions(questions any) (map[string]any, error) {
	entries, err := questionEntries(questions)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, &SDKError{Message: "At least one question is required."}
	}
	for name, question := range entries {
		if err := validateQuestion(name, question); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// questionEntries converts the accepted question container forms into a plain map.
func questionEntries(questions any) (map[string]any, error) {
	switch typed := questions.(type) {
	case nil:
		return nil, &SDKError{Message: "At least one question is required."}
	case Questions:
		return toStringKeyedMap(typed), nil
	case RawQuestion:
		// A RawQuestion is a named map[string]any, so accept it as a container the same way
		// Questions is accepted.
		return map[string]any(typed), nil
	case map[string]any:
		return typed, nil
	case map[string]Question:
		return toStringKeyedMap(typed), nil
	case map[string]*NoulQuestion:
		return toStringKeyedMap(typed), nil
	case map[string]*ChoiceQuestion:
		return toStringKeyedMap(typed), nil
	case map[string]*ScoreQuestion:
		return toStringKeyedMap(typed), nil
	default:
		return nil, &SDKError{
			Message: fmt.Sprintf("Questions must be a map of names to questions, not %T.", questions),
		}
	}
}

func toStringKeyedMap[V any](source map[string]V) map[string]any {
	target := make(map[string]any, len(source))
	for name, question := range source {
		target[name] = question
	}
	return target
}

// validateQuestion applies a question's own validation, falling back to the raw structural checks.
func validateQuestion(name string, question any) error {
	switch typed := question.(type) {
	case Question:
		return typed.Validate(name)
	case map[string]any:
		return RawQuestion(typed).Validate(name)
	default:
		return &SDKError{
			Message: fmt.Sprintf("Question %q must be a question object or a map with a nonempty string \"type\".", name),
		}
	}
}

// marshalWithExtra encodes a modeled question together with any forward-compatibility fields.
func marshalWithExtra(question any, extra map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(question)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return encoded, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	for key, value := range extra {
		fields[key] = value
	}
	return json.Marshal(fields)
}
