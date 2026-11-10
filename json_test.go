package typesafe

import (
	"encoding/json/jsontext"
	"testing"
)

func TestJSONPointerPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pointer jsontext.Pointer
		want    string
	}{
		{name: "empty", pointer: "", want: ""},
		{name: "object field", pointer: "/answers/tone/confidence", want: "answers.tone.confidence"},
		{name: "array index", pointer: "/answers/0/noul", want: "answers.0.noul"},
		{name: "escaped slash", pointer: "/a~1b/c", want: "a/b.c"},
		{name: "escaped tilde", pointer: "/a~0b", want: "a~b"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := jsonPointerPath(test.pointer); got != test.want {
				t.Errorf("jsonPointerPath(%q) = %q, want %q", test.pointer, got, test.want)
			}
		})
	}
}
