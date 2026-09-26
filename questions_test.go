package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// questionsWire returns the JSON wire form of a value as a decoded generic map.
func questionsWire(t *testing.T, value any) map[string]any {
	t.Helper()
	var decoded map[string]any
	questionsDecode(t, value, &decoded)
	return decoded
}

// questionsJSON returns the canonical JSON encoding of a value. json.Marshal sorts object keys,
// so two encodings are equal exactly when the values are structurally equal.
func questionsJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	return string(encoded)
}

// questionsDecode marshals value and unmarshals it into target.
func questionsDecode(t *testing.T, value, target any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", encoded, err)
	}
}

// questionsAssertEqual compares two values by their canonical JSON encoding.
func questionsAssertEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	gotJSON, wantJSON := questionsJSON(t, got), questionsJSON(t, want)
	if gotJSON != wantJSON {
		t.Errorf("%s = %s, want %s", label, gotJSON, wantJSON)
	}
}

// questionsAssertError asserts the exact message and the TypeSafeError marker.
func questionsAssertError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	var marker TypeSafeError
	if !errors.As(err, &marker) {
		t.Errorf("error of type %T does not implement TypeSafeError", err)
	}
}

// questionsAssertNoError fails the test when err is non-nil.
func questionsAssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// newQuestionsClient returns a mock client whose API answers every question in the request, the way
// the real API does, so a test that only asserts the request still receives a decodable response.
func newQuestionsClient(t *testing.T, opts ...Option) (*Client, *mockTransport) {
	t.Helper()
	return newMockClient(t, func(request *http.Request, _ int) *http.Response {
		return JSONResponse(http.StatusOK, answeringQuestionsJSON(t, request))
	}, opts...)
}

// answeringQuestionsJSON builds a success payload with one noul answer per question id the request
// carries. The transport drains the request body before the handler runs, so the body is replayed
// through GetBody, which http.NewRequest sets for a byte slice.
func answeringQuestionsJSON(t *testing.T, request *http.Request) string {
	t.Helper()
	var body []byte
	if request.GetBody != nil {
		reader, err := request.GetBody()
		if err != nil {
			t.Fatalf("replaying the request body: %v", err)
		}
		read, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("reading the request body: %v", err)
		}
		body = read
	}
	var sent struct {
		Questions map[string]json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal(body, &sent)
	names := make([]string, 0, len(sent.Questions))
	for name := range sent.Questions {
		names = append(names, name)
	}
	sort.Strings(names)
	answers := make([]string, 0, len(names))
	for _, name := range names {
		key, err := json.Marshal(name)
		if err != nil {
			t.Fatalf("encoding the question id %q: %v", name, err)
		}
		answers = append(answers, fmt.Sprintf("%s:{\"type\":\"noul\",\"noul\":0.9}", key))
	}
	return fmt.Sprintf(`{"model":"jev-latest","usage":{"input_tokens":12,"output_tokens":3},"answers":{%s}}`, strings.Join(answers, ","))
}

