package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// ErrTooManyPixels means the image is larger than allowed; it is rejected before decoding.
var ErrTooManyPixels = errors.New("image has too many pixels")

// Longest side and JPEG quality of each stored variant (docs/DATABASE.md §4).
const (
	previewSize     = 1280
	thumbSize       = 320
	originalQuality = 90
	previewQuality  = 85
	thumbQuality    = 80
)

// Limits are the configured limits checked before an image is decoded.
type Limits struct {
	// MaxPixels is the largest allowed width × height.
	MaxPixels int64
}

// Variant is one stored JPEG.
type Variant struct {
	Data          []byte
	Width, Height int
}

// Result holds the three variants of a processed photo.
type Result struct {
	Source   Format
	Original Variant
	Preview  Variant
	Thumb    Variant
}

// Process checks the file and produces the original, preview and thumbnail as new JPEG files. The
// pixel count is checked from the file header first, so oversized images are never decoded.
func Process(data []byte, limits Limits) (Result, error) {
	format, err := Detect(data)
	if err != nil {
		return Result{}, err
	}
	if err := checkSize(format, data, limits); err != nil {
		return Result{}, err
	}
	decoded, err := decode(format, data)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUnsupportedType, err)
	}
	pixels := flatten(decoded)
	if format == JPEG {
		pixels = orient(pixels, jpegOrientation(data))
	}
	return variants(format, pixels)
}

// checkSize reads only the header of the file and rejects images above the pixel limit.
func checkSize(format Format, data []byte, limits Limits) error {
	cfg, err := decodeConfig(format, data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsupportedType, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return ErrUnsupportedType
	}
	if int64(cfg.Width)*int64(cfg.Height) > limits.MaxPixels {
		return ErrTooManyPixels
	}
	return nil
}

func variants(format Format, pixels *image.RGBA) (Result, error) {
	var err error
	result := Result{Source: format}
	if result.Original, err = encode(pixels, originalQuality); err != nil {
		return Result{}, err
	}
	if result.Preview, err = encode(fit(pixels, previewSize), previewQuality); err != nil {
		return Result{}, err
	}
	if result.Thumb, err = encode(fit(pixels, thumbSize), thumbQuality); err != nil {
		return Result{}, err
	}
	return result, nil
}

func decodeConfig(format Format, data []byte) (image.Config, error) {
	r := bytes.NewReader(data)
	switch format {
	case JPEG:
		return jpeg.DecodeConfig(r)
	case PNG:
		return png.DecodeConfig(r)
	default:
		return webp.DecodeConfig(r)
	}
}

func decode(format Format, data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	switch format {
	case JPEG:
		return jpeg.Decode(r)
	case PNG:
		return png.Decode(r)
	default:
		return webp.Decode(r)
	}
}

// flatten draws the image over a white background: JPEG has no transparency.
func flatten(src image.Image) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}

// fit scales the image down so that its longest side is at most size; smaller images are kept.
func fit(src *image.RGBA, size int) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	longest := max(w, h)
	if longest <= size {
		return src
	}
	dw := max(1, w*size/longest)
	dh := max(1, h*size/longest)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

func encode(img *image.RGBA, quality int) (Variant, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return Variant{}, fmt.Errorf("encode JPEG: %w", err)
	}
	return Variant{Data: out.Bytes(), Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}, nil
}
