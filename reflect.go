package typesafe

import (
	"encoding/json"
	"reflect"
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
// encoding/json semantics, where a missing field leaves the zero value.
func missingRequiredField(document map[string]json.RawMessage, target reflect.Type) string {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		return ""
	}
	for field := range target.Fields() {
		if !field.IsExported() || field.Anonymous {
			continue
		}
		name, optional := jsonFieldName(field)
		if optional || name == "" || canBeAbsent(field.Type) {
			continue
		}
		if _, present := document[name]; !present {
			return name
		}
	}
	return ""
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