// TestQuestionsMarshalWireForms pins the JSON form of every modeled question type: optional
// fields are omitted when unset, and nil and empty values are distinct on the wire.
func TestQuestionsMarshalWireForms(t *testing.T) {
	tests := []struct {
		name     string
		question Question
		want     map[string]any
	}{
		{
			name:     "noul with instructions",
			question: NewNoul("Is this message spam?"),
			want:     map[string]any{"type": "noul", "instructions": "Is this message spam?"},
		},
		{
			name:     "noul omits instructions and criteria when unset",
			question: NewNoul(nil),
			want:     map[string]any{"type": "noul"},
		},
		{
			name:     "noul omits a blank instruction",
			question: NewNoul(""),
			want:     map[string]any{"type": "noul"},
		},
		{
			name:     "noul omits a whitespace-only instruction",
			question: NewNoul("   "),
			want:     map[string]any{"type": "noul"},
		},
		{
			name:     "an instruction option overrides the positional value",
			question: NewNoul("positional", WithInstructions("from the option")),
			want:     map[string]any{"type": "noul", "instructions": "from the option"},
		},
		{
			name:     "an instruction option supplies the value when the positional is blank",
			question: NewNoul("", WithInstructions("from the option")),
			want:     map[string]any{"type": "noul", "instructions": "from the option"},
		},
		{
			name:     "choice omits a blank instruction",
			question: NewChoice(map[string]any{"a": nil}, WithInstructions("")),
			want:     map[string]any{"type": "choice", "criteria": map[string]any{"a": nil}},
		},
		{
			name:     "score omits a blank instruction",
			question: NewScore([]any{"low", "high"}, WithInstructions(" ")),
			want:     map[string]any{"type": "score", "criteria": []any{"low", "high"}},
		},
		{
			name:     "noul keeps an empty array instruction",
			question: NewNoul([]any{}),
			want:     map[string]any{"type": "noul", "instructions": []any{}},
		},
		{
			name: "noul keeps an object instruction",
			question: NewNoul(
				map[string]any{"text": "Classify", "extra": nil},
				WithNoulCriteria(NoulCriteria{True: map[string]any{"extra": nil}}),
			),
			want: map[string]any{
				"type":         "noul",
				"instructions": map[string]any{"text": "Classify", "extra": nil},
				"criteria":     map[string]any{"true": map[string]any{"extra": nil}},
			},
		},
		{
			name:     "noul with both criteria sides",
			question: NewNoul("Is this spam?", WithNoulCriteria(NoulCriteria{True: "Unsolicited advertising", False: "A legitimate conversation"})),
			want: map[string]any{
				"type":         "noul",
				"instructions": "Is this spam?",
				"criteria":     map[string]any{"true": "Unsolicited advertising", "false": "A legitimate conversation"},
			},
		},
		{
			name:     "noul with only the true side",
			question: NewNoul("Is this spam?", WithNoulCriteria(NoulCriteria{True: "Unsolicited"})),
			want: map[string]any{
				"type":         "noul",
				"instructions": "Is this spam?",
				"criteria":     map[string]any{"true": "Unsolicited"},
			},
		},
		{
			name:     "noul with only the false side",
			question: NewNoul("Is this spam?", WithNoulCriteria(NoulCriteria{False: "Wanted"})),
			want: map[string]any{
				"type":         "noul",
				"instructions": "Is this spam?",
				"criteria":     map[string]any{"false": "Wanted"},
			},
		},
		{
			name:     "noul with array criteria",
			question: NewNoul(nil, WithNoulCriteria(NoulCriteria{True: []any{"yes", nil}})),
			want: map[string]any{
				"type":     "noul",
				"criteria": map[string]any{"true": []any{"yes", nil}},
			},
		},
		{
			name:     "noul with zero-valued criteria keeps an empty object",
			question: NewNoul("Is this spam?", WithNoulCriteria(NoulCriteria{})),
			want: map[string]any{
				"type":         "noul",
				"instructions": "Is this spam?",
				"criteria":     map[string]any{},
			},
		},
		{
			name:     "choice with a nil description",
			question: NewChoice(map[string]any{"calm": nil}, WithInstructions("What is the tone?")),
			want: map[string]any{
				"type":         "choice",
				"criteria":     map[string]any{"calm": nil},
				"instructions": "What is the tone?",
			},
		},
		{
			name:     "choice omits instructions when unset",
			question: NewChoice(map[string]any{"calm": nil}),
			want:     map[string]any{"type": "choice", "criteria": map[string]any{"calm": nil}},
		},
		{
			name:     "choice with a nil criteria map keeps null",
			question: NewChoice(nil),
			want:     map[string]any{"type": "choice", "criteria": nil},
		},
		{
			name:     "choice with one option keeps the criteria object",
			question: NewChoice(map[string]any{"calm": nil}, WithInstructions("What is the tone?")),
			want:     map[string]any{"type": "choice", "instructions": "What is the tone?", "criteria": map[string]any{"calm": nil}},
		},
		{
			name: "choice with structured descriptions",
			question: NewChoice(map[string]any{
				"a": "text",
				"b": map[string]any{"example": nil},
				"c": []any{"x", 1},
				"d": nil,
			}),
			want: map[string]any{
				"type": "choice",
				"criteria": map[string]any{
					"a": "text",
					"b": map[string]any{"example": nil},
					"c": []any{"x", 1},
					"d": nil,
				},
			},
		},
		{
			name: "score with a mixed rubric",
			question: NewScore(
				[]any{"can wait", map[string]any{"label": "today", "weight": nil}, []any{"level", 2}},
				WithInstructions("How urgent is this message?"),
			),
			want: map[string]any{
				"type":         "score",
				"criteria":     []any{"can wait", map[string]any{"label": "today", "weight": nil}, []any{"level", 2}},
				"instructions": "How urgent is this message?",
			},
		},
		{
			name:     "score omits instructions when unset",
			question: NewScore([]any{"good"}),
			want:     map[string]any{"type": "score", "criteria": []any{"good"}},
		},
		{
			name:     "score with a nil rubric keeps null",
			question: NewScore(nil),
			want:     map[string]any{"type": "score", "criteria": nil},
		},
		{
			name:     "score with an empty rubric keeps an empty array",
			question: NewScore([]any{}),
			want:     map[string]any{"type": "score", "criteria": []any{}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			questionsAssertEqual(t, "wire form", questionsWire(t, test.question), test.want)
		})
	}
}

// TestQuestionsTypeDiscriminators checks that each constructor stamps its discriminator on the
// struct and on the wire, and that a well-formed question validates under its name.
func TestQuestionsTypeDiscriminators(t *testing.T) {
	tests := []struct {
		name     string
		question Question
		wantType string
	}{
		{name: "noul", question: NewNoul("Is this spam?"), wantType: "noul"},
		{name: "choice", question: NewChoice(map[string]any{"calm": nil}), wantType: "choice"},
		{name: "score", question: NewScore([]any{"low", "high"}), wantType: "score"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.question.QuestionType(); got != test.wantType {
				t.Errorf("QuestionType() = %q, want %q", got, test.wantType)
			}
			if got := questionsWire(t, test.question)["type"]; got != test.wantType {
				t.Errorf("wire type = %v, want %q", got, test.wantType)
			}
			if err := test.question.Validate("q"); err != nil {
				t.Errorf("Validate() = %v, want nil for a well-formed question", err)
			}
		})
	}

	if got := NewNoul(nil).Type; got != "noul" {
		t.Errorf("NoulQuestion.Type = %q, want %q", got, "noul")
	}
	if got := NewChoice(nil).Type; got != "choice" {
		t.Errorf("ChoiceQuestion.Type = %q, want %q", got, "choice")
	}
	if got := NewScore(nil).Type; got != "score" {
		t.Errorf("ScoreQuestion.Type = %q, want %q", got, "score")
	}

	// The request schema requires no instructions field: only criteria is required, and only for a
	// choice or a score question, so a question that omits instructions is accepted rather than
	// stopped locally.
	for name, question := range map[string]Question{
		"choice without instructions": NewChoice(map[string]any{"calm": nil}),
		"score without instructions":  NewScore([]any{"low", "high"}),
	} {
		if err := question.Validate("q"); err != nil {
			t.Errorf("Validate() on a %s = %v, want nil", name, err)
		}
	}
	if err := NewNoul(nil).Validate("q"); err != nil {
		t.Errorf("Validate() on a bare noul = %v, want nil", err)
	}
}

