package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodedPNG(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A PNG larger than the box is scaled into it with its aspect ratio and its
// transparency kept, and stays a PNG.
func TestFitScalesAPNGAndKeepsItsTransparency(t *testing.T) {
	source := encodedPNG(t, 300, 150, color.NRGBA{R: 200, A: 0})
	fitted, resized, err := Fit(source, "image/png", 128)
	if err != nil || !resized {
		t.Fatalf("resized=%v err=%v", resized, err)
	}
	img, format, err := image.Decode(bytes.NewReader(fitted))
	if err != nil || format != "png" {
		t.Fatalf("format=%q err=%v", format, err)
	}
	if bounds := img.Bounds(); bounds.Dx() != 128 || bounds.Dy() != 64 {
		t.Fatalf("size=%v, want 128x64", bounds.Size())
	}
	if _, _, _, alpha := img.At(10, 10).RGBA(); alpha != 0 {
		t.Fatalf("alpha=%d, want the transparency kept", alpha)
	}
}

// A JPEG stays a JPEG, scaled into the box.
func TestFitScalesAJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 512))
	var source bytes.Buffer
	if err := jpeg.Encode(&source, img, nil); err != nil {
		t.Fatal(err)
	}
	fitted, resized, err := Fit(source.Bytes(), "image/jpeg", 128)
	if err != nil || !resized {
		t.Fatalf("resized=%v err=%v", resized, err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(fitted))
	if err != nil || format != "jpeg" || config.Width != 64 || config.Height != 128 {
		t.Fatalf("format=%q size=%dx%d err=%v", format, config.Width, config.Height, err)
	}
}

// An image already within the box is returned exactly as it was.
func TestFitLeavesASmallImageUntouched(t *testing.T) {
	source := encodedPNG(t, 64, 128, color.NRGBA{G: 255, A: 255})
	fitted, resized, err := Fit(source, "image/png", 128)
	if err != nil || resized || !bytes.Equal(fitted, source) {
		t.Fatalf("resized=%v err=%v, want the same bytes back", resized, err)
	}
}

// An animated GIF stays animated: every frame is scaled, its timing and
// looping kept, and a frame that patches only part of the picture is
// composed over what came before it rather than scaled on its own.
func TestFitScalesEveryFrameOfAnAnimation(t *testing.T) {
	palette := color.Palette{color.Transparent, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	first := image.NewPaletted(image.Rect(0, 0, 256, 256), palette)
	for i := range first.Pix {
		first.Pix[i] = 1
	}
	// The second frame repaints only the top-left corner.
	second := image.NewPaletted(image.Rect(0, 0, 64, 64), palette)
	for i := range second.Pix {
		second.Pix[i] = 2
	}
	var source bytes.Buffer
	if err := gif.EncodeAll(&source, &gif.GIF{
		Image: []*image.Paletted{first, second}, Delay: []int{10, 25}, LoopCount: 0,
		Disposal: []byte{gif.DisposalNone, gif.DisposalNone},
		Config:   image.Config{ColorModel: palette, Width: 256, Height: 256},
	}); err != nil {
		t.Fatal(err)
	}
	fitted, resized, err := Fit(source.Bytes(), "image/gif", 128)
	if err != nil || !resized {
		t.Fatalf("resized=%v err=%v", resized, err)
	}
	animation, err := gif.DecodeAll(bytes.NewReader(fitted))
	if err != nil {
		t.Fatal(err)
	}
	if len(animation.Image) != 2 || animation.Delay[0] != 10 || animation.Delay[1] != 25 || animation.LoopCount != 0 {
		t.Fatalf("frames=%d delays=%v loop=%d", len(animation.Image), animation.Delay, animation.LoopCount)
	}
	if animation.Config.Width != 128 || animation.Config.Height != 128 {
		t.Fatalf("size=%dx%d", animation.Config.Width, animation.Config.Height)
	}
	frame := animation.Image[1]
	if frame.Bounds().Dx() != 128 {
		t.Fatalf("the second frame is %v, want a whole scaled picture", frame.Bounds())
	}
	corner, rest := frame.At(5, 5), frame.At(100, 100)
	if r, _, b, _ := corner.RGBA(); b == 0 || r != 0 {
		t.Fatalf("the repainted corner reads %v, want blue", corner)
	}
	if r, _, b, _ := rest.RGBA(); r == 0 || b != 0 {
		t.Fatalf("the untouched rest reads %v, want the first frame's red kept", rest)
	}
}

// Bytes that are not the declared image, or not an image, are refused.
func TestFitRefusesWhatItCannotScale(t *testing.T) {
	source := encodedPNG(t, 300, 300, color.White)
	for name, item := range map[string]struct {
		data     []byte
		mimeType string
	}{
		"a PNG declared a JPEG": {source, "image/jpeg"},
		"not an image":          {[]byte("hello"), "image/png"},
		"an unsupported type":   {source, "image/webp"},
	} {
		if _, _, err := Fit(item.data, item.mimeType, 128); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: err=%v, want ErrUnsupported", name, err)
		}
	}
}
