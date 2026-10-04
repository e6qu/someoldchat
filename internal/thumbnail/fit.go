package thumbnail

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// MaxAnimationFrames bounds an animated GIF Fit will resize. An emoji's
// animation is a few dozen frames; a file claiming thousands is refused rather
// than decoded and re-encoded frame by frame.
const MaxAnimationFrames = 500

// Fit scales an image down so neither side exceeds maxDim, preserving aspect
// ratio and keeping its format: a PNG stays a PNG with its transparency, a
// JPEG stays a JPEG, and an animated GIF stays animated, each frame scaled
// with its timing kept. An image already within maxDim is returned as it was,
// byte for byte, and resized reports false.
//
// It returns ErrUnsupported for a format other than those three, for bytes
// that do not decode, and for a source past MaxSourcePixels (summed over a
// GIF's frames) or MaxAnimationFrames.
func Fit(data []byte, mimeType string, maxDim int) ([]byte, bool, error) {
	if maxDim <= 0 || !Supported(mimeType) {
		return nil, false, ErrUnsupported
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > MaxSourcePixels {
		return nil, false, ErrUnsupported
	}
	if "image/"+format != mimeType {
		return nil, false, ErrUnsupported
	}
	if config.Width <= maxDim && config.Height <= maxDim {
		return data, false, nil
	}
	var out bytes.Buffer
	switch format {
	case "gif":
		animation, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil || len(animation.Image) == 0 || len(animation.Image) > MaxAnimationFrames ||
			int64(config.Width)*int64(config.Height)*int64(len(animation.Image)) > MaxSourcePixels {
			return nil, false, ErrUnsupported
		}
		if err := gif.EncodeAll(&out, fitAnimation(animation, maxDim)); err != nil {
			return nil, false, err
		}
	case "png":
		source, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, false, ErrUnsupported
		}
		if err := png.Encode(&out, downscale(source, maxDim)); err != nil {
			return nil, false, err
		}
	default:
		source, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, false, ErrUnsupported
		}
		if err := jpeg.Encode(&out, downscale(source, maxDim), &jpeg.Options{Quality: 90}); err != nil {
			return nil, false, err
		}
	}
	return out.Bytes(), true, nil
}

// fitAnimation scales every frame of an animation. A GIF frame is a patch
// drawn over what the previous frames left, as its disposal method says, so
// each frame is composed onto a full canvas first and the whole canvas is
// scaled; the scaled frames are then complete pictures that replace one
// another. Timing and looping are kept.
func fitAnimation(animation *gif.GIF, maxDim int) *gif.GIF {
	bounds := image.Rect(0, 0, animation.Config.Width, animation.Config.Height)
	canvas := image.NewRGBA(bounds)
	var saved *image.RGBA
	result := &gif.GIF{LoopCount: animation.LoopCount}
	for index, frame := range animation.Image {
		disposal := byte(gif.DisposalNone)
		if index < len(animation.Disposal) {
			disposal = animation.Disposal[index]
		}
		if disposal == gif.DisposalPrevious {
			saved = image.NewRGBA(bounds)
			draw.Draw(saved, bounds, canvas, image.Point{}, draw.Src)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		scaled := downscale(canvas, maxDim)
		paletted := image.NewPaletted(scaled.Bounds(), framePalette(frame))
		draw.FloydSteinberg.Draw(paletted, scaled.Bounds(), scaled, scaled.Bounds().Min)
		result.Image = append(result.Image, paletted)
		delay := 0
		if index < len(animation.Delay) {
			delay = animation.Delay[index]
		}
		result.Delay = append(result.Delay, delay)
		// Every scaled frame is a whole picture, so nothing needs disposing.
		result.Disposal = append(result.Disposal, gif.DisposalNone)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if saved != nil {
				canvas = saved
			}
		}
	}
	scaledBounds := result.Image[0].Bounds()
	result.Config = image.Config{ColorModel: result.Image[0].Palette, Width: scaledBounds.Dx(), Height: scaledBounds.Dy()}
	return result
}

// framePalette is the frame's own palette when it can carry a transparent
// colour, which a composed frame needs where nothing has been drawn yet, and
// a general web palette otherwise.
func framePalette(frame *image.Paletted) color.Palette {
	for _, entry := range frame.Palette {
		if _, _, _, alpha := entry.RGBA(); alpha == 0 {
			return frame.Palette
		}
	}
	if len(frame.Palette) < 256 {
		return append(append(color.Palette{}, frame.Palette...), color.Transparent)
	}
	return append(append(color.Palette{}, palette.WebSafe...), color.Transparent)
}