// TestRawQuestionWireForm checks that a raw question reaches the wire unchanged, including the
// fields this SDK version does not model.
func TestRawQuestionWireForm(t *testing.T) {
	raw := RawQuestion{
		"type":         "noul",
		"instructions": "Is this about billing?",
		"weight":       3,
		"metadata":     map[string]any{"source": nil},
	}
	questionsAssertEqual(t, "wire form", questionsWire(t, raw), map[string]any{
		"type":         "noul",
		"instructions": "Is this about billing?",
		"weight":       3,
		"metadata":     map[string]any{"source": nil},
	})
}

// TestQuestionsNilInstructionsNeverSerializedAsNull checks that an unset instructions field is
// omitted rather than encoded as null.
func TestQuestionsNilInstructionsNeverSerializedAsNull(t *testing.T) {
	questions := map[string]any{
		"noul":            NewNoul(nil),
		"noul zero value": &NoulQuestion{Type: "noul"},
		"noul nil option": NewNoul(nil, WithInstructions(nil)),
		"choice":          NewChoice(map[string]any{"calm": "Calm", "angry": nil}),
		"score":           NewScore([]any{"bad", "good"}, WithInstructions(nil)),
	}

	if encoded := questionsJSON(t, questions); strings.Contains(encoded, `"instructions":null`) {
		t.Errorf("wire form contains a null instructions field: %s", encoded)
	}

	decoded := questionsWire(t, questions)
	for name, question := range decoded {
		fields, ok := question.(map[string]any)
		if !ok {
			t.Fatalf("question %q decoded to %T, want an object", name, question)
		}
		if _, present := fields["instructions"]; present {
			t.Errorf("question %q carries an instructions field, want it omitted", name)
		}
	}
}

// TestQuestionsWithQuestionFieldAddsAndOverrides checks the forward-compatibility escape hatch:
// new fields are added, and a field colliding with a modeled one replaces it.
func TestQuestionsWithQuestionFieldAddsAndOverrides(t *testing.T) {
	tests := []struct {
		name     string
		question Question
		want     map[string]any
	}{
		{
			name:     "adds an unmodeled field",
			question: NewNoul("Is this spam?", WithQuestionField("weight", 3)),
			want:     map[string]any{"type": "noul", "instructions": "Is this spam?", "weight": 3},
		},
		{
			name: "adds several fields",
			question: NewChoice(
				map[string]any{"calm": nil},
				WithQuestionField("weight", 3),
				WithQuestionField("locale", "en"),
				WithQuestionField("metadata", map[string]any{"source": nil}),
			),
			want: map[string]any{
				"type":     "choice",
				"criteria": map[string]any{"calm": nil},
				"weight":   3,
				"locale":   "en",
				"metadata": map[string]any{"source": nil},
			},
		},
		{
			name:     "overrides the discriminator",
			question: NewNoul("Is this spam?", WithQuestionField("type", "future")),
			want:     map[string]any{"type": "future", "instructions": "Is this spam?"},
		},
		{
			name:     "overrides instructions",
			question: NewScore([]any{"good"}, WithInstructions("How good?"), WithQuestionField("instructions", "How good, really?")),
			want:     map[string]any{"type": "score", "criteria": []any{"good"}, "instructions": "How good, really?"},
		},
		{
			name:     "overrides the whole criteria object",
			question: NewChoice(map[string]any{"calm": nil}, WithQuestionField("criteria", map[string]any{"angry": "Hostile"})),
			want:     map[string]any{"type": "choice", "criteria": map[string]any{"angry": "Hostile"}},
		},
		{
			name:     "last write wins",
			question: NewNoul("Is this spam?", WithQuestionField("weight", 1), WithQuestionField("weight", 2)),
			want:     map[string]any{"type": "noul", "instructions": "Is this spam?", "weight": 2},
		},
		{
			name:     "a nil value writes an explicit null",
			question: NewNoul("Is this spam?", WithQuestionField("metadata", nil)),
			want:     map[string]any{"type": "noul", "instructions": "Is this spam?", "metadata": nil},
		},
		{
			name:     "a field added to a raw-style question",
			question: NewChoice(nil, WithQuestionField("criteria", nil)),
			want:     map[string]any{"type": "choice", "criteria": nil},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			questionsAssertEqual(t, "wire form", questionsWire(t, test.question), test.want)
		})
	}
}

// TestNormalizeQuestionsPreservesTypedQuestions checks that normalization keeps the caller's own
// returned document carries the caller's own question objects, encoded to the wire form.
func TestNormalizeQuestionsPreservesTypedQuestions(t *testing.T) {
	noul := NewNoul("Spam?")
	choice := NewChoice(map[string]any{"calm": nil}, WithInstructions("Tone?"))
	score := NewScore([]any{"bad", "good"}, WithInstructions("Quality?"))

	normalized, err := NormalizeQuestions(Questions{"noul": noul, "choice": choice, "score": score})
	questionsAssertNoError(t, err)

	for name, want := range map[string]any{"noul": noul, "choice": choice, "score": score} {
		if normalized[name] != want {
			t.Errorf("normalized[%q] = %#v, want the same %T the caller passed", name, normalized[name], want)
		}
	}

	questionsAssertEqual(t, "wire form", questionsWire(t, normalized), map[string]any{
		"noul":   map[string]any{"type": "noul", "instructions": "Spam?"},
		"choice": map[string]any{"type": "choice", "instructions": "Tone?", "criteria": map[string]any{"calm": nil}},
		"score":  map[string]any{"type": "score", "instructions": "Quality?", "criteria": []any{"bad", "good"}},
	})
}

