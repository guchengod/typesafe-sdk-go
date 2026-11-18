package typesafe

import (
	"bytes"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"uuid"
)

// Asset describes an asset attached to a System One request.
type Asset struct {
	// Type is the kind of asset; currently always "image".
	Type string `json:"type,omitzero"`

	// Part is the name of the file part in a multipart request.
	Part string `json:"part,omitzero"`

	// DataBase64 is the standard base64-encoded image content.
	DataBase64 string `json:"data_base64,omitzero"`

	// Format is the image format: "png", "jpg", or "jpeg".
	Format string `json:"format,omitzero"`
}

// Assets maps reference strings to their Asset descriptions.
type Assets map[string]Asset

// Image represents an image asset that can be referenced in state, instructions, or criteria.
type Image struct {
	// Ref is the unique reference string used to refer to this image.
	Ref string

	// Format is the normalized format ("png" or "jpg").
	Format string

	// Data is the raw image file bytes.
	Data []byte

	// Base64 is the base64-encoded representation of Data.
	Base64 string
}

// NewImage creates an image asset with an auto-generated unique reference.
//
// Accepted formats are "png", "jpg", and "jpeg". Maximum size is 16 MiB.
func NewImage(data []byte, format string) (*Image, error) {
	ref := newRandomRef("img")
	return NewImageWithRef(ref, data, format)
}

// NewImageWithRef creates an image asset with a caller-chosen reference string.
func NewImageWithRef(ref string, data []byte, format string) (*Image, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &SDKError{Message: "Image reference cannot be empty."}
	}
	if strings.IndexFunc(ref, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) >= 0 {
		return nil, &SDKError{Message: "Image reference must not contain control characters."}
	}
	if len(data) == 0 {
		return nil, &SDKError{Message: "Image data cannot be empty."}
	}
	if len(data) > MaxImageSizeLimit {
		return nil, &SDKError{
			Message: fmt.Sprintf("Image size %d bytes exceeds maximum limit of %d bytes (16 MiB).", len(data), MaxImageSizeLimit),
		}
	}
	normalizedFormat, err := normalizeImageFormat(format)
	if err != nil {
		return nil, err
	}
	cloned := bytes.Clone(data)
	b64 := base64.StdEncoding.EncodeToString(cloned)
	return &Image{
		Ref:    ref,
		Format: normalizedFormat,
		Data:   cloned,
		Base64: b64,
	}, nil
}

// NewImageFromBase64 creates an image asset from a base64 string.
func NewImageFromBase64(ref, b64, format string) (*Image, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &SDKError{Message: "Image reference cannot be empty."}
	}
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, &SDKError{Message: "Image base64 data cannot be empty."}
	}
	if base64.StdEncoding.DecodedLen(len(b64)) > MaxImageSizeLimit+3 {
		return nil, &SDKError{
			Message: fmt.Sprintf("Image base64 data exceeds maximum limit of %d bytes (16 MiB).", MaxImageSizeLimit),
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, &SDKError{Message: "Invalid base64 image data", Cause: err}
	}
	return NewImageWithRef(ref, data, format)
}

var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte{0xff, 0xd8, 0xff}
)

// NewImageFromFile loads a regular image file from disk and creates an Image asset.
// When loading filenames influenced by untrusted input, prefer [NewImageFromRoot] to confine reads
// within an allowed directory.
func NewImageFromFile(path string) (*Image, error) {
	normalizedFormat, err := normalizeImageFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
	if err != nil {
		return nil, err
	}
	preInfo, err := os.Stat(path)
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", path), Cause: err}
	}
	if err := checkRegularImageInfo(preInfo, path); err != nil {
		return nil, err
	}
	// #nosec G304 -- NewImageFromFile intentionally opens a caller-supplied path; NewImageFromRoot provides os.Root confinement.
	file, err := os.Open(path)
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", path), Cause: err}
	}
	defer func() { _ = file.Close() }()
	return readImageFile(file, path, normalizedFormat)
}

// NewImageFromRoot loads a regular image file confined within root, preventing path traversal
// and symlink escapes outside the root directory.
func NewImageFromRoot(root *os.Root, name string) (*Image, error) {
	if root == nil {
		return nil, &SDKError{Message: "Image root directory cannot be nil."}
	}
	normalizedFormat, err := normalizeImageFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), "."))
	if err != nil {
		return nil, err
	}
	preInfo, err := root.Stat(name)
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", name), Cause: err}
	}
	if err := checkRegularImageInfo(preInfo, name); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", name), Cause: err}
	}
	defer func() { _ = file.Close() }()
	return readImageFile(file, name, normalizedFormat)
}

