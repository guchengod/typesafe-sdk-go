package typesafe

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	b64 := base64.StdEncoding.EncodeToString(data)
	return &Image{
		Ref:    ref,
		Format: normalizedFormat,
		Data:   data,
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
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, &SDKError{Message: "Invalid base64 image data", Cause: err}
	}
	return NewImageWithRef(ref, data, format)
}

// NewImageFromFile loads an image from disk and creates an Image asset.
func NewImageFromFile(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &SDKError{Message: fmt.Sprintf("Failed to read image file %q", path), Cause: err}
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	return NewImage(data, ext)
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
		return json.Marshal("")
	}
	return json.Marshal(img.Ref)
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
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%08x-%04x-%04x-%04x-%012x", prefix, b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// collectImagesFrom walks val recursively to discover any embedded *Image instances.
func collectImagesFrom(val any, assets map[string]any) {
	if val == nil || assets == nil {
		return
	}
	switch v := val.(type) {
	case *Image:
		if v != nil && v.Ref != "" {
			assets[v.Ref] = v.Asset()
		}
	case []any:
		for _, item := range v {
			collectImagesFrom(item, assets)
		}
	case []*Image:
		for _, img := range v {
			if img != nil && img.Ref != "" {
				assets[img.Ref] = img.Asset()
			}
		}
	case map[string]any:
		for _, item := range v {
			collectImagesFrom(item, assets)
		}
	case map[string]*Image:
		for _, img := range v {
			if img != nil && img.Ref != "" {
				assets[img.Ref] = img.Asset()
			}
		}
	}
}
