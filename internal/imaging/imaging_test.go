package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

var limits = Limits{MaxPixels: 50_000_000}

// solid returns a w×h image whose left half is red and right half blue, so rotations can be seen.
func solid(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out.Bytes()
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out.Bytes()
}

// withExif inserts an APP1 segment with IFD0 holding Orientation and a fake GPS string right after
// the SOI marker of a JPEG file.
func withExif(jpegData []byte, order binary.ByteOrder, orientation uint16) []byte {
	tiff := new(bytes.Buffer)
	if order == binary.LittleEndian {
		tiff.WriteString("II")
	} else {
		tiff.WriteString("MM")
	}
	_ = binary.Write(tiff, order, uint16(42))
	_ = binary.Write(tiff, order, uint32(8))
	_ = binary.Write(tiff, order, uint16(1)) // one IFD entry
	_ = binary.Write(tiff, order, uint16(orientationTag))
	_ = binary.Write(tiff, order, uint16(typeShort))
	_ = binary.Write(tiff, order, uint32(1))
	_ = binary.Write(tiff, order, orientation)
	_ = binary.Write(tiff, order, uint16(0))
	_ = binary.Write(tiff, order, uint32(0)) // no next IFD
	tiff.WriteString("GPSLatitude 55.7558 N")
	segment := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(segment)+2))
	out := append([]byte{}, jpegData[:2]...)
	out = append(out, app1...)
	out = append(out, segment...)
	return append(out, jpegData[2:]...)
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a JPEG: %v", err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

func isBlue(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return b > 0xC000 && r < 0x4000 && g < 0x4000
}

func isWhite(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xF000 && g > 0xF000 && b > 0xF000
}

