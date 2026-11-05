package imaging

import "image"

// orient returns the image as it should be displayed for the given EXIF orientation.
func orient(src *image.RGBA, orientation int) *image.RGBA {
	if orientation <= orientationNormal || orientation > orientationTransverse {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 { // orientations 5–8 swap width and height
		dw, dh = h, w
	}
	source := sourceOf(orientation, w, h)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			sx, sy := source(x, y)
			s := src.PixOffset(src.Rect.Min.X+sx, src.Rect.Min.Y+sy)
			d := dst.PixOffset(x, y)
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// sourceOf maps a pixel of the displayed image to the pixel of the stored one.
func sourceOf(orientation, w, h int) func(x, y int) (int, int) {
	switch orientation {
	case 2: // mirrored horizontally
		return func(x, y int) (int, int) { return w - 1 - x, y }
	case 3: // rotated 180°
		return func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case 4: // mirrored vertically
		return func(x, y int) (int, int) { return x, h - 1 - y }
	case 5: // transposed
		return func(x, y int) (int, int) { return y, x }
	case 6: // to be rotated 90° clockwise
		return func(x, y int) (int, int) { return y, h - 1 - x }
	case 7: // transversed
		return func(x, y int) (int, int) { return w - 1 - y, h - 1 - x }
	default: // 8: to be rotated 90° counter-clockwise
		return func(x, y int) (int, int) { return w - 1 - y, x }
	}
}
