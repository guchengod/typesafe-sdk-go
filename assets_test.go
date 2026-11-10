package typesafe

import (
	"encoding/base64"
	jsonv2 "encoding/json/v2"
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

	encoded, err := jsonv2.Marshal(img, marshalOptions)
	if err != nil {
		t.Fatalf("jsonv2.Marshal failed: %v", err)
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
	if err := jsonv2.Unmarshal(call.Body, &recordedBody); err != nil {
		t.Fatalf("jsonv2.Unmarshal failed: %v", err)
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

// TestSystemOneCollectsImagesFromQuestions covers the Jev 1.14 rule that an image may appear in a
// question's instructions or criteria, not only in state.
func TestSystemOneCollectsImagesFromQuestions(t *testing.T) {
	t.Parallel()

	instructionsImage, err := NewImageWithRef("ref-instructions", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	criteriaImage, err := NewImageWithRef("ref-criteria", []byte("more-image-data"), "jpg")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}

	const answerBody = `{"model":"jev-1.14.0","usage":{"input_tokens":10,"output_tokens":2},"answers":{
		"noul":{"type":"noul","noul":0.9},
		"choice":{"type":"choice","choice":"dog","confidence":0.8,"probabilities":{"dog":0.8,"cat":0.2}},
		"score":{"type":"score","score":1,"confidence":0.8,"legend":{"0":"low","1":"high"},"probabilities":{"0":0.5,"1":0.5}}}}`
	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, answerBody)
	})

	questions := Questions{
		"noul": NewNoul(map[string]any{"ask": "What is in this image?", "image": instructionsImage}),
		"choice": NewChoice(
			map[string]any{"dog": criteriaImage, "cat": nil},
			WithInstructions("Which animal is shown?"),
		),
		"score": NewScore(
			[]any{"low", map[string]any{"image": criteriaImage}},
			WithInstructions("How prominent is the subject?"),
		),
	}

	if _, err := client.SystemOne(t.Context(), "plain text state", questions); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}

	call := transport.lastCall(t)
	if call.Header.Get(HeaderDangerZoneImages) != "true" {
		t.Errorf("missing automatically enabled %s header", HeaderDangerZoneImages)
	}

	assetsMap, ok := decodingBody(t, call)["assets"].(map[string]any)
	if !ok {
		t.Fatal("assets missing from the request body")
	}
	for _, ref := range []string{"ref-instructions", "ref-criteria"} {
		asset, ok := assetsMap[ref].(map[string]any)
		if !ok {
			t.Errorf("assets[%q] = %v, want a map", ref, assetsMap[ref])
			continue
		}
		if asset["type"] != "image" {
			t.Errorf("assets[%q].type = %v, want image", ref, asset["type"])
		}
	}
}

// TestSystemOneCollectsImagesFromStructState covers state values that are structs or pointers to
// structs, which the earlier type-switch walker did not descend into.
func TestSystemOneCollectsImagesFromStructState(t *testing.T) {
	t.Parallel()

	image, err := NewImageWithRef("ref-struct", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	hidden, err := NewImageWithRef("ref-hidden", []byte("hidden-image"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	type inner struct {
		Photo  *Image `json:"photo"`
		Hidden *Image `json:"-"`
	}
	state := struct {
		Inner *inner `json:"inner"`
	}{Inner: &inner{Photo: image, Hidden: hidden}}

	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{"model":"jev-1.14.0","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":{"type":"noul","noul":0.5}}}`)
	})

	if _, err := client.SystemOne(t.Context(), state, Questions{"q": NewNoul("Is there a photo?")}); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	assetsMap, ok := decodingBody(t, transport.lastCall(t))["assets"].(map[string]any)
	if !ok {
		t.Fatal("assets missing from the request body")
	}
	if _, ok := assetsMap["ref-struct"]; !ok {
		t.Errorf("assets = %v, want ref-struct", assetsMap)
	}
	if _, ok := assetsMap["ref-hidden"]; ok {
		t.Errorf("assets = %v, want ref-hidden excluded by json:\"-\"", assetsMap)
	}
}

// TestSystemOneDoesNotOverrideExplicitImagesHeader checks that WithCallImages(false) still wins over
// the automatic header, while the assets themselves stay in the body.
func TestSystemOneDoesNotOverrideExplicitImagesHeader(t *testing.T) {
	t.Parallel()

	image, err := NewImageWithRef("ref-disabled", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{"model":"jev-1.14.0","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":{"type":"noul","noul":0.5}}}`)
	})

	if _, err := client.SystemOne(
		t.Context(),
		map[string]any{"photo": image},
		Questions{"q": NewNoul("Is this a photo?")},
		WithCallImages(false),
	); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call := transport.lastCall(t)
	if header := call.Header.Get(HeaderDangerZoneImages); header != "" {
		t.Errorf("%s = %q after WithCallImages(false), want empty", HeaderDangerZoneImages, header)
	}
	if _, ok := decodingBody(t, call)["assets"].(map[string]any); !ok {
		t.Error("assets missing from the request body")
	}
}

// TestSystemOneCollectsImagesFromExtraBodyState covers the overlay running before asset collection:
// an image in a state supplied through WithExtraBody must still become an asset.
func TestSystemOneCollectsImagesFromExtraBodyState(t *testing.T) {
	t.Parallel()

	image, err := NewImageWithRef("ref-extra-body", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	client, transport := newMockClient(t, func(*http.Request, int) *http.Response {
		return JSONResponse(http.StatusOK, `{"model":"jev-1.14.0","usage":{"input_tokens":1,"output_tokens":1},"answers":{"q":{"type":"noul","noul":0.5}}}`)
	})

	if _, err := client.SystemOne(
		t.Context(),
		"placeholder state",
		Questions{"q": NewNoul("Is there a photo?")},
		WithExtraBody(map[string]any{"state": map[string]any{"photo": image}}),
	); err != nil {
		t.Fatalf("SystemOne failed: %v", err)
	}
	call := transport.lastCall(t)
	if header := call.Header.Get(HeaderDangerZoneImages); header != "true" {
		t.Errorf("%s = %q, want true", HeaderDangerZoneImages, header)
	}
	assetsMap, ok := decodingBody(t, call)["assets"].(map[string]any)
	if !ok {
		t.Fatal("assets missing from the request body")
	}
	if _, ok := assetsMap["ref-extra-body"]; !ok {
		t.Errorf("assets = %v, want ref-extra-body", assetsMap)
	}
}

// TestCollectImagesFromHandlesCycles ensures a self-referential value cannot recurse forever.
func TestCollectImagesFromHandlesCycles(t *testing.T) {
	t.Parallel()

	image, err := NewImageWithRef("ref-cycle", []byte("image-data"), "png")
	if err != nil {
		t.Fatalf("NewImageWithRef failed: %v", err)
	}
	type cyclic struct {
		Self  *cyclic
		Photo *Image
	}
	value := &cyclic{Photo: image}
	value.Self = value

	cyclicMap := map[string]any{"photo": image}
	cyclicMap["self"] = cyclicMap

	assets := map[string]any{}
	collectImagesFrom(value, assets)
	collectImagesFrom(cyclicMap, assets)
	if _, ok := assets["ref-cycle"]; !ok {
		t.Errorf("assets = %v, want ref-cycle", assets)
	}
}
