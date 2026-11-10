package typesafe

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"strings"
)

// marshalOptions keeps encoded output stable and v1-compatible. The v2 encoder traverses maps in a
// non-deterministic order and encodes nil maps and slices as empty objects and arrays, while v1
// sorted names and encoded nils as null. Deterministic output keeps request bodies, logs, and tests
// reproducible; the nil options preserve the wire form callers already depend on.
var marshalOptions = jsonv2.JoinOptions(
	jsonv2.Deterministic(true),
	jsonv2.FormatNilMapAsNull(true),
	jsonv2.FormatNilSliceAsNull(true),
)

// unmarshalOptions preserves the v1 case-insensitive field matching that callers of [SystemOneAs]
// may rely on. It has no effect on the maps and manually decoded payloads the SDK parses itself.
var unmarshalOptions = jsonv2.MatchCaseInsensitiveNames(true)

// jsonPointerPath renders a JSON Pointer as the SDK's dotted field path, so an error reported by the
// v2 decoder names the offending field the same way the SDK's own validation does.
func jsonPointerPath(pointer jsontext.Pointer) string {
	text := string(pointer)
	if text == "" {
		return ""
	}
	segments := strings.Split(strings.TrimPrefix(text, "/"), "/")
	for index, segment := range segments {
		segment = strings.ReplaceAll(segment, "~1", "/")
		segments[index] = strings.ReplaceAll(segment, "~0", "~")
	}
	return strings.Join(segments, ".")
}