func TestDetectBySignature(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want Format
	}{
		"jpeg":              {[]byte{0xFF, 0xD8, 0xFF, 0xE0}, JPEG},
		"png":               {[]byte("\x89PNG\r\n\x1a\n...."), PNG},
		"webp":              {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP},
		"gif named as jpeg": {[]byte("GIF89a"), ""},
		"riff but wave":     {[]byte("RIFF\x00\x00\x00\x00WAVEfmt "), ""},
		"html":              {[]byte("<html><script>"), ""},
		"empty":             {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Detect(tc.data)
			if tc.want == "" {
				if !errors.Is(err, ErrUnsupportedType) {
					t.Fatalf("err = %v, want ErrUnsupportedType", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Detect = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestProcessJPEGMakesThreeShrunkVariants(t *testing.T) {
	result, err := Process(jpegBytes(t, solid(2000, 1000)), limits)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	checks := []struct {
		name          string
		variant       Variant
		width, height int
	}{
		{"original", result.Original, 2000, 1000},
		{"preview", result.Preview, 1280, 640},
		{"thumb", result.Thumb, 320, 160},
	}
	for _, c := range checks {
		img := decodeJPEG(t, c.variant.Data)
		if c.variant.Width != c.width || c.variant.Height != c.height ||
			img.Bounds().Dx() != c.width || img.Bounds().Dy() != c.height {
			t.Errorf("%s: %dx%d (decoded %v), want %dx%d", c.name, c.variant.Width, c.variant.Height,
				img.Bounds(), c.width, c.height)
		}
	}
	if result.Source != JPEG {
		t.Errorf("Source = %q", result.Source)
	}
}

func TestProcessKeepsSmallImagesAtTheirSize(t *testing.T) {
	result, err := Process(pngBytes(t, solid(200, 100)), limits)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	for _, v := range []Variant{result.Original, result.Preview, result.Thumb} {
		if v.Width != 200 || v.Height != 100 {
			t.Errorf("variant %dx%d, want 200x100", v.Width, v.Height)
		}
	}
}

func TestProcessAppliesOrientationAndDropsExif(t *testing.T) {
	// Stored 40×20 with red on the left; orientation 6 means "rotate 90° clockwise to display",
	// so the displayed image is 20×40 with red on top.
	for name, order := range map[string]binary.ByteOrder{"intel": binary.LittleEndian, "motorola": binary.BigEndian} {
		t.Run(name, func(t *testing.T) {
			data := withExif(jpegBytes(t, solid(40, 20)), order, 6)

			result, err := Process(data, limits)
			if err != nil {
				t.Fatalf("Process: %v", err)
			}

			img := decodeJPEG(t, result.Original.Data)
			if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 40 {
				t.Fatalf("size %v, want 20x40", img.Bounds())
			}
			if !isRed(img.At(10, 5)) || !isBlue(img.At(10, 35)) {
				t.Errorf("rotation wrong: top %v, bottom %v", img.At(10, 5), img.At(10, 35))
			}
			for _, v := range []Variant{result.Original, result.Preview, result.Thumb} {
				if bytes.Contains(v.Data, []byte("Exif")) || bytes.Contains(v.Data, []byte("GPSLatitude")) {
					t.Error("EXIF data survived re-encoding")
				}
			}
		})
	}
}

func TestOrientMapsEveryValue(t *testing.T) {
	// 3×2 source with distinct pixels; expected position of source pixel (0,0) after orientation.
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range src.Pix {
		src.Pix[i] = byte(i)
	}
	marker := src.RGBAAt(0, 0)
	cases := map[int]struct{ w, h, x, y int }{
		1: {3, 2, 0, 0},
		2: {3, 2, 2, 0},
		3: {3, 2, 2, 1},
		4: {3, 2, 0, 1},
		5: {2, 3, 0, 0},
		6: {2, 3, 1, 0},
		7: {2, 3, 1, 2},
		8: {2, 3, 0, 2},
		9: {3, 2, 0, 0}, // unknown values keep the image
	}
	for orientation, want := range cases {
		got := orient(src, orientation)
		if got.Bounds().Dx() != want.w || got.Bounds().Dy() != want.h {
			t.Errorf("orientation %d: size %v", orientation, got.Bounds())
			continue
		}
		if got.RGBAAt(want.x, want.y) != marker {
			t.Errorf("orientation %d: pixel (0,0) not at (%d,%d)", orientation, want.x, want.y)
		}
	}
}

func TestJPEGOrientationIgnoresBrokenExif(t *testing.T) {
	base := jpegBytes(t, solid(4, 4))
	good := withExif(base, binary.BigEndian, 3)
	cases := map[string][]byte{
		"no exif":           base,
		"not a jpeg marker": {0xFF, 0xD8, 0x00, 0x00, 0x00, 0x00},
		"truncated segment": good[:12],
		"bad byte order":    bytes.Replace(good, []byte("MM"), []byte("XX"), 1),
		"bad tiff magic":    bytes.Replace(good, []byte{'M', 'M', 0, 42}, []byte{'M', 'M', 0, 43}, 1),
		"orientation zero":  withExif(base, binary.BigEndian, 0),
		"orientation 99":    withExif(base, binary.BigEndian, 99),
	}
	for name, data := range cases {
		if got := jpegOrientation(data); got != orientationNormal {
			t.Errorf("%s: orientation %d, want normal", name, got)
		}
	}
	if got := jpegOrientation(good); got != 3 {
		t.Errorf("valid EXIF: orientation %d, want 3", got)
	}
}

func TestTIFFOrientationRejectsMalformedIFD(t *testing.T) {
	be := binary.BigEndian
	header := func(ifd uint32, entries uint16) []byte {
		b := []byte{'M', 'M', 0, 42, 0, 0, 0, 0, 0, 0}
		be.PutUint32(b[4:], ifd)
		be.PutUint16(b[8:], entries)
		return b
	}
	wrongType := append(header(8, 1), 0x01, 0x12, 0, 4, 0, 0, 0, 1, 0, 6, 0, 0)
	otherTag := append(header(8, 1), 0x01, 0x0F, 0, 2, 0, 0, 0, 1, 0, 6, 0, 0)
	cases := map[string][]byte{
		"too short":        {'M', 'M'},
		"ifd out of range": header(500, 0),
		"entry truncated":  header(8, 2),
		"wrong value type": wrongType,
		"no orientation":   otherTag,
	}
	for name, data := range cases {
		if got := tiffOrientation(data); got != orientationNormal {
			t.Errorf("%s: orientation %d, want normal", name, got)
		}
	}
}

func TestProcessFlattensTransparencyOnWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := range 10 {
		for x := range 5 {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}

	result, err := Process(pngBytes(t, img), limits)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	out := decodeJPEG(t, result.Original.Data)
	if !isRed(out.At(1, 5)) || !isWhite(out.At(8, 5)) {
		t.Errorf("left %v, right %v; want red and white", out.At(1, 5), out.At(8, 5))
	}
}

func TestProcessWebP(t *testing.T) {
	lossy, err := Process(readTestdata(t, "lossy.webp"), limits)
	if err != nil {
		t.Fatalf("lossy: %v", err)
	}
	if lossy.Source != WebP || lossy.Original.Width != 60 || lossy.Original.Height != 40 {
		t.Errorf("lossy: %+v", lossy.Original)
	}

	alpha, err := Process(readTestdata(t, "alpha.webp"), limits)
	if err != nil {
		t.Fatalf("alpha: %v", err)
	}
	out := decodeJPEG(t, alpha.Original.Data)
	if !isRed(out.At(3, 10)) || !isWhite(out.At(26, 10)) {
		t.Errorf("alpha: left %v, right %v; want red and white", out.At(3, 10), out.At(26, 10))
	}
}

// pngHeaderOnly builds a PNG with a valid IHDR for the given size and no image data: decoding it
// would fail, so a rejection proves the size was checked before decoding.
func pngHeaderOnly(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	chunk := append([]byte("IHDR"), ihdr...)
	out := bytes.NewBuffer([]byte("\x89PNG\r\n\x1a\n"))
	_ = binary.Write(out, binary.BigEndian, uint32(len(ihdr)))
	out.Write(chunk)
	_ = binary.Write(out, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return out.Bytes()
}

func TestProcessRejectsDecompressionBombBeforeDecoding(t *testing.T) {
	_, err := Process(pngHeaderOnly(100_000, 100_000), limits)
	if !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("err = %v, want ErrTooManyPixels", err)
	}
}

func TestProcessRejectsDamagedAndFakeFiles(t *testing.T) {
	cases := map[string][]byte{
		"text with jpeg name":     []byte("just text"),
		"jpeg signature only":     {0xFF, 0xD8, 0xFF, 0xE0, 0x00},
		"png header without data": pngHeaderOnly(10, 10),
		"webp signature only":     []byte("RIFF\x10\x00\x00\x00WEBPJUNK"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Process(data, limits); !errors.Is(err, ErrUnsupportedType) {
				t.Fatalf("err = %v, want ErrUnsupportedType", err)
			}
		})
	}
}

func TestProcessRejectsZeroSizedImage(t *testing.T) {
	if _, err := Process(pngHeaderOnly(0, 10), limits); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}