func checkRegularImageInfo(info os.FileInfo, displayPath string) error {
	if !info.Mode().IsRegular() {
		return &SDKError{Message: fmt.Sprintf("Image path %q is not a regular file.", displayPath)}
	}
	if info.Size() > MaxImageSizeLimit {
		return &SDKError{
			Message: fmt.Sprintf("Image size %d bytes exceeds maximum limit of %d bytes (16 MiB).", info.Size(), MaxImageSizeLimit),
		}
	}
	return nil
}

func readImageFile(file *os.File, displayPath, normalizedFormat string) (*Image, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to stat image file %q", displayPath), Cause: err}
	}
	if err := checkRegularImageInfo(info, displayPath); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImageSizeLimit+1))
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", displayPath), Cause: err}
	}
	if len(data) > MaxImageSizeLimit {
		return nil, &SDKError{
			Message: fmt.Sprintf("Image size %d bytes exceeds maximum limit of %d bytes (16 MiB).", len(data), MaxImageSizeLimit),
		}
	}
	if err := verifyImageMagicBytes(data, normalizedFormat); err != nil {
		return nil, err
	}
	return NewImage(data, normalizedFormat)
}

// ValidateMagicBytes reports whether img's raw bytes begin with the expected PNG or JPEG file signature.
func (img *Image) ValidateMagicBytes() error {
	if img == nil {
		return &SDKError{Message: "Image cannot be nil."}
	}
	return verifyImageMagicBytes(img.Data, img.Format)
}

func verifyImageMagicBytes(data []byte, normalizedFormat string) error {
	switch normalizedFormat {
	case "png":
		if !bytes.HasPrefix(data, pngMagic) {
			return &SDKError{Message: "Invalid PNG image data: missing PNG file signature."}
		}
	case "jpg":
		if !bytes.HasPrefix(data, jpegMagic) {
			return &SDKError{Message: "Invalid JPEG image data: missing JPEG file signature."}
		}
	}
	return nil
}

// String returns the image reference string.
func (img *Image) String() string {
	if img == nil {
		return ""
	}
	return img.Ref
}

// Asset returns the wire Asset definition for this image.
func (img *Image) Asset() Asset {
	if img == nil {
		return Asset{}
	}
	return Asset{
		Type:       "image",
		Format:     img.Format,
		DataBase64: img.Base64,
	}
}

// MarshalJSON encodes the image as its reference string, allowing Image objects to be
// embedded directly inside state, instructions, or criteria.
func (img *Image) MarshalJSON() ([]byte, error) {
	if img == nil {
		return jsonv2.Marshal("", marshalOptions)
	}
	return jsonv2.Marshal(img.Ref, marshalOptions)
}

// CollectAssets returns an Assets map containing all the given images.
func CollectAssets(images ...*Image) Assets {
	assets := make(Assets, len(images))
	for _, img := range images {
		if img != nil && img.Ref != "" {
			assets[img.Ref] = img.Asset()
		}
	}
	return assets
}

func normalizeImageFormat(format string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	switch normalized {
	case "png":
		return "png", nil
	case "jpg", "jpeg":
		return "jpg", nil
	default:
		return "", &SDKError{
			Message: fmt.Sprintf("Unsupported image format %q; only PNG and JPEG are supported.", format),
		}
	}
}

func newRandomRef(prefix string) string {
	buf := make([]byte, 0, len(prefix)+1+36)
	buf = append(buf, prefix...)
	buf = append(buf, '-')
	buf, _ = uuid.NewV4().AppendText(buf)
	return string(buf)
}

// imagePointerType is the type the walker recognizes as an image reference.
var imagePointerType = reflect.TypeFor[*Image]()

const maxFastImageWalkDepth = 8

type questionExtraCarrier interface {
	questionExtra() map[string]any
}

// collectImagesFrom walks val recursively to discover any embedded *Image instances, so an image
// placed in state, a question's instructions, or its criteria is registered in the request's assets.
// It follows pointers, interfaces, exported struct fields, maps, slices, and arrays, tolerates
// cycles in the value graph, and reports an error if two distinct assets collide on the same Ref.
func collectImagesFrom(val any, assets map[string]any) error {
	if val == nil || assets == nil {
		return nil
	}
	return collectImagesInto(val, &assets)
}