// TestNormalizeQuestionsAcceptsEveryContainer checks that every question survives normalization,
// whatever the container type the caller used.
func TestNormalizeQuestionsAcceptsEveryContainer(t *testing.T) {
	noulWant := map[string]any{"type": "noul", "instructions": "Is this spam?"}
	choiceWant := map[string]any{"type": "choice", "criteria": map[string]any{"calm": nil}, "instructions": "Tone?"}
	scoreWant := map[string]any{"type": "score", "criteria": []any{"bad", "good"}}
	rawWant := map[string]any{"type": "score", "criteria": []any{"bad", "good"}, "weight": 3}

	tests := []struct {
		name      string
		questions func() any
		want      map[string]any
	}{
		{
			name:      "Questions",
			questions: func() any { return Questions{"q": NewNoul("Is this spam?")} },
			want:      map[string]any{"q": noulWant},
		},
		{
			name:      "RawQuestion used as the container",
			questions: func() any { return RawQuestion{"q": NewNoul("Is this spam?")} },
			want:      map[string]any{"q": noulWant},
		},
		{
			name: "map of any with typed values",
			questions: func() any {
				return map[string]any{"q": NewChoice(map[string]any{"calm": nil}, WithInstructions("Tone?"))}
			},
			want: map[string]any{"q": choiceWant},
		},
		{
			name:      "map of Question",
			questions: func() any { return map[string]Question{"q": NewNoul("Is this spam?")} },
			want:      map[string]any{"q": noulWant},
		},
		{
			name:      "map of noul pointers",
			questions: func() any { return map[string]*NoulQuestion{"q": NewNoul("Is this spam?")} },
			want:      map[string]any{"q": noulWant},
		},
		{
			name: "map of choice pointers",
			questions: func() any {
				return map[string]*ChoiceQuestion{"q": NewChoice(map[string]any{"calm": nil}, WithInstructions("Tone?"))}
			},
			want: map[string]any{"q": choiceWant},
		},
		{
			name:      "map of score pointers",
			questions: func() any { return map[string]*ScoreQuestion{"q": NewScore([]any{"bad", "good"})} },
			want:      map[string]any{"q": scoreWant},
		},
		{
			name: "raw question value",
			questions: func() any {
				return Questions{"q": RawQuestion{"type": "score", "criteria": []any{"bad", "good"}, "weight": 3}}
			},
			want: map[string]any{"q": rawWant},
		},
		{
			name: "raw map value",
			questions: func() any {
				return map[string]any{"q": map[string]any{"type": "score", "criteria": []any{"bad", "good"}, "weight": 3}}
			},
			want: map[string]any{"q": rawWant},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeQuestions(test.questions())
			questionsAssertNoError(t, err)
			questionsAssertEqual(t, "normalized questions", questionsWire(t, normalized), test.want)
		})
	}
}

// TestNormalizeQuestionsLeavesRawQuestionsUntouched checks that raw questions pass through
// structurally, without rewriting or dropping the fields this SDK version does not model.
func TestNormalizeQuestionsLeavesRawQuestionsUntouched(t *testing.T) {
	raw := map[string]any{
		"type":     "score",
		"criteria": []string{"bad", "good"},
		"weight":   3,
		"nested":   map[string]any{"k": nil},
	}
	before := questionsJSON(t, raw)
	score := NewScore([]any{"low", "high"}, WithInstructions("How good?"))
	instructions := map[string]any{"text": "Spam?", "extra": nil}

	// The typed container rejects an untyped dict at compile time, so a caller holding one — here a
	// dict decoded from elsewhere — passes it through the untyped container instead.
	normalized, err := NormalizeQuestions(map[string]any{
		"raw":   raw,
		"typed": NewNoul(instructions),
		"score": score,
	})
	questionsAssertNoError(t, err)

	if after := questionsJSON(t, raw); after != before {
		t.Errorf("raw question was modified: %s, want %s", after, before)
	}
	if len(score.Criteria) != 2 || score.Criteria[0] != "low" {
		t.Errorf("score criteria = %v, want the caller's rubric untouched", score.Criteria)
	}
	if len(instructions) != 2 || instructions["text"] != "Spam?" {
		t.Errorf("instructions = %v, want the caller's object untouched", instructions)
	}
	questionsAssertEqual(t, "normalized raw question", questionsWire(t, normalized["raw"]), map[string]any{
		"type":     "score",
		"criteria": []any{"bad", "good"},
		"weight":   3,
		"nested":   map[string]any{"k": nil},
	})
	if _, ok := normalized["typed"].(*NoulQuestion); !ok {
		t.Errorf("normalized typed question = %T, want *typesafe.NoulQuestion", normalized["typed"])
	}
}

// TestNormalizeQuestionsRequiresAtLeastOneQuestion checks the empty-input error across every
// accepted container form.
func TestNormalizeQuestionsRequiresAtLeastOneQuestion(t *testing.T) {
	tests := []struct {
		name      string
		questions any
	}{
		{name: "nil", questions: nil},
		{name: "empty Questions", questions: Questions{}},
		{name: "empty map of any", questions: map[string]any{}},
		{name: "empty map of Question", questions: map[string]Question{}},
		{name: "empty map of noul pointers", questions: map[string]*NoulQuestion{}},
		{name: "empty map of choice pointers", questions: map[string]*ChoiceQuestion{}},
		{name: "empty map of score pointers", questions: map[string]*ScoreQuestion{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeQuestions(test.questions)
			questionsAssertError(t, err, "At least one question is required.")
			if normalized != nil {
				t.Errorf("NormalizeQuestions() = %v, want nil alongside the error", normalized)
			}
		})
	}
}

// TestNormalizeQuestionsRejectsUnsupportedContainers checks the container type error.
func TestNormalizeQuestionsRejectsUnsupportedContainers(t *testing.T) {
	tests := []struct {
		name      string
		questions any
		want      string
	}{
		{
			name:      "slice",
			questions: []any{NewNoul("Is this spam?")},
			want:      "Questions must be a map of names to questions, not []interface {}.",
		},
		{
			name:      "string",
			questions: "noul",
			want:      "Questions must be a map of names to questions, not string.",
		},
		{
			name:      "integer",
			questions: 42,
			want:      "Questions must be a map of names to questions, not int.",
		},
		{
			name:      "map of strings",
			questions: map[string]string{"q": "noul"},
			want:      "Questions must be a map of names to questions, not map[string]string.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeQuestions(test.questions)
			questionsAssertError(t, err, test.want)
		})
	}
}

