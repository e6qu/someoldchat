package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestGenerateDownscalesAndPreservesAspect(t *testing.T) {
	data := encodePNG(t, 800, 600)
	thumb, mime, err := Generate(data, 360)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("a PNG source produced %q, want image/png so transparency survives", mime)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "png" {
		t.Fatalf("thumbnail format %q, want png", format)
	}
	// The long side is bounded to 360 and the aspect ratio (4:3) is preserved.
	if config.Width != 360 {
		t.Errorf("thumbnail width %d, want 360", config.Width)
	}
	if config.Height != 270 {
		t.Errorf("thumbnail height %d, want 270 (4:3 of a 360 width)", config.Height)
	}
	if len(thumb) >= len(data) {
		t.Errorf("thumbnail (%d bytes) is not smaller than the source (%d bytes)", len(thumb), len(data))
	}
}

func TestGenerateReencodesJPEGAsJPEG(t *testing.T) {
	thumb, mime, err := Generate(encodeJPEG(t, 1000, 1000), 200)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("a JPEG source produced %q, want image/jpeg", mime)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 200 || config.Height != 200 {
		t.Errorf("square thumbnail is %dx%d, want 200x200", config.Width, config.Height)
	}
}

func TestGenerateBoundsTinyImagesToAtLeastOnePixel(t *testing.T) {
	// A very wide, one-pixel-tall image must not round its short side to zero.
	thumb, _, err := Generate(encodePNG(t, 900, 1), 100)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 100 || config.Height != 1 {
		t.Errorf("thumbnail is %dx%d, want 100x1", config.Width, config.Height)
	}
}

func TestGenerateRejectsNonImages(t *testing.T) {
	if _, _, err := Generate([]byte("this is not an image"), 360); err != ErrUnsupported {
		t.Fatalf("non-image error = %v, want ErrUnsupported", err)
	}
}

func TestSupported(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif"} {
		if !Supported(mime) {
			t.Errorf("Supported(%q) = false, want true", mime)
		}
	}
	for _, mime := range []string{"application/pdf", "text/plain", "image/svg+xml", ""} {
		if Supported(mime) {
			t.Errorf("Supported(%q) = true, want false", mime)
		}
	}
}
