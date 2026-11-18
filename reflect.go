package typesafe

import (
	"encoding/json/jsontext"
	"reflect"
	"slices"
	"strings"
)

// typeOf returns the reflect.Type for T, memoized by the runtime.
func typeOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

// missingRequiredField reports the dotted path of the first top-level field of T that the
// response document does not contain, or "" when every required field is present.
//
// A field is required unless it can represent absence — a pointer, map, slice, or interface
// holds nil for "not reported" — or it is tagged `omitempty`, or it is untagged, embedded, or
// unexported. Only the top level is inspected: nested structures decode with standard
// encoding/json/v2 semantics, where a missing field leaves the zero value.
func missingRequiredField(document map[string]jsontext.Value, target reflect.Type) string {
	// Stack-backed seen slice avoids heap map allocation on flat and shallowly embedded structs.
	var stackSeen [8]reflect.Type
	return missingRequiredFieldSeen(document, target, stackSeen[:0])
}

func missingRequiredFieldSeen(document map[string]jsontext.Value, target reflect.Type, seen []reflect.Type) string {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct || slices.Contains(seen, target) {
		return ""
	}
	seen = append(seen, target)
	numFields := target.NumField()
	for i := range numFields {
		field := target.Field(i)
		if field.Anonymous {
			tag, tagged := field.Tag.Lookup("json")
			tagName, options, _ := strings.Cut(tag, ",")
			if tagName == "-" || strings.Contains(options, "omitempty") || strings.Contains(options, "omitzero") {
				continue
			}
			// Named anonymous fields (e.g. `json:"meta"`) are encoded as nested objects, not promoted.
			if !tagged || tagName == "" {
				if field.Type.Kind() == reflect.Pointer {
					continue
				}
				if missing := missingRequiredFieldSeen(document, field.Type, seen); missing != "" {
					return missing
				}
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		name, optional := jsonFieldName(field)
		if optional || name == "" || canBeAbsent(field.Type) {
			continue
		}
		if !hasNonNullDocumentField(document, name) {
			return name
		}
	}
	return ""
}

func hasNonNullDocumentField(document map[string]jsontext.Value, name string) bool {
	if raw, present := document[name]; present {
		return !isJSONNull(raw)
	}
	for key, raw := range document {
		if strings.EqualFold(key, name) {
			return !isJSONNull(raw)
		}
	}
	return false
}

// canBeAbsent reports whether a value of the given type can represent "the server did not send
// this field". Pointer, map, slice, and interface fields can; every other kind decodes to a zero
// value that is indistinguishable from a reported value, so those fields are required.
func canBeAbsent(kind reflect.Type) bool {
	switch kind.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return true
	default:
		return false
	}
}

// jsonFieldName returns the JSON name of a struct field and whether its tag marks it optional.
// It reports an empty name for a field excluded from JSON encoding.
func jsonFieldName(field reflect.StructField) (string, bool) {
	tag, tagged := field.Tag.Lookup("json")
	if !tagged {
		return field.Name, false
	}
	name, options, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		name = field.Name
	}
	return name, strings.Contains(options, "omitempty") || strings.Contains(options, "omitzero")
}
