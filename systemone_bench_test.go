package typesafe

import (
	"encoding/json/jsontext"
	"testing"
)

var (
	benchSinkTicket   *benchTicketModel
	benchSinkDocument map[string]jsontext.Value
	benchSinkMissing  string
)

type benchEmbeddedMeta struct {
	Model string `json:"model"`
	Usage Usage  `json:"usage"`
}

type benchTicketModel struct {
	benchEmbeddedMeta
	Billing  NoulAnswer   `json:"billing"`
	Tone     ChoiceAnswer `json:"tone"`
	Urgency  ScoreAnswer  `json:"urgency"`
	Optional *BoxAnswer   `json:"optional,omitempty"`
	Answers  Answers      `json:"answers"`
}

const benchMultimodalResponse = `{
	"model": "jev-preview",
	"usage": {"input_tokens": 312, "output_tokens": 28},
	"answers": {
		"has_shape": {"type": "noul", "noul": 0.97},
		"shape_box": {"type": "box", "box": {"top": 0.1, "bottom": 0.4, "left": 0.15, "right": 0.45}, "confidence": 0.93}
	}
}`

const benchUnknownAnswerResponse = `{
	"model": "jev-latest",
	"usage": {"input_tokens": 120, "output_tokens": 18},
	"answers": {
		"billing": {"type": "noul", "noul": 0.98},
		"tone": {"type": "choice", "choice": "angry", "confidence": 0.9, "probabilities": {"calm": 0.1, "angry": 0.9}},
		"urgency": {"type": "score", "score": 1.7, "confidence": 0.8, "legend": {"0": "can wait", "1": "this week", "2": "today"}, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}},
		"future_primitive": {"type": "polygon", "points": [[0.1, 0.2], [0.3, 0.4]]}
	}
}`

// BenchmarkClientSystemOneMultimodal measures an end-to-end System One call carrying an embedded
// *Image asset and a Box question, exercising image discovery, asset serialization, header gating,
// and BoxAnswer decoding.
func BenchmarkClientSystemOneMultimodal(b *testing.B) {
	client, transport := benchmarkMockClient(b, benchMultimodalResponse)
	img, err := NewImageWithRef("img-bench-canvas", benchPNGBytes(64, 64), "png")
	if err != nil {
		b.Fatalf("NewImageWithRef() error = %v", err)
	}
	state := map[string]any{
		"photo":   img,
		"caption": "Synthetic benchmark canvas",
	}
	questions := Questions{
		"has_shape": NewNoul("Is there a shape in photo?"),
		"shape_box": NewBox("the blue rectangle in photo"),
	}
	ctx := b.Context()

	b.ReportAllocs()
	b.SetBytes(int64(len(benchMultimodalResponse)))
	for iteration := 0; b.Loop(); iteration++ {
		result, err := client.SystemOne(ctx, state, questions, WithModel(ModelJevPreview))
		if err != nil {
			b.Fatalf("SystemOne() error = %v", err)
		}
		benchSinkResponse = result
		if iteration%1024 == 1023 {
			transport.mu.Lock()
			transport.calls = transport.calls[:0]
			transport.mu.Unlock()
		}
	}
}

// BenchmarkSystemOneAs measures generic typed response decoding end-to-end, including answer
// lifting and reflection-based required-field validation on an embedded struct model.
func BenchmarkSystemOneAs(b *testing.B) {
	client, transport := benchmarkMockClient(b, fixedAnswerResponse)
	questions := fixedQuestions()
	state := map[string]any{"message": "I was charged twice. Please help."}
	ctx := b.Context()

	b.ReportAllocs()
	b.SetBytes(int64(len(fixedAnswerResponse)))
	for iteration := 0; b.Loop(); iteration++ {
		result, err := SystemOneAs[benchTicketModel](ctx, client, state, questions)
		if err != nil {
			b.Fatalf("SystemOneAs() error = %v", err)
		}
		benchSinkTicket = result
		if iteration%1024 == 1023 {
			transport.mu.Lock()
			transport.calls = transport.calls[:0]
			transport.mu.Unlock()
		}
	}
}

// BenchmarkLiftAnswers isolates answer-lifting document reconstruction for both modeled-only
// payloads and payloads containing unrecognized future answer types.
func BenchmarkLiftAnswers(b *testing.B) {
	logger := benchmarkLogger()

	b.Run("ModeledOnly", func(b *testing.B) {
		resp := benchmarkResponse(fixedAnswerResponse)
		asked := benchmarkAnswerIDs(b, fixedAnswerResponse)
		parsed, err := parseSystemOneResponse(resp, asked, logger)
		if err != nil {
			b.Fatalf("parseSystemOneResponse() error = %v", err)
		}
		b.ReportAllocs()
		b.SetBytes(int64(len(fixedAnswerResponse)))
		for b.Loop() {
			doc, err := liftAnswers(resp, parsed, asked)
			if err != nil {
				b.Fatalf("liftAnswers() error = %v", err)
			}
			benchSinkDocument = doc
		}
	})

	b.Run("DropUnknownType", func(b *testing.B) {
		resp := benchmarkResponse(benchUnknownAnswerResponse)
		asked := []string{"billing", "tone", "urgency"}
		parsed, err := parseSystemOneResponse(resp, asked, logger)
		if err != nil {
			b.Fatalf("parseSystemOneResponse() error = %v", err)
		}
		b.ReportAllocs()
		b.SetBytes(int64(len(benchUnknownAnswerResponse)))
		for b.Loop() {
			doc, err := liftAnswers(resp, parsed, asked)
			if err != nil {
				b.Fatalf("liftAnswers() error = %v", err)
			}
			benchSinkDocument = doc
		}
	})
}

// BenchmarkMissingRequiredField isolates the reflection walker in reflect.go that checks top-level
// and promoted anonymous struct fields for required JSON presence.
func BenchmarkMissingRequiredField(b *testing.B) {
	logger := benchmarkLogger()
	resp := benchmarkResponse(fixedAnswerResponse)
	asked := benchmarkAnswerIDs(b, fixedAnswerResponse)
	parsed, err := parseSystemOneResponse(resp, asked, logger)
	if err != nil {
		b.Fatalf("parseSystemOneResponse() error = %v", err)
	}
	doc, err := liftAnswers(resp, parsed, asked)
	if err != nil {
		b.Fatalf("liftAnswers() error = %v", err)
	}

	type flatTicket struct {
		Model   string       `json:"model"`
		Usage   Usage        `json:"usage"`
		Billing NoulAnswer   `json:"billing"`
		Tone    ChoiceAnswer `json:"tone"`
		Urgency ScoreAnswer  `json:"urgency"`
	}

	b.Run("FlatStruct", func(b *testing.B) {
		target := typeOf[flatTicket]()
		b.ReportAllocs()
		for b.Loop() {
			benchSinkMissing = missingRequiredField(doc, target)
		}
	})

	b.Run("EmbeddedStruct", func(b *testing.B) {
		target := typeOf[benchTicketModel]()
		b.ReportAllocs()
		for b.Loop() {
			benchSinkMissing = missingRequiredField(doc, target)
		}
	})
}