// TestNormalizeQuestionsRejectsNonQuestionValues checks the error for a value that is neither a
// modeled question nor a raw question map.
func TestNormalizeQuestionsRejectsNonQuestionValues(t *testing.T) {
	const want = `Question "x" must be a question object or a map with a nonempty string "type".`
	tests := []struct {
		name  string
		value any
	}{
		{name: "nil", value: nil},
		{name: "string", value: "noul"},
		{name: "integer", value: 1},
		{name: "slice", value: []any{"noul"}},
		{name: "empty struct", value: struct{}{}},
		{name: "map of strings", value: map[string]string{"type": "noul"}},
		{name: "nil raw question", value: RawQuestion(nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeQuestions(map[string]any{"x": test.value})
			questionsAssertError(t, err, want)
			if normalized != nil {
				t.Errorf("NormalizeQuestions() = %v, want nil alongside the error", normalized)
			}
		})
	}
}

// TestNormalizeQuestionsRawQuestionStructuralChecks pins the structural checks a raw question
// must pass, and their exact messages.
func TestNormalizeQuestionsRawQuestionStructuralChecks(t *testing.T) {
	const missingType = `Question "x" must be a question object or a map with a nonempty string "type".`
	const requiresCriteria = `Question "x" requires "criteria".`
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "missing type", value: map[string]any{"instructions": "Missing type"}, want: missingType},
		{name: "empty type", value: map[string]any{"type": ""}, want: missingType},
		{name: "nil type", value: map[string]any{"type": nil}, want: missingType},
		{name: "integer type", value: map[string]any{"type": 1}, want: missingType},
		{name: "slice type", value: map[string]any{"type": []any{"future"}}, want: missingType},
		{name: "typed raw question without type", value: RawQuestion{"instructions": "Missing type"}, want: missingType},
		{name: "choice without a criteria key", value: map[string]any{"type": "choice"}, want: requiresCriteria},
		{name: "score without a criteria key", value: map[string]any{"type": "score"}, want: requiresCriteria},
		{name: "typed raw choice without a criteria key", value: RawQuestion{"type": "choice"}, want: requiresCriteria},
		// Accepted raw questions: an unknown type passes through, and a noul needs no criteria.
		{name: "unknown type", value: map[string]any{"type": "future", "nested": map[string]any{"k": nil}}},
		{name: "noul without criteria", value: map[string]any{"type": "noul"}},
		{name: "noul with both criteria sides", value: map[string]any{"type": "noul", "instructions": "Spam?", "criteria": map[string]any{"true": "Yes", "false": nil}}},
		{name: "noul with empty criteria", value: map[string]any{"type": "noul", "criteria": map[string]any{}}},
		{name: "noul with an explicit null instruction", value: map[string]any{"type": "noul", "instructions": nil, "criteria": nil}},
		{name: "choice with an explicit null instruction", value: map[string]any{"type": "choice", "instructions": nil, "criteria": map[string]any{"a": nil}}},
		{name: "choice with nil criteria", value: map[string]any{"type": "choice", "criteria": nil}},
		{name: "choice with an empty criteria object", value: map[string]any{"type": "choice", "criteria": map[string]any{}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeQuestions(map[string]any{"x": test.value})
			if test.want != "" {
				questionsAssertError(t, err, test.want)
				return
			}
			questionsAssertNoError(t, err)
			questionsAssertEqual(t, "normalized question", questionsWire(t, normalized["x"]), test.value)
		})
	}
}

