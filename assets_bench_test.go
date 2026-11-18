package typesafe

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

var (
	benchSinkImage  *Image
	benchSinkAssets map[string]any
)

func benchPNGBytes(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(0x9e3779b9)
	for y := range height {
		for x := range width {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(state & 0xff),
				G: uint8((state >> 8) & 0xff),
				B: uint8((state >> 16) & 0xff),
				A: 0xff,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// BenchmarkNewImage measures image asset creation including cryptographic random reference
// generation, defensive slice cloning, and standard base64 encoding.
func BenchmarkNewImage(b *testing.B) {
	for _, dim := range []int{32, 128, 256} {
		raw := benchPNGBytes(dim, dim)
		b.Run(fmt.Sprintf("dim=%dx%d", dim, dim), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				img, err := NewImage(raw, "png")
				if err != nil {
					b.Fatalf("NewImage() error = %v", err)
				}
				benchSinkImage = img
			}
		})
	}
}

// BenchmarkNewImageWithRef isolates image validation, defensive cloning, and base64 encoding
// without crypto/rand reference generation.
func BenchmarkNewImageWithRef(b *testing.B) {
	for _, dim := range []int{32, 128, 256} {
		raw := benchPNGBytes(dim, dim)
		b.Run(fmt.Sprintf("dim=%dx%d", dim, dim), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				img, err := NewImageWithRef("img-fixed-ref", raw, "png")
				if err != nil {
					b.Fatalf("NewImageWithRef() error = %v", err)
				}
				benchSinkImage = img
			}
		})
	}
}

// BenchmarkNewImageFromRoot measures root-confined filesystem image loading, regular-file checks,
// magic-byte verification, and base64 asset construction.
func BenchmarkNewImageFromRoot(b *testing.B) {
	dir := b.TempDir()
	raw := benchPNGBytes(128, 128)
	const fileName = "sample.png"
	if err := os.WriteFile(filepath.Join(dir, fileName), raw, 0o600); err != nil {
		b.Fatalf("os.WriteFile() error = %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatalf("os.OpenRoot() error = %v", err)
	}
	b.Cleanup(func() { _ = root.Close() })

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		img, err := NewImageFromRoot(root, fileName)
		if err != nil {
			b.Fatalf("NewImageFromRoot() error = %v", err)
		}
		benchSinkImage = img
	}
}

// BenchmarkCollectImagesFrom measures the recursive reflection walker that discovers *Image values
// across state maps, slices, structs, and Question extra fields.
func BenchmarkCollectImagesFrom(b *testing.B) {
	img1, err := NewImageWithRef("img-1", benchPNGBytes(32, 32), "png")
	if err != nil {
		b.Fatalf("NewImageWithRef(1) error = %v", err)
	}
	img2, err := NewImageWithRef("img-2", benchPNGBytes(32, 32), "png")
	if err != nil {
		b.Fatalf("NewImageWithRef(2) error = %v", err)
	}
	img3, err := NewImageWithRef("img-3", benchPNGBytes(32, 32), "png")
	if err != nil {
		b.Fatalf("NewImageWithRef(3) error = %v", err)
	}

	b.Run("NoImages", func(b *testing.B) {
		state := map[string]any{
			"ticket_id": "TCK-1001",
			"messages":  []string{"First message", "Second message", "Third message"},
			"metadata":  map[string]any{"plan": "enterprise", "seats": 42},
		}
		b.ReportAllocs()
		for b.Loop() {
			assets := make(map[string]any)
			if err := collectImagesFrom(state, assets); err != nil {
				b.Fatalf("collectImagesFrom(NoImages) error = %v", err)
			}
			benchSinkAssets = assets
		}
	})

	b.Run("StateAndQuestions", func(b *testing.B) {
		payload := map[string]any{
			"state": map[string]any{
				"primary_photo": img1,
				"gallery":       []*Image{img2},
				"raw_blob":      make([]byte, 4096), // skipped as []uint8 fast path
			},
			"questions": map[string]any{
				"has_damage": NewNoul(
					"Is visible damage present?",
					WithQuestionField("reference_image", img3),
				),
				"damage_box": NewBox("the damaged region"),
			},
		}
		b.ReportAllocs()
		for b.Loop() {
			assets := make(map[string]any, 3)
			if err := collectImagesFrom(payload, assets); err != nil {
				b.Fatalf("collectImagesFrom(StateAndQuestions) error = %v", err)
			}
			benchSinkAssets = assets
		}
	})
}
