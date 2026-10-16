package typesafe

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageCreationAndFormatNormalization(t *testing.T) {
	t.Parallel()

	data := []byte("\x89PNG\r\n\x1a\nfake-png-content")

	img, err := NewImage(data, "PNG")
	if err != nil {
		t.Fatalf("NewImage(PNG) error: %v", err)
	}
	if img.Format != "png" {
		t.Errorf("Format = %q, want png", img.Format)
	}
	if !strings.HasPrefix(img.Ref, "img-") {
		t.Errorf("Ref = %q, want prefix img-", img.Ref)
	}
	if img.Base64 != base64.StdEncoding.EncodeToString(data) {
		t.Errorf("Base64 mismatch")
	}

	jpegImg, err := NewImageWithRef("ref-custom", data, "JPEG")
	if err != nil {
		t.Fatalf("NewImageWithRef(JPEG) error: %v", err)
	}
	if jpegImg.Format != "jpg" {
		t.Errorf("Format = %q, want jpg", jpegImg.Format)
	}
	if jpegImg.Ref != "ref-custom" {
		t.Errorf("Ref = %q, want ref-custom", jpegImg.Ref)
	}

	b64Text := base64.StdEncoding.EncodeToString(data)
	fromB64, err := NewImageFromBase64("ref-b64", b64Text, "jpg")
	if err != nil {
		t.Fatalf("NewImageFromBase64 error: %v", err)
	}
	if fromB64.Ref != "ref-b64" || fromB64.Format != "jpg" {
		t.Errorf("fromB64 mismatch: %+v", fromB64)
	}
}

func TestImageValidationErrors(t *testing.T) {
	t.Parallel()

	data := []byte("some-bytes")

	// Unsupported format
	_, err := NewImage(data, "gif")
	if err == nil || !strings.Contains(err.Error(), "Unsupported image format") {
		t.Errorf("expected unsupported format error, got %v", err)
	}

	// Empty data
	_, err = NewImage(nil, "png")
	if err == nil || !strings.Contains(err.Error(), "Image data cannot be empty") {
		t.Errorf("expected empty data error, got %v", err)
	}

	// Empty ref
	_, err = NewImageWithRef("  ", data, "png")
	if err == nil || !strings.Contains(err.Error(), "Image reference cannot be empty") {
		t.Errorf("expected empty reference error, got %v", err)
	}

	// Over 16 MiB
	tooBig := make([]byte, MaxImageSizeLimit+1)
	_, err = NewImage(tooBig, "png")
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum limit") {
		t.Errorf("expected size limit error, got %v", err)
	}
}

func TestImageFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, []byte("fake-photo-data"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	img, err := NewImageFromFile(path)
	if err != nil {
		t.Fatalf("NewImageFromFile failed: %v", err)
	}
	if img.Format != "jpg" {
		t.Errorf("Format = %q, want jpg", img.Format)
	}

	_, err = NewImageFromFile(filepath.Join(dir, "non-existent.png"))
	if err == nil {
		t.Error("expected error for non-existent file, got nil")
	}
}

func TestImageMarshalJSONAndCollectAssets(t *testing.T) {
	t.Parallel()

	data := []byte("image-data")
	img, err := NewImageWithRef("ref-img-1", data, "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}

	encoded, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if string(encoded) != `"ref-img-1"` {
		t.Errorf("MarshalJSON = %s, want %q", encoded, "ref-img-1")
	}

	assets := CollectAssets(img)
	if len(assets) != 1 {
		t.Fatalf("len(assets) = %d, want 1", len(assets))
	}
	asset := assets["ref-img-1"]
	if asset.Type != "image" || asset.Format != "png" || asset.DataBase64 != base64.StdEncoding.EncodeToString(data) {
		t.Errorf("Asset mismatch: %+v", asset)
	}
}

func TestSystemOneWithAssetsAndAutoCollection(t *testing.T) {
	t.Parallel()

	data := []byte("image-data")
	img, err := NewImageWithRef("ref-dog", data, "jpg")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}

	client, transport := newMockClient(t, func(req *http.Request, _ int) *http.Response {
		return JSONResponse(http.StatusOK, `{"model":"jev-preview","usage":{"input_tokens":10,"output_tokens":2},"answers":{"q":{"type":"noul","noul":0.99}}}`)
	}, WithImages(true))

	// State with embedded *Image
	state := map[string]any{
		"photo": img,
	}

	_, err = client.SystemOne(t.Context(), state, Questions{"q": NewNoul("Is this a dog?")})
	if err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}

	call := transport.lastCall(t)
	if call.Header.Get(HeaderDangerZoneImages) != "true" {
		t.Errorf("missing %s header", HeaderDangerZoneImages)
	}

	var recordedBody map[string]any
	if err := json.Unmarshal(call.Body, &recordedBody); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	assetsMap, ok := recordedBody["assets"].(map[string]any)
	if !ok {
		t.Fatalf("assets = %v, want map[string]any", recordedBody["assets"])
	}
	dogAsset, ok := assetsMap["ref-dog"].(map[string]any)
	if !ok {
		t.Fatalf("assets[ref-dog] = %v, want map", assetsMap["ref-dog"])
	}
	if dogAsset["type"] != "image" || dogAsset["format"] != "jpg" {
		t.Errorf("dogAsset = %+v", dogAsset)
	}
}