// TestNormalizeQuestionsScoreCriteriaValidation pins which rubrics are rejected as invalid and
// which heterogeneous criteria values are accepted.
func TestNormalizeQuestionsScoreCriteriaValidation(t *testing.T) {
	const want = `Score question "q" requires between 2 and 10 levels.`
	rejected := []struct {
		name  string
		value any
	}{
		{name: "typed nil criteria", value: NewScore(nil)},
		{name: "typed empty criteria", value: NewScore([]any{})},
		{name: "typed 1-element criteria", value: NewScore([]any{"one"})},
		{name: "typed 11-element criteria", value: NewScore([]any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})},
		{name: "raw nil criteria", value: map[string]any{"type": "score", "criteria": nil}},
		{name: "raw empty array", value: map[string]any{"type": "score", "criteria": []any{}}},
		{name: "raw 1-element array", value: map[string]any{"type": "score", "criteria": []any{"one"}}},
		{name: "raw 11-element array", value: map[string]any{"type": "score", "criteria": []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}},
		{name: "raw empty string array", value: map[string]any{"type": "score", "criteria": []string{}}},
		{name: "raw 1-element string array", value: map[string]any{"type": "score", "criteria": []string{"one"}}},
		{name: "raw empty raw message", value: map[string]any{"type": "score", "criteria": json.RawMessage("[]")}},
		{name: "raw 1-element raw message", value: map[string]any{"type": "score", "criteria": json.RawMessage(`["one"]`)}},
		{name: "raw null raw message", value: map[string]any{"type": "score", "criteria": json.RawMessage("null")}},
		{name: "raw 1-element map slice", value: map[string]any{"type": "score", "criteria": []map[string]any{{"desc": "one"}}}},
		{name: "raw 11-element int slice", value: map[string]any{"type": "score", "criteria": []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}},
		{name: "typed raw question with empty criteria", value: RawQuestion{"type": "score", "criteria": []any{}}},
		{name: "typed raw question with 1-element criteria", value: RawQuestion{"type": "score", "criteria": []any{"one"}}},
	}

	for _, test := range rejected {
		t.Run("rejects "+test.name, func(t *testing.T) {
			_, err := NormalizeQuestions(map[string]any{"q": test.value})
			questionsAssertError(t, err, want)
			var scoreErr *ScoreError
			if !errors.As(err, &scoreErr) {
				t.Fatalf("error = %T, want *ScoreError", err)
			}
			if scoreErr.Name != "q" {
				t.Errorf("ScoreError.Name = %q, want %q", scoreErr.Name, "q")
			}
		})
	}

	accepted := []struct {
		name  string
		value any
		want  map[string]any
	}{
		{
			name:  "typed nonempty criteria",
			value: NewScore([]any{"good", "better"}),
			want:  map[string]any{"type": "score", "criteria": []any{"good", "better"}},
		},
		{
			name:  "typed criteria holding nil entries",
			value: NewScore([]any{nil, nil}),
			want:  map[string]any{"type": "score", "criteria": []any{nil, nil}},
		},
		{
			name:  "typed 10-element criteria",
			value: NewScore([]any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
			want:  map[string]any{"type": "score", "criteria": []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		},
		{
			name:  "raw string array",
			value: map[string]any{"type": "score", "criteria": []string{"bad", "good"}},
			want:  map[string]any{"type": "score", "criteria": []any{"bad", "good"}},
		},
		{
			name:  "raw raw message",
			value: map[string]any{"type": "score", "criteria": json.RawMessage(`["bad","good"]`)},
			want:  map[string]any{"type": "score", "criteria": []any{"bad", "good"}},
		},
		{
			name:  "raw object criteria",
			value: map[string]any{"type": "score", "criteria": map[string]any{}},
			want:  map[string]any{"type": "score", "criteria": map[string]any{}},
		},
	}

	for _, test := range accepted {
		t.Run("accepts "+test.name, func(t *testing.T) {
			normalized, err := NormalizeQuestions(map[string]any{"q": test.value})
			questionsAssertNoError(t, err)
			questionsAssertEqual(t, "normalized question", questionsWire(t, normalized["q"]), test.want)
		})
	}
}

// TestNormalizeQuestionsChoiceCriteriaValidation checks the choice side of the criteria rule.
func TestNormalizeQuestionsChoiceCriteriaValidation(t *testing.T) {
	const requiresCriteria = `Question "q" requires "criteria".`

	t.Run("typed choice with nil criteria is rejected", func(t *testing.T) {
		_, err := NormalizeQuestions(Questions{"q": NewChoice(nil)})
		questionsAssertError(t, err, requiresCriteria)
	})

	t.Run("typed choice with empty criteria is sent as given", func(t *testing.T) {
		normalized, err := NormalizeQuestions(Questions{"q": NewChoice(map[string]any{})})
		questionsAssertNoError(t, err)
		questionsAssertEqual(t, "criteria", questionsWire(t, normalized["q"])["criteria"], map[string]any{})
	})

	t.Run("typed choice with > 255 options is rejected", func(t *testing.T) {
		options := make(map[string]any, 256)
		for i := range 256 {
			options[fmt.Sprintf("opt%d", i)] = nil
		}
		_, err := NormalizeQuestions(Questions{"q": NewChoice(options)})
		questionsAssertError(t, err, `Choice question "q" exceeds maximum of 255 options.`)
	})

	t.Run("raw choice with > 255 options is rejected", func(t *testing.T) {
		options := make(map[string]any, 256)
		for i := range 256 {
			options[fmt.Sprintf("opt%d", i)] = nil
		}
		_, err := NormalizeQuestions(map[string]any{"q": map[string]any{"type": "choice", "criteria": options}})
		questionsAssertError(t, err, `Choice question "q" exceeds maximum of 255 options.`)

		stringOptions := make(map[string]string, 256)
		for i := range 256 {
			stringOptions[fmt.Sprintf("opt%d", i)] = "desc"
		}
		_, err = NormalizeQuestions(map[string]any{"q": map[string]any{"type": "choice", "criteria": stringOptions}})
		questionsAssertError(t, err, `Choice question "q" exceeds maximum of 255 options.`)
	})

	t.Run("raw choice with an explicit nil criteria passes through", func(t *testing.T) {
		normalized, err := NormalizeQuestions(map[string]any{"q": map[string]any{"type": "choice", "criteria": nil}})
		questionsAssertNoError(t, err)
		questionsAssertEqual(t, "normalized question", questionsWire(t, normalized["q"]),
			map[string]any{"type": "choice", "criteria": nil})
	})

	t.Run("raw noul with a nil criteria passes through", func(t *testing.T) {
		normalized, err := NormalizeQuestions(map[string]any{"q": map[string]any{"type": "noul", "criteria": nil}})
		questionsAssertNoError(t, err)
		questionsAssertEqual(t, "normalized question", questionsWire(t, normalized["q"]),
			map[string]any{"type": "noul", "criteria": nil})
	})
}

// TestScoreErrorTextAndMarker checks the error the caller receives for an empty rubric.
func TestScoreErrorTextAndMarker(t *testing.T) {
	const want = `Score question "rating" requires between 2 and 10 levels.`

	err := &ScoreError{Name: "rating"}
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	var marker TypeSafeError
	if !errors.As(err, &marker) {
		t.Errorf("*ScoreError does not implement TypeSafeError")
	}

	_, normalizeErr := NormalizeQuestions(Questions{"rating": NewScore([]any{})})
	if normalizeErr == nil {
		t.Fatal("NormalizeQuestions() error = nil, want the score error")
	}
	if normalizeErr.Error() != want {
		t.Errorf("NormalizeQuestions() error = %q, want %q", normalizeErr.Error(), want)
	}
}

// TestRawQuestionQuestionTypeAndValidate checks discriminator extraction and the nil-safe
// structural checks.
func TestRawQuestionQuestionTypeAndValidate(t *testing.T) {
	tests := []struct {
		name     string
		question RawQuestion
		wantType string
	}{
		{name: "noul", question: RawQuestion{"type": "noul"}, wantType: "noul"},
		{name: "choice", question: RawQuestion{"type": "choice"}, wantType: "choice"},
		{name: "score", question: RawQuestion{"type": "score"}, wantType: "score"},
		{name: "unknown type", question: RawQuestion{"type": "future"}, wantType: "future"},
		{name: "missing type", question: RawQuestion{"instructions": "Spam?"}, wantType: ""},
		{name: "nil type", question: RawQuestion{"type": nil}, wantType: ""},
		{name: "non-string type", question: RawQuestion{"type": 1}, wantType: ""},
		{name: "slice type", question: RawQuestion{"type": []any{"future"}}, wantType: ""},
		{name: "nil question", question: RawQuestion(nil), wantType: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.question.QuestionType(); got != test.wantType {
				t.Errorf("QuestionType() = %q, want %q", got, test.wantType)
			}
		})
	}

	if err := RawQuestion(nil).Validate("q"); err == nil {
		t.Error("Validate() on a nil raw question = nil, want an error")
	} else if want := `Question "q" must be a question object or a map with a nonempty string "type".`; err.Error() != want {
		t.Errorf("Validate() error = %q, want %q", err.Error(), want)
	}

	if err := (RawQuestion{"type": "future"}).Validate("q"); err != nil {
		t.Errorf("Validate() on an unknown question type = %v, want nil", err)
	}
}

