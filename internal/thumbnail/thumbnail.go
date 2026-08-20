// Package thumbnail derives a small preview of an uploaded image so a timeline
// can show a lightweight version and fetch the full bytes only when someone opens
// it. It leans on nothing outside the standard library: the decoders are the ones
// image.Decode already registers, and the downscale is a plain box filter, so no
// image-processing dependency is admitted for a feature this small.
package thumbnail

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	// Registers the PNG, JPEG and GIF decoders with image.Decode/DecodeConfig.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// ErrUnsupported is returned for bytes that are not an image this package will
// thumbnail — an unknown format, or one whose declared dimensions are absurd.
var ErrUnsupported = errors.New("thumbnail: unsupported or unsafe image")

// MaxSourcePixels bounds the decoded image so a small file that claims enormous
// dimensions — a decompression bomb — cannot make the server allocate gigabytes
// decoding it. 40 megapixels is well past any real photograph a chat carries.
const MaxSourcePixels = 40 * 1000 * 1000

// Supported reports whether a mime type is one Generate can thumbnail. The
// caller uses it to skip the decode entirely for non-images.
func Supported(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif":
		return true
	default:
		return false
	}
}

// Generate downscales data so neither side exceeds maxDim, preserving aspect
// ratio, and re-encodes it. A PNG source stays a PNG so transparency survives;
// everything else becomes a JPEG, which is far smaller for a photograph. An image
// already within maxDim on both sides is still re-encoded, so the output is
// always a clean, self-consistent thumbnail rather than the original bytes.
// It returns ErrUnsupported for a format it does not handle or a source larger
// than MaxSourcePixels, so the caller can simply skip the thumbnail and fall back
// to the full image.
func Generate(data []byte, maxDim int) ([]byte, string, error) {
	if maxDim <= 0 {
		return nil, "", fmt.Errorf("thumbnail: maxDim must be positive, got %d", maxDim)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > MaxSourcePixels {
		return nil, "", ErrUnsupported
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	scaled := downscale(source, maxDim)

	var out bytes.Buffer
	if format == "png" {
		if err := png.Encode(&out, scaled); err != nil {
			return nil, "", err
		}
		return out.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: 80}); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "image/jpeg", nil
}

// downscale box-filters source down so neither dimension exceeds maxDim. An image
// already small enough is returned scaled by 1 — copied into an RGBA so the
// encoders get a concrete, alpha-capable image regardless of the source type. A
// box filter (averaging every source pixel that maps into a target pixel) is not
// the sharpest resampler, but it is honest at a thumbnail's size, needs no
// dependency, and never reads outside the source bounds.
func downscale(source image.Image, maxDim int) image.Image {
	bounds := source.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dstW, dstH := srcW, srcH
	if srcW > maxDim || srcH > maxDim {
		if srcW >= srcH {
			dstW = maxDim
			dstH = srcH * maxDim / srcW
		} else {
			dstH = maxDim
			dstW = srcW * maxDim / srcH
		}
	}
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	if dstW == srcW && dstH == srcH {
		dst := image.NewRGBA(image.Rect(0, 0, srcW, srcH))
		draw.Draw(dst, dst.Bounds(), source, bounds.Min, draw.Src)
		return dst
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		y0 := bounds.Min.Y + dy*srcH/dstH
		y1 := bounds.Min.Y + (dy+1)*srcH/dstH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dstW; dx++ {
			x0 := bounds.Min.X + dx*srcW/dstW
			x1 := bounds.Min.X + (dx+1)*srcW/dstW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var rSum, gSum, bSum, aSum, count uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, a := source.At(sx, sy).RGBA()
					rSum += uint64(r)
					gSum += uint64(g)
					bSum += uint64(b)
					aSum += uint64(a)
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			// RGBA() returns alpha-premultiplied 16-bit channels; shift the average
			// back to 8-bit for the destination image.
			dst.SetRGBA(dx, dy, color.RGBA{
				R: uint8((rSum / count) >> 8),
				G: uint8((gSum / count) >> 8),
				B: uint8((bSum / count) >> 8),
				A: uint8((aSum / count) >> 8),
			})
		}
	}
	return dst
}
