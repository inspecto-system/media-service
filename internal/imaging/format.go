// Package imaging checks, decodes and re-encodes uploaded photos (ADR-025). It uses only the
// standard library and golang.org/x/image; every stored variant is a freshly encoded JPEG, so no
// metadata of the uploaded file survives.
package imaging

import (
	"bytes"
	"errors"
)

// Format is the type of an uploaded file, determined by its signature.
type Format string

// Accepted upload formats.
const (
	JPEG Format = "image/jpeg"
	PNG  Format = "image/png"
	WebP Format = "image/webp"
)

// Formats lists the accepted upload formats in a stable order.
var Formats = []Format{JPEG, PNG, WebP}

// ErrUnsupportedType means the file is not a valid JPEG, PNG or WebP image.
var ErrUnsupportedType = errors.New("unsupported or damaged image")

var (
	jpegSignature = []byte{0xFF, 0xD8, 0xFF}
	pngSignature  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
	riffSignature = []byte("RIFF")
	webpSignature = []byte("WEBP")
)

// Detect returns the format from the first bytes of the file; the file name and the declared
// content type are never trusted (SECURITY.md §4).
func Detect(data []byte) (Format, error) {
	switch {
	case bytes.HasPrefix(data, jpegSignature):
		return JPEG, nil
	case bytes.HasPrefix(data, pngSignature):
		return PNG, nil
	case len(data) >= 12 && bytes.Equal(data[0:4], riffSignature) && bytes.Equal(data[8:12], webpSignature):
		return WebP, nil
	default:
		return "", ErrUnsupportedType
	}
}