// TestQuestionsNilOptionAndNilValueSafety checks the nil-tolerance the SDK documents: a nil
// option is ignored, and unset fields never reach the wire.
func TestQuestionsNilOptionAndNilValueSafety(t *testing.T) {
	questionsAssertEqual(t, "noul with a nil option", questionsWire(t, NewNoul("Is this spam?", nil)),
		map[string]any{"type": "noul", "instructions": "Is this spam?"})
	questionsAssertEqual(t, "choice with a nil option", questionsWire(t, NewChoice(map[string]any{"calm": nil}, nil)),
		map[string]any{"type": "choice", "criteria": map[string]any{"calm": nil}})
	questionsAssertEqual(t, "score with a nil option", questionsWire(t, NewScore([]any{"good"}, nil)),
		map[string]any{"type": "score", "criteria": []any{"good"}})

	// A nil option never displaces a value set by another option.
	questionsAssertEqual(t, "instructions survive a nil option",
		questionsWire(t, NewNoul(nil, WithInstructions("Updated?"), nil)),
		map[string]any{"type": "noul", "instructions": "Updated?"})
}

// TestSystemOneSendsEveryQuestionTypeOnTheWire checks the request body the API receives for a
// mixed question set: the decoded state, model, and questions structure.
func TestSystemOneSendsEveryQuestionTypeOnTheWire(t *testing.T) {
	client, transport := newQuestionsClient(t, WithDefaultModel("client-default"))

	state := map[string]any{
		"message": "I was charged twice.",
		"missing": nil,
		"items":   []any{nil, map[string]any{"nested": nil}},
	}
	questions := Questions{
		"billing": NewNoul("Is this message about billing?"),
		"tone": NewChoice(
			map[string]any{"calm": nil, "angry": "An upset or hostile message"},
			WithInstructions("What is the tone of this message?"),
		),
		"urgency": NewScore(
			[]any{"can wait", map[string]any{"label": "today"}, []any{"level", 2}},
			WithInstructions("How urgent is this message?"),
		),
		"raw": RawQuestion{"type": "noul", "instructions": "Is this about billing?", "weight": 3},
	}

	response, err := client.SystemOne(t.Context(), state, questions)
	questionsAssertNoError(t, err)
	if response.Model != "jev-latest" {
		t.Errorf("response.Model = %q, want %q", response.Model, "jev-latest")
	}

	call := transport.lastCall(t)
	if call.Method != http.MethodPost {
		t.Errorf("request method = %q, want %q", call.Method, http.MethodPost)
	}
	if want := DefaultBaseURL + SystemOnePath; call.URL != want {
		t.Errorf("request URL = %q, want %q", call.URL, want)
	}

	body := decodingBody(t, call)
	if len(body) != 3 {
		t.Errorf("request body has %d fields (%s), want state, model, and questions only",
			len(body), questionsJSON(t, body))
	}
	questionsAssertEqual(t, "state", body["state"], state)
	if body["model"] != "client-default" {
		t.Errorf("model = %v, want the client default %q", body["model"], "client-default")
	}
	questionsAssertEqual(t, "questions", body["questions"], map[string]any{
		"billing": map[string]any{"type": "noul", "instructions": "Is this message about billing?"},
		"tone": map[string]any{
			"type":         "choice",
			"criteria":     map[string]any{"calm": nil, "angry": "An upset or hostile message"},
			"instructions": "What is the tone of this message?",
		},
		"urgency": map[string]any{
			"type":         "score",
			"criteria":     []any{"can wait", map[string]any{"label": "today"}, []any{"level", 2}},
			"instructions": "How urgent is this message?",
		},
		"raw": map[string]any{"type": "noul", "instructions": "Is this about billing?", "weight": 3},
	})
}