// collectImagesInto discovers embedded *Image instances into *assets, allocating the map lazily
// only when at least one *Image is encountered. Common scalar, map, slice, and built-in Question
// types take a zero-reflection fast path before falling back to reflect for arbitrary structs or
// deeply nested / cyclic graphs.
func collectImagesInto(val any, assets *map[string]any) error {
	if val == nil || assets == nil {
		return nil
	}
	var seen map[imageVisit]bool
	return collectImagesFast(val, assets, 0, &seen)
}

func registerImageAsset(image *Image, assets *map[string]any) error {
	if image == nil || image.Ref == "" {
		return nil
	}
	if *assets == nil {
		*assets = make(map[string]any, 2)
	}
	incoming := image.Asset()
	if existing, exists := (*assets)[image.Ref]; exists && !sameAssetPayload(existing, incoming) {
		return &SDKError{
			Message: fmt.Sprintf("Conflicting image assets share reference %q.", image.Ref),
		}
	}
	(*assets)[image.Ref] = incoming
	return nil
}

func collectImagesFast(val any, assets *map[string]any, depth int, seen *map[imageVisit]bool) error {
	if val == nil {
		return nil
	}
	if depth >= maxFastImageWalkDepth {
		return walkImages(reflect.ValueOf(val), assets, seen)
	}
	switch typed := val.(type) {
	case string, bool, int, int64, int32, int16, int8, uint, uint64, uint32, uint16, uint8,
		float64, float32, []byte, []string, map[string]string, map[string]int, map[string]float64, map[string]bool:
		return nil
	case *Image:
		return registerImageAsset(typed, assets)
	case []*Image:
		for _, img := range typed {
			if err := registerImageAsset(img, assets); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := collectImagesFast(item, assets, depth+1, seen); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, item := range typed {
			if err := collectImagesFast(item, assets, depth+1, seen); err != nil {
				return err
			}
		}
		return nil
	case Questions:
		for _, item := range typed {
			if err := collectImagesFast(item, assets, depth+1, seen); err != nil {
				return err
			}
		}
		return nil
	case RawQuestion:
		for _, item := range typed {
			if err := collectImagesFast(item, assets, depth+1, seen); err != nil {
				return err
			}
		}
		return nil
	case *NoulCriteria:
		if typed == nil {
			return nil
		}
		if err := collectImagesFast(typed.True, assets, depth+1, seen); err != nil {
			return err
		}
		return collectImagesFast(typed.False, assets, depth+1, seen)
	case NoulCriteria:
		if err := collectImagesFast(typed.True, assets, depth+1, seen); err != nil {
			return err
		}
		return collectImagesFast(typed.False, assets, depth+1, seen)
	case *NoulQuestion:
		if typed == nil {
			return nil
		}
		return collectNoulQuestionImages(*typed, assets, depth+1, seen)
	case NoulQuestion:
		return collectNoulQuestionImages(typed, assets, depth+1, seen)
	case *ChoiceQuestion:
		if typed == nil {
			return nil
		}
		return collectChoiceQuestionImages(*typed, assets, depth+1, seen)
	case ChoiceQuestion:
		return collectChoiceQuestionImages(typed, assets, depth+1, seen)
	case *ScoreQuestion:
		if typed == nil {
			return nil
		}
		return collectScoreQuestionImages(*typed, assets, depth+1, seen)
	case ScoreQuestion:
		return collectScoreQuestionImages(typed, assets, depth+1, seen)
	case *BoxQuestion:
		if typed == nil {
			return nil
		}
		return collectBoxQuestionImages(*typed, assets, depth+1, seen)
	case BoxQuestion:
		return collectBoxQuestionImages(typed, assets, depth+1, seen)
	default:
		return walkImages(reflect.ValueOf(val), assets, seen)
	}
}

func collectNoulQuestionImages(q NoulQuestion, assets *map[string]any, depth int, seen *map[imageVisit]bool) error {
	if err := collectImagesFast(q.Instructions, assets, depth, seen); err != nil {
		return err
	}
	if q.Criteria != nil {
		if err := collectImagesFast(q.Criteria, assets, depth, seen); err != nil {
			return err
		}
	}
	if len(q.extra) > 0 {
		return collectImagesFast(q.extra, assets, depth, seen)
	}
	return nil
}

func collectChoiceQuestionImages(q ChoiceQuestion, assets *map[string]any, depth int, seen *map[imageVisit]bool) error {
	if err := collectImagesFast(q.Criteria, assets, depth, seen); err != nil {
		return err
	}
	if err := collectImagesFast(q.Instructions, assets, depth, seen); err != nil {
		return err
	}
	if len(q.extra) > 0 {
		return collectImagesFast(q.extra, assets, depth, seen)
	}
	return nil
}

func collectScoreQuestionImages(q ScoreQuestion, assets *map[string]any, depth int, seen *map[imageVisit]bool) error {
	if err := collectImagesFast(q.Criteria, assets, depth, seen); err != nil {
		return err
	}
	if err := collectImagesFast(q.Instructions, assets, depth, seen); err != nil {
		return err
	}
	if len(q.extra) > 0 {
		return collectImagesFast(q.extra, assets, depth, seen)
	}
	return nil
}

func collectBoxQuestionImages(q BoxQuestion, assets *map[string]any, depth int, seen *map[imageVisit]bool) error {
	if err := collectImagesFast(q.Instructions, assets, depth, seen); err != nil {
		return err
	}
	if len(q.extra) > 0 {
		return collectImagesFast(q.extra, assets, depth, seen)
	}
	return nil
}

// imageVisit identifies a reference-typed value the walker has already visited, so a cyclic value
// cannot recurse forever while distinct slice windows sharing a backing array are still traversed.
type imageVisit struct {
	typ    reflect.Type
	ptr    uintptr
	length int
}

// walkImages recurses through value, collecting every reachable *Image into *assets.
func walkImages(value reflect.Value, assets *map[string]any, seen *map[imageVisit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == imagePointerType {
		if value.CanInterface() {
			if image, ok := reflect.TypeAssert[*Image](value); ok {
				return registerImageAsset(image, assets)
			}
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if !value.IsNil() {
			return walkImages(value.Elem(), assets, seen)
		}
	case reflect.Pointer:
		if !value.IsNil() && !alreadySeen(value, seen) {
			return walkImages(value.Elem(), assets, seen)
		}
	case reflect.Map:
		if value.IsNil() || alreadySeen(value, seen) {
			return nil
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := walkImages(iterator.Value(), assets, seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		// Byte slices are data, not containers of images; skipping them also avoids walking large
		// payloads such as jsontext.Value.
		if value.IsNil() || value.Type().Elem().Kind() == reflect.Uint8 || alreadySeen(value, seen) {
			return nil
		}
		for index := range value.Len() {
			if err := walkImages(value.Index(index), assets, seen); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := range value.Len() {
			if err := walkImages(value.Index(index), assets, seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typ := value.Type()
		if value.CanInterface() {
			if carrier, ok := reflect.TypeAssert[questionExtraCarrier](value); ok {
				if extra := carrier.questionExtra(); len(extra) > 0 {
					if err := walkImages(reflect.ValueOf(extra), assets, seen); err != nil {
						return err
					}
				}
			}
		}
		numFields := typ.NumField()
		for i := range numFields {
			structField := typ.Field(i)
			// Unexported fields and fields tagged `json:"-"` are not part of the JSON wire form.
			if structField.IsExported() && !jsonIgnored(structField) {
				if err := walkImages(value.Field(i), assets, seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func sameAssetPayload(existing any, incoming Asset) bool {
	switch typed := existing.(type) {
	case Asset:
		return typed == incoming
	case *Asset:
		return typed != nil && *typed == incoming
	case map[string]any:
		t, _ := typed["type"].(string)
		f, _ := typed["format"].(string)
		d, _ := typed["data_base64"].(string)
		p, _ := typed["part"].(string)
		return t == incoming.Type && f == incoming.Format && d == incoming.DataBase64 && p == incoming.Part
	default:
		return false
	}
}

// jsonIgnored reports whether a struct field is excluded from the JSON wire form.
func jsonIgnored(field reflect.StructField) bool {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name == "-"
}

// alreadySeen records a reference-typed value and reports whether it was visited before.
func alreadySeen(value reflect.Value, seen *map[imageVisit]bool) bool {
	if *seen == nil {
		*seen = make(map[imageVisit]bool, 8)
	}
	length := 0
	if value.Kind() == reflect.Slice {
		length = value.Len()
	}
	key := imageVisit{typ: value.Type(), ptr: value.Pointer(), length: length}
	if (*seen)[key] {
		return true
	}
	(*seen)[key] = true
	return false
}