// TestSystemOneModelSelection checks that the model comes from WithModel, then the client
// default.
func TestSystemOneModelSelection(t *testing.T) {
	tests := []struct {
		name         string
		defaultModel string
		opts         []RequestOption
		want         string
	}{
		{name: "client default", defaultModel: "client-default", want: "client-default"},
		{
			name:         "call model wins",
			defaultModel: "client-default",
			opts:         []RequestOption{WithModel("call-model")},
			want:         "call-model",
		},
		{
			name:         "empty call model falls back to the default",
			defaultModel: "client-default",
			opts:         []RequestOption{WithModel("")},
			want:         "client-default",
		},
		{
			name:         "last call model wins",
			defaultModel: "client-default",
			opts:         []RequestOption{WithModel("first"), WithModel("second")},
			want:         "second",
		},
		{name: "sdk default when the client has none", want: DefaultModel},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{}
			if test.defaultModel != "" {
				options = append(options, WithDefaultModel(test.defaultModel))
			}
			client, transport := newQuestionsClient(t, options...)

			_, err := client.SystemOne(t.Context(), "text", Questions{"q": NewNoul("Is this spam?")}, test.opts...)
			questionsAssertNoError(t, err)

			body := decodingBody(t, transport.lastCall(t))
			if body["model"] != test.want {
				t.Errorf("model = %v, want %q", body["model"], test.want)
			}
		})
	}
}

// TestSystemOneExtraBodyOverridesTopLevelFields checks that extra_body is a shallow,
// last-write-wins overlay: a colliding key replaces the SDK's value outright.
func TestSystemOneExtraBodyOverridesTopLevelFields(t *testing.T) {
	client, transport := newQuestionsClient(t, WithDefaultModel("client-default"))

	_, err := client.SystemOne(
		t.Context(),
		map[string]any{"message": "original"},
		Questions{"billing": NewNoul("Is this about billing?")},
		WithModel("call-model"),
		WithExtraBody(map[string]any{
			"state":       map[string]any{"replaced": true},
			"model":       "extra-model",
			"questions":   map[string]any{"other": map[string]any{"type": "noul", "instructions": "Replaced?"}},
			"temperature": 0.2,
		}),
	)
	questionsAssertNoError(t, err)

	body := decodingBody(t, transport.lastCall(t))
	questionsAssertEqual(t, "state", body["state"], map[string]any{"replaced": true})
	if body["model"] != "extra-model" {
		t.Errorf("model = %v, want %q", body["model"], "extra-model")
	}
	questionsAssertEqual(t, "questions", body["questions"],
		map[string]any{"other": map[string]any{"type": "noul", "instructions": "Replaced?"}})
	if body["temperature"] != 0.2 {
		t.Errorf("temperature = %v, want 0.2", body["temperature"])
	}
	if _, present := body["billing"]; present {
		t.Errorf("questions = %s, want the extra body's object to replace the SDK's set", questionsJSON(t, body["questions"]))
	}
}

// TestSystemOneValidatesQuestionsBeforeApplyingExtraBody checks that extra_body cannot bypass
// question validation: an invalid set fails before any request is sent.
func TestSystemOneValidatesQuestionsBeforeApplyingExtraBody(t *testing.T) {
	client, transport := newQuestionsClient(t)

	_, err := client.SystemOne(
		t.Context(),
		"text",
		Questions{"q": NewScore(nil)},
		WithExtraBody(map[string]any{"questions": map[string]any{"q": map[string]any{"type": "noul"}}}),
	)
	questionsAssertError(t, err, `Score question "q" requires between 2 and 10 levels.`)

	if calls := transport.callsSnapshot(); len(calls) != 0 {
		t.Errorf("SDK sent %d requests, want none", len(calls))
	}
}

// TestSystemOneAcceptsEveryQuestionContainer checks that every accepted container reaches the wire
// container form reaches the API as the same questions document.
func TestSystemOneAcceptsEveryQuestionContainer(t *testing.T) {
	noulWire := map[string]any{"type": "noul", "instructions": "Is this spam?"}
	choiceWire := map[string]any{"type": "choice", "criteria": map[string]any{"calm": nil}, "instructions": "Tone?"}
	scoreWire := map[string]any{"type": "score", "criteria": []any{"bad", "good"}}

	containers := []struct {
		name      string
		questions any
		want      map[string]any
	}{
		{name: "Questions", questions: Questions{"q": NewNoul("Is this spam?")}, want: map[string]any{"q": noulWire}},
		{name: "map of noul pointers", questions: map[string]*NoulQuestion{"q": NewNoul("Is this spam?")}, want: map[string]any{"q": noulWire}},
		{name: "map of choice pointers", questions: map[string]*ChoiceQuestion{"q": NewChoice(map[string]any{"calm": nil}, WithInstructions("Tone?"))}, want: map[string]any{"q": choiceWire}},
		{name: "map of score pointers", questions: map[string]*ScoreQuestion{"q": NewScore([]any{"bad", "good"})}, want: map[string]any{"q": scoreWire}},
		{name: "map of Question", questions: map[string]Question{"q": NewChoice(map[string]any{"calm": nil}, WithInstructions("Tone?"))}, want: map[string]any{"q": choiceWire}},
		{name: "map of any with a raw value", questions: map[string]any{"q": map[string]any{"type": "noul", "instructions": "Is this spam?"}}, want: map[string]any{"q": noulWire}},
		{name: "map of any with a typed value", questions: map[string]any{"q": NewScore([]any{"bad", "good"})}, want: map[string]any{"q": scoreWire}},
		{name: "raw question value", questions: Questions{"q": RawQuestion{"type": "score", "criteria": []any{"bad", "good"}}}, want: map[string]any{"q": scoreWire}},
	}

	client, transport := newQuestionsClient(t)
	for _, container := range containers {
		t.Run(container.name, func(t *testing.T) {
			_, err := client.SystemOne(t.Context(), "text", container.questions)
			questionsAssertNoError(t, err)
		})
	}

	calls := transport.callsSnapshot()
	if len(calls) != len(containers) {
		t.Fatalf("SDK sent %d requests, want %d", len(calls), len(containers))
	}
	for index, container := range containers {
		questionsAssertEqual(t, "questions for "+container.name,
			decodingBody(t, calls[index])["questions"], container.want)
	}
}
